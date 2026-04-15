package proxy

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/middleware/httpauth"
	"github.com/itdar/shield-agent/internal/monitor"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/telemetry"
)

// HTTPAuthDeps holds dependencies for A2A and HTTP API authentication in proxy mode.
type HTTPAuthDeps struct {
	Store    auth.KeyStore
	Mode     string // "open", "verified", "closed"
	Logger   *slog.Logger
	DB       *storage.DB
	Metrics  *monitor.Metrics
	Recorder *telemetry.Collector
}

// ProtocolAwareHandler wraps an MCP transport handler (SSE or Streamable HTTP)
// and adds support for A2A and plain HTTP API protocols via auto-detection.
type ProtocolAwareHandler struct {
	mcpHandler     http.Handler
	upstream       string
	protocolHint   Protocol
	httpDeps       *HTTPAuthDeps
	logger         *slog.Logger
	allowedOrigins []string
	client         *http.Client
}

// NewProtocolAwareHandler creates a handler that detects the request protocol
// and routes MCP traffic to mcpHandler while handling A2A/HTTP API inline.
func NewProtocolAwareHandler(
	mcpHandler http.Handler,
	upstream string,
	hint Protocol,
	deps *HTTPAuthDeps,
	logger *slog.Logger,
	allowedOrigins []string,
) *ProtocolAwareHandler {
	return &ProtocolAwareHandler{
		mcpHandler:     mcpHandler,
		upstream:       strings.TrimRight(upstream, "/"),
		protocolHint:   hint,
		httpDeps:       deps,
		logger:         logger,
		allowedOrigins: allowedOrigins,
		client:         &http.Client{Timeout: 60 * time.Second},
	}
}

// ServeHTTP dispatches to the appropriate handler based on detected protocol.
func (h *ProtocolAwareHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS preflight always passes through.
	if r.Method == http.MethodOptions {
		h.mcpHandler.ServeHTTP(w, r)
		return
	}

	// Non-POST methods (GET, DELETE) are MCP session management.
	if r.Method != http.MethodPost {
		h.mcpHandler.ServeHTTP(w, r)
		return
	}

	// If hint is explicitly MCP, skip detection.
	if h.protocolHint == ProtoMCP {
		h.mcpHandler.ServeHTTP(w, r)
		return
	}

	// If hint is explicitly A2A or HTTP API, handle without body-based detection.
	if h.protocolHint == ProtoA2A || h.protocolHint == ProtoHTTPAPI {
		h.handleHTTPProtocol(w, r, h.protocolHint)
		return
	}

	// Auto-detect: read body to inspect protocol signals.
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "request read error", http.StatusBadRequest)
		return
	}

	proto := DetectProtocol(r, body)
	h.logger.Debug("protocol detected",
		slog.String("protocol", proto.String()),
		slog.String("path", r.URL.Path),
	)

	// Restore body for downstream handlers.
	r.Body = io.NopCloser(bytes.NewReader(body))

	switch proto {
	case ProtoA2A, ProtoHTTPAPI:
		h.handleHTTPProtocolWithBody(w, r, body, proto)
	default:
		h.mcpHandler.ServeHTTP(w, r)
	}
}

// handleHTTPProtocol reads the body then delegates to handleHTTPProtocolWithBody.
func (h *ProtocolAwareHandler) handleHTTPProtocol(w http.ResponseWriter, r *http.Request, proto Protocol) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "request read error", http.StatusBadRequest)
		return
	}
	h.handleHTTPProtocolWithBody(w, r, body, proto)
}

