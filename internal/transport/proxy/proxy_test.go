package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itdar/shield-agent/internal/middleware"
)

func TestStreamableProxy_PostForward(t *testing.T) {
	// Upstream mock that echoes a JSON-RPC response.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  map[string]any{"status": "ok"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	chain := middleware.NewSwappableChain(middleware.NewChain())
	proxy := NewStreamableProxy(upstream.URL, chain, slog.Default(), []string{"*"})
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	reqBody := `{"jsonrpc":"2.0","method":"tools/list","id":1}`
	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", result["jsonrpc"])
	}
}

func TestStreamableProxy_MethodNotAllowed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	chain := middleware.NewSwappableChain(middleware.NewChain())
	proxy := NewStreamableProxy(upstream.URL, chain, slog.Default(), nil)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestStreamableProxy_CORS(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	chain := middleware.NewSwappableChain(middleware.NewChain())
	proxy := NewStreamableProxy(upstream.URL, chain, slog.Default(), []string{"*"})
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/mcp", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for OPTIONS, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected CORS header *, got %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestStreamableProxy_MiddlewareBlocks(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)) //nolint:errcheck
	}))
	defer upstream.Close()

	// Guard with rate limit of 1 per minute.
	guard := middleware.NewGuardMiddleware(middleware.GuardConfig{RateLimitPerMin: 1}, slog.Default(), nil)
	chain := middleware.NewSwappableChain(middleware.NewChain(guard))
	proxy := NewStreamableProxy(upstream.URL, chain, slog.Default(), nil)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	reqBody := `{"jsonrpc":"2.0","method":"test","id":1}`

	// First request should pass through to upstream.
	resp1, _ := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(reqBody))
	resp1.Body.Close()
	if upstreamCalls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", upstreamCalls)
	}

	// Second request should be blocked by rate limit.
	resp2, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("second POST failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 (blocked), got %d", resp2.StatusCode)
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected upstream not called again, got %d calls", upstreamCalls)
	}
}

func TestStreamableProxy_UpstreamDown(t *testing.T) {
	// Use a closed server URL.
	chain := middleware.NewSwappableChain(middleware.NewChain())
	proxy := NewStreamableProxy("http://127.0.0.1:1", chain, slog.Default(), nil)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	reqBody := `{"jsonrpc":"2.0","method":"test","id":1}`
	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}

func TestSSEProxy_MessagesWithoutSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	chain := middleware.NewSwappableChain(middleware.NewChain())
	proxy := NewSSEProxy(upstream.URL, chain, slog.Default(), nil)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	// POST without sessionId should return 400.
	resp, err := http.Post(srv.URL+"/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSSEProxy_MessagesUnknownSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	chain := middleware.NewSwappableChain(middleware.NewChain())
	proxy := NewSSEProxy(upstream.URL, chain, slog.Default(), nil)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/messages?sessionId=nonexistent", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestSSEProxy_SSEMethodNotAllowed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	chain := middleware.NewSwappableChain(middleware.NewChain())
	proxy := NewSSEProxy(upstream.URL, chain, slog.Default(), nil)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/sse", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestCORSHeaders(t *testing.T) {
	tests := []struct {
		name     string
		allowed  []string
		origin   string
		expected string
	}{
		{"wildcard", []string{"*"}, "http://example.com", "*"},
		{"match", []string{"http://example.com"}, "http://example.com", "http://example.com"},
		{"no match", []string{"http://other.com"}, "http://example.com", ""},
		{"empty list", nil, "http://example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodOptions, "/", nil)
			r.Header.Set("Origin", tt.origin)
			SetCORSHeaders(w, r, tt.allowed)

			got := w.Header().Get("Access-Control-Allow-Origin")
			if got != tt.expected {
				t.Errorf("expected CORS header %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestParseSSEEvent(t *testing.T) {
	lines := []string{"event: message", "data: {\"test\":true}"}
	ev := parseSSEEvent(lines)
	if ev.typ != "message" {
		t.Errorf("expected type 'message', got %q", ev.typ)
	}
	if ev.data != "{\"test\":true}" {
		t.Errorf("expected data '{\"test\":true}', got %q", ev.data)
	}
}
