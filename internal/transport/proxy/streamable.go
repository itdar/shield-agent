package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/itdar/shield-agent/internal/middleware"
)

// StreamableProxy is an HTTP proxy that applies middleware between an MCP client
// using Streamable HTTP transport and an upstream MCP server.
//
// Protocol:
//   - POST /mcp (or /) → apply middleware to request, forward to upstream /mcp, relay response
type StreamableProxy struct {
	upstream       string
	chain          *middleware.SwappableChain
	logger         *slog.Logger
	client         *http.Client
	allowedOrigins []string
}

// NewStreamableProxy creates a new Streamable HTTP proxy.
func NewStreamableProxy(upstream string, chain *middleware.SwappableChain, logger *slog.Logger, allowedOrigins []string) *StreamableProxy {
	return &StreamableProxy{
		upstream:       strings.TrimRight(upstream, "/"),
		chain:          chain,
		logger:         logger,
		client:         &http.Client{Timeout: 60 * time.Second},
		allowedOrigins: allowedOrigins,
	}
}

// Handler returns the http.Handler for this proxy.
// Handles both POST /mcp and POST /.
func (p *StreamableProxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", p.handleMCP)
	mux.HandleFunc("/mcp/", p.handleMCP)
	mux.HandleFunc("/", p.handleMCP)
	return mux
}

func (p *StreamableProxy) handleMCP(w http.ResponseWriter, r *http.Request) {
	// CORS preflight.
	SetCORSHeaders(w, r, p.allowedOrigins)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Streamable HTTP supports GET (open SSE stream), POST (send message), DELETE (close session).
	// POST is the primary method.
	if r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// GET/DELETE are for session management — proxy directly without middleware.
	if r.Method == http.MethodGet || r.Method == http.MethodDelete {
		p.proxyRaw(w, r)
		return
	}

	// POST: apply middleware.
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "request read error", http.StatusBadRequest)
		return
	}

	p.logger.Debug("streamable: request received",
		slog.String("path", r.URL.Path),
		slog.Int("bytes", len(body)),
	)

	// Apply middleware chain (auth + logging) with client IP.
	mwCtx, body, chainErr := applyRequestWithIP(r.Context(), body, p.chain, p.logger, r.RemoteAddr)
	if chainErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(body) //nolint:errcheck
		return
	}

	// Determine upstream URL.
	// If the upstream already includes a path (e.g. https://host/mcp), use it as-is.
	// Only append the request path when the upstream has no path component.
	upstreamURL := buildUpstreamURL(p.upstream, r.URL.Path, r.URL.RawQuery)

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		p.logger.Error("streamable: upstream request creation failed", slog.String("error", err.Error()))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	// Copy relevant headers from the original request.
	upReq.Header.Set("Content-Type", "application/json")
	if accept := r.Header.Get("Accept"); accept != "" {
		upReq.Header.Set("Accept", accept)
	}
	if sessionID := r.Header.Get("Mcp-Session-Id"); sessionID != "" {
		upReq.Header.Set("Mcp-Session-Id", sessionID)
	}

	upResp, err := p.client.Do(upReq)
	if err != nil {
		p.logger.Error("streamable: upstream request failed",
			slog.String("url", upstreamURL),
			slog.String("error", err.Error()),
		)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	// Relay upstream response headers.
	for k, vs := range upResp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(upResp.StatusCode)

	// If the response is an SSE stream, relay chunk by chunk and apply middleware.
	ct := upResp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		p.relaySSEResponse(w, upResp.Body)
		return
	}

	// Plain JSON response: read fully, apply ProcessResponse, then return.
	respBody, err := io.ReadAll(upResp.Body)
	if err != nil {
		return
	}

	out := applyResponse(mwCtx, respBody, p.chain, p.logger)
	if out == nil {
		out = respBody // if blocked, keep original (passing through is safer than dropping in streamable)
	}
	w.Write(out) //nolint:errcheck
}

// proxyRaw forwards methods like GET/DELETE directly to upstream without middleware.
func (p *StreamableProxy) proxyRaw(w http.ResponseWriter, r *http.Request) {
	upstreamURL := buildUpstreamURL(p.upstream, r.URL.Path, r.URL.RawQuery)

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	// Copy headers.
	for k, vs := range r.Header {
		for _, v := range vs {
			upReq.Header.Add(k, v)
		}
	}

	upResp, err := p.client.Do(upReq)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	for k, vs := range upResp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(upResp.StatusCode)

	ct := upResp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		p.relaySSEResponse(w, upResp.Body)
		return
	}
	io.Copy(w, upResp.Body) //nolint:errcheck
}

// relaySSEResponse relays the upstream SSE stream to the client in real time.
// Applies middleware (ProcessResponse) to the data field of each event.
func (p *StreamableProxy) relaySSEResponse(w http.ResponseWriter, body io.Reader) {
	flusher, canFlush := w.(http.Flusher)
	scanner := bufio.NewScanner(body)
	var lines []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			ev := parseSSEEvent(lines)
			lines = lines[:0]

			var sb strings.Builder
			if ev.typ != "" {
				sb.WriteString("event: ")
				sb.WriteString(ev.typ)
				sb.WriteString("\n")
			}
			if ev.data != "" {
				// Apply ProcessResponse.
				data := applyResponse(context.Background(), []byte(ev.data), p.chain, p.logger)
				if data == nil {
					data = []byte(ev.data) // if blocked, keep original
				}
				sb.WriteString("data: ")
				sb.Write(data)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")

			w.Write([]byte(sb.String())) //nolint:errcheck
			if canFlush {
				flusher.Flush()
			}
		} else {
			// Non-event lines (id:, retry:, etc.) pass through as-is.
			if !strings.HasPrefix(line, "event: ") && !strings.HasPrefix(line, "data: ") {
				w.Write([]byte(line + "\n")) //nolint:errcheck
			} else {
				lines = append(lines, line)
			}
		}
	}
}