// handleHTTPProtocolWithBody applies HTTP-level auth, forwards to upstream, and logs.
func (h *ProtocolAwareHandler) handleHTTPProtocolWithBody(w http.ResponseWriter, r *http.Request, body []byte, proto Protocol) {
	SetCORSHeaders(w, r, h.allowedOrigins)
	startedAt := time.Now()

	// Auth check.
	authStatus, agentHash, authErr := h.checkAuth(r, body, proto)
	if authErr != nil {
		http.Error(w, authErr.Error(), http.StatusUnauthorized)
		return
	}

	// Forward to upstream.
	upstreamURL := buildUpstreamURL(h.upstream, r.URL.Path, r.URL.RawQuery)
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		h.logger.Error("upstream request creation failed", slog.String("error", err.Error()))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	copyRequestHeaders(r.Header, upReq.Header)

	upResp, err := h.client.Do(upReq)
	if err != nil {
		h.logger.Error("upstream request failed",
			slog.String("protocol", proto.String()),
			slog.String("url", upstreamURL),
			slog.String("error", err.Error()),
		)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	// Relay response headers and body.
	for k, vs := range upResp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(upResp.StatusCode)
	respBody, _ := io.ReadAll(upResp.Body)
	w.Write(respBody) //nolint:errcheck

	// Log request/response pair.
	h.logRequest(r, body, proto, authStatus, agentHash, startedAt, upResp.StatusCode, len(respBody))
}

// checkAuth verifies the agent signature for A2A / HTTP API requests.
func (h *ProtocolAwareHandler) checkAuth(r *http.Request, body []byte, proto Protocol) (status, agentHash string, err error) {
	if h.httpDeps == nil {
		return "unsigned", "", nil
	}

	sigHeader := "X-Agent-Signature"
	if proto == ProtoA2A {
		sigHeader = "X-A2A-Signature"
	}

	agentID := r.Header.Get("X-Agent-ID")
	sigHex := r.Header.Get(sigHeader)

	recordAuth := func(s string) {
		if h.httpDeps.Metrics != nil {
			h.httpDeps.Metrics.AuthTotal.WithLabelValues(s).Inc()
		}
	}

	if agentID == "" || sigHex == "" {
		recordAuth("unsigned")
		h.logger.Warn("unsigned request",
			slog.String("protocol", proto.String()),
			slog.String("path", r.URL.Path),
		)
		if h.httpDeps.Mode == "closed" {
			return "unsigned", "", fmt.Errorf("unauthorized: signature required")
		}
		return "unsigned", "", nil
	}

	aHash := auth.AgentIDHash(agentID)

	// Resolve public key (DID or key store).
	var pubKey ed25519.PublicKey
	var resolveErr error
	if strings.HasPrefix(agentID, "did:key:") {
		pubKey, resolveErr = auth.ResolveDIDKey(agentID)
	} else {
		pubKey, resolveErr = h.httpDeps.Store.PublicKey(agentID)
	}
	if resolveErr != nil {
		recordAuth("failed")
		h.logger.Warn("unknown agent",
			slog.String("agent_id_hash", aHash),
			slog.String("protocol", proto.String()),
		)
		if h.httpDeps.Mode == "closed" || h.httpDeps.Mode == "verified" {
			return "failed", aHash, fmt.Errorf("unauthorized: unknown agent")
		}
		return "failed", aHash, nil
	}

	// Verify Ed25519 signature.
	sigBytes, decErr := hex.DecodeString(sigHex)
	if decErr != nil {
		recordAuth("failed")
		if h.httpDeps.Mode == "closed" || h.httpDeps.Mode == "verified" {
			return "failed", aHash, fmt.Errorf("unauthorized: invalid signature encoding")
		}
		return "failed", aHash, nil
	}

	reqHash := httpauth.HashRequest(r.Method, r.URL.Path, body)
	if !ed25519.Verify(pubKey, reqHash, sigBytes) {
		recordAuth("failed")
		h.logger.Warn("signature verification failed",
			slog.String("agent_id_hash", aHash),
			slog.String("protocol", proto.String()),
		)
		if h.httpDeps.Mode == "closed" || h.httpDeps.Mode == "verified" {
			return "failed", aHash, fmt.Errorf("unauthorized: signature verification failed")
		}
		return "failed", aHash, nil
	}

	recordAuth("verified")
	h.logger.Info("request verified",
		slog.String("agent_id_hash", aHash),
		slog.String("protocol", proto.String()),
	)
	return "verified", aHash, nil
}

// logRequest writes an action log entry for A2A / HTTP API requests.
func (h *ProtocolAwareHandler) logRequest(
	r *http.Request, body []byte, proto Protocol,
	authStatus, agentHash string,
	startedAt time.Time, respStatus, respSize int,
) {
	if h.httpDeps == nil || h.httpDeps.DB == nil {
		return
	}

	var method string
	switch proto {
	case ProtoA2A:
		var probe jsonrpcProbe
		if json.Unmarshal(body, &probe) == nil && probe.Method != "" {
			method = probe.Method
		} else {
			method = "unknown"
		}
	default:
		method = fmt.Sprintf("%s %s", r.Method, r.URL.Path)
	}

	latencyMs := float64(time.Since(startedAt).Milliseconds())
	success := respStatus < 400

	ip := extractClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))

	entry := storage.ActionLog{
		Timestamp:   time.Now().UTC(),
		AgentIDHash: agentHash,
		Method:      method,
		Direction:   "in",
		Success:     success,
		LatencyMs:   latencyMs,
		PayloadSize: len(body),
		AuthStatus:  authStatus,
		ErrorCode:   protoHTTPErrorCode(respStatus),
		IPAddress:   ip,
	}

	go func() {
		if err := h.httpDeps.DB.Insert(entry); err != nil {
			h.logger.Error("failed to log request", slog.String("error", err.Error()))
		}
	}()

	// Prometheus metrics.
	if h.httpDeps.Metrics != nil {
		h.httpDeps.Metrics.MessagesTotal.WithLabelValues("in", method).Inc()
		if latencyMs > 0 {
			h.httpDeps.Metrics.MessageLatency.WithLabelValues(method).Observe(latencyMs / 1000.0)
		}
	}

	// Telemetry.
	if h.httpDeps.Recorder != nil {
		h.httpDeps.Recorder.Record(telemetry.Event{
			AgentIDHash:      agentHash,
			Timestamp:        entry.Timestamp,
			Method:           method,
			Direction:        "request",
			Success:          success,
			LatencyMs:        latencyMs,
			PayloadSizeBytes: len(body),
			AuthStatus:       authStatus,
		})
	}
}

// copyRequestHeaders copies headers from src to dst, skipping hop-by-hop headers.
func copyRequestHeaders(src, dst http.Header) {
	hopByHop := map[string]bool{
		"connection": true, "keep-alive": true, "transfer-encoding": true,
		"te": true, "trailer": true, "upgrade": true,
	}
	for k, vs := range src {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// extractClientIP returns the client IP, preferring X-Forwarded-For.
func extractClientIP(remoteAddr, xForwardedFor string) string {
	if xff := strings.TrimSpace(xForwardedFor); xff != "" {
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return xff
	}
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		return remoteAddr[:idx]
	}
	return remoteAddr
}

// protoHTTPErrorCode returns the status code as a string for error responses.
func protoHTTPErrorCode(status int) string {
	if status >= 400 {
		return fmt.Sprintf("%d", status)
	}
	return ""
}
