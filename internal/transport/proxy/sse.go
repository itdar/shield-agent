// Package proxy provides HTTP proxy handlers for MCP SSE and Streamable HTTP transports.
package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/itdar/shield-agent/internal/middleware"
)

// SSEProxy is an HTTP proxy that applies middleware between an MCP client
// (e.g. Claude Desktop) using SSE transport and an upstream MCP server.
//
// Protocol:
//   - GET  /sse          → connect to upstream SSE and relay events
//   - POST /messages     → apply middleware to request, then forward to upstream /messages
type SSEProxy struct {
	upstream       string
	chain          *middleware.SwappableChain
	logger         *slog.Logger
	sessions       *sessionStore
	allowedOrigins []string
}

// NewSSEProxy creates a new SSE proxy.
func NewSSEProxy(upstream string, chain *middleware.SwappableChain, logger *slog.Logger, allowedOrigins []string) *SSEProxy {
	return &SSEProxy{
		upstream:       strings.TrimRight(upstream, "/"),
		chain:          chain,
		logger:         logger,
		sessions:       newSessionStore(),
		allowedOrigins: allowedOrigins,
	}
}

// Handler returns the http.Handler for this proxy.
func (p *SSEProxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", p.handleSSE)
	mux.HandleFunc("/messages", p.handleMessages)
	mux.HandleFunc("/messages/", p.handleMessages)
	return mux
}

func (p *SSEProxy) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this server", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	// Connect to upstream SSE.
	upSSEURL := p.upstream + "/sse"
	upReq, err := http.NewRequestWithContext(ctx, http.MethodGet, upSSEURL, nil)
	if err != nil {
		p.logger.Error("sse: upstream request creation failed", slog.String("error", err.Error()))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	upReq.Header.Set("Accept", "text/event-stream")
	upReq.Header.Set("Cache-Control", "no-cache")

	// SSE is a long-lived connection — no timeout.
	upClient := &http.Client{Timeout: 0}
	upResp, err := upClient.Do(upReq)
	if err != nil {
		p.logger.Error("sse: upstream connection failed",
			slog.String("url", upSSEURL),
			slog.String("error", err.Error()),
		)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	// Create local session.
	localID := uuid.New().String()
	sess := &session{
		events: make(chan []byte, 512),
	}
	p.sessions.add(localID, sess)
	defer p.sessions.remove(localID)

	p.logger.Info("sse: session started", slog.String("session_id", localID))

	// done channel signals goroutine termination.
	done := make(chan struct{})
	defer close(done)

	// Goroutine that reads from upstream SSE.
	go func() {
		defer close(sess.events)

		scanner := bufio.NewScanner(upResp.Body)
		var lines []string

		send := func(event []byte) bool {
			select {
			case sess.events <- event:
				return true
			case <-done:
				return false
			}
		}

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				// SSE event block complete.
				ev := parseSSEEvent(lines)
				lines = lines[:0]

				switch ev.typ {
				case "endpoint":
					// Store upstream message URL (convert to absolute URL).
					upMsgURL := ev.data
					if strings.HasPrefix(upMsgURL, "/") {
						upMsgURL = p.upstream + upMsgURL
					}
					sess.setUpstreamMsgURL(upMsgURL)

					// Tell the client our local endpoint instead.
					localPath := fmt.Sprintf("/messages?sessionId=%s", localID)
					event := fmt.Sprintf("event: endpoint\ndata: %s\n\n", localPath)
					p.logger.Debug("sse: endpoint established",
						slog.String("upstream_msg_url", upMsgURL),
						slog.String("local_path", localPath),
					)
					if !send([]byte(event)) {
						return
					}

				default:
					if ev.data == "" {
						continue
					}
					// Apply middleware to response data (LogMiddleware records latency).
					data := applyResponse(ctx, []byte(ev.data), p.chain, p.logger)
					if data == nil {
						continue // middleware blocked
					}

					var sb strings.Builder
					if ev.typ != "" {
						sb.WriteString("event: ")
						sb.WriteString(ev.typ)
						sb.WriteString("\n")
					}
					sb.WriteString("data: ")
					sb.Write(data)
					sb.WriteString("\n\n")
					if !send([]byte(sb.String())) {
						return
					}
				}
			} else {
				lines = append(lines, line)
			}
		}

		if err := scanner.Err(); err != nil {
			p.logger.Debug("sse: upstream scanner stopped", slog.String("error", err.Error()))
		}
	}()

	// Set SSE headers for the client.
	SetCORSHeaders(w, r, p.allowedOrigins)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Pull events from the session channel and relay to the client.
	for {
		select {
		case event, ok := <-sess.events:
			if !ok {
				p.logger.Info("sse: upstream closed", slog.String("session_id", localID))
				return
			}
			if _, err := w.Write(event); err != nil {
				p.logger.Debug("sse: client write error", slog.String("error", err.Error()))
				return
			}
			flusher.Flush()
		case <-ctx.Done():
			p.logger.Info("sse: client disconnected", slog.String("session_id", localID))
			return
		}
	}
}

func (p *SSEProxy) handleMessages(w http.ResponseWriter, r *http.Request) {
	SetCORSHeaders(w, r, p.allowedOrigins)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "missing sessionId query param", http.StatusBadRequest)
		return
	}

	sess, ok := p.sessions.get(sessionID)
	if !ok {
		p.logger.Warn("sse: unknown session", slog.String("session_id", sessionID))
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	upMsgURL := sess.getUpstreamMsgURL()
	if upMsgURL == "" {
		http.Error(w, "session not ready yet (endpoint not received from upstream)", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "request read error", http.StatusBadRequest)
		return
	}

	p.logger.Debug("sse: message received",
		slog.String("session_id", sessionID),
		slog.Int("bytes", len(body)),
	)

	// Apply middleware chain (auth + logging).
	body, chainErr := applyRequest(r.Context(), body, p.chain, p.logger)
	if chainErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(body) //nolint:errcheck
		return
	}

	// Forward to upstream.
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upMsgURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "upstream request error", http.StatusBadGateway)
		return
	}
	upReq.Header.Set("Content-Type", "application/json")

	upClient := &http.Client{Timeout: 30 * time.Second}
	upResp, err := upClient.Do(upReq)
	if err != nil {
		p.logger.Error("sse: upstream message forward failed", slog.String("error", err.Error()))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	// Return upstream response status code as-is (typically 202 Accepted).
	w.WriteHeader(upResp.StatusCode)
}

// sseEvent holds a parsed SSE event block.
type sseEvent struct {
	typ  string
	data string
}

func parseSSEEvent(lines []string) sseEvent {
	var ev sseEvent
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.typ = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return ev
}
