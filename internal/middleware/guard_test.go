package middleware

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/itdar/shield-agent/internal/jsonrpc"
)

func TestGuardMiddleware_Name(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{}, slog.Default(), nil)
	if g.Name() != "guard" {
		t.Fatalf("expected %q, got %q", "guard", g.Name())
	}
}

func TestGuardMiddleware_PassThrough(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{}, slog.Default(), nil)
	req := &jsonrpc.Request{Method: "tools/list"}
	got, err := g.ProcessRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != req {
		t.Fatal("expected the same request to be returned")
	}
}

func TestGuardMiddleware_RateLimitExceeded(t *testing.T) {
	rejected := 0
	g := NewGuardMiddleware(GuardConfig{RateLimitPerMin: 2}, slog.Default(), func() { rejected++ })

	req := &jsonrpc.Request{Method: "tools/call"}
	ctx := context.Background()

	// First two calls should succeed
	for i := 0; i < 2; i++ {
		if _, err := g.ProcessRequest(ctx, req); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}

	// Third call should be rejected
	_, err := g.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected rate limit error, got nil")
	}
	if rejected != 1 {
		t.Fatalf("expected onReject called once, got %d", rejected)
	}
}

func TestGuardMiddleware_RateLimitNotExceeded(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{RateLimitPerMin: 10}, slog.Default(), nil)
	req := &jsonrpc.Request{Method: "tools/call"}
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := g.ProcessRequest(ctx, req); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}
}

func TestGuardMiddleware_SizeExceeded(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{MaxBodySize: 10}, slog.Default(), nil)
	req := &jsonrpc.Request{
		Method: "tools/call",
		Params: []byte(`{"tool":"long-tool-name","arguments":{"key":"value"}}`),
	}
	_, err := g.ProcessRequest(context.Background(), req)
	if err == nil {
		t.Fatal("expected size limit error, got nil")
	}
}

func TestGuardMiddleware_SizeNotExceeded(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{MaxBodySize: 1024}, slog.Default(), nil)
	req := &jsonrpc.Request{
		Method: "tools/call",
		Params: []byte(`{"tool":"x"}`),
	}
	_, err := g.ProcessRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGuardMiddleware_NoLimits(t *testing.T) {
	// Zero values mean unlimited — no errors expected regardless of size or count
	g := NewGuardMiddleware(GuardConfig{RateLimitPerMin: 0, MaxBodySize: 0}, slog.Default(), nil)
	req := &jsonrpc.Request{Method: "m", Params: []byte(`{"k":"` + string(make([]byte, 10000)) + `"}`)}
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		if _, err := g.ProcessRequest(ctx, req); err != nil {
			t.Fatalf("unexpected error on call %d: %v", i, err)
		}
	}
}

func TestGuardMiddleware_BruteForce(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{
		BruteForceMaxFails: 3,
		BruteForceWindow:   1 * time.Minute,
		BruteForceBlockDur: 1 * time.Minute,
	}, slog.Default(), nil)

	ctx := context.Background()
	req := &jsonrpc.Request{Method: "tools/call"}

	// Before any failures, request should pass.
	if _, err := g.ProcessRequest(ctx, req); err != nil {
		t.Fatalf("expected pass before failures: %v", err)
	}

	// Record 3 failures.
	for i := 0; i < 3; i++ {
		g.RecordFailure("tools/call")
	}

	// Now should be blocked.
	_, err := g.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected brute force block, got nil")
	}
}

func TestGuardMiddleware_BruteForceDisabled(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{BruteForceMaxFails: 0}, slog.Default(), nil)
	// RecordFailure should be a no-op when disabled.
	g.RecordFailure("test")
	req := &jsonrpc.Request{Method: "test"}
	if _, err := g.ProcessRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGuardMiddleware_MalformedJSONRPC(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{ValidateJSONRPC: true}, slog.Default(), nil)
	ctx := context.Background()

	// Empty method should fail.
	_, err := g.ProcessRequest(ctx, &jsonrpc.Request{JSONRPC: "2.0", Method: ""})
	if err == nil {
		t.Fatal("expected error for empty method")
	}

	// Invalid version should fail.
	_, err = g.ProcessRequest(ctx, &jsonrpc.Request{JSONRPC: "1.0", Method: "test"})
	if err == nil {
		t.Fatal("expected error for invalid version")
	}

	// Valid request should pass.
	_, err = g.ProcessRequest(ctx, &jsonrpc.Request{JSONRPC: "2.0", Method: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGuardMiddleware_MalformedJSONRPCDisabled(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{ValidateJSONRPC: false}, slog.Default(), nil)
	// Empty method should pass when validation is disabled.
	_, err := g.ProcessRequest(context.Background(), &jsonrpc.Request{Method: ""})
	if err != nil {
		t.Fatalf("unexpected error with validation disabled: %v", err)
	}
}

func TestGuardMiddleware_IPBlocklist(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{
		IPBlocklist: []string{"192.168.1.0/24", "10.0.0.5"},
	}, slog.Default(), nil)

	tests := []struct {
		ip      string
		blocked bool
	}{
		{"192.168.1.100", true},
		{"192.168.1.1", true},
		{"192.168.2.1", false},
		{"10.0.0.5", true},
		{"10.0.0.6", false},
	}
	for _, tt := range tests {
		err := g.CheckIPAccess(tt.ip)
		if tt.blocked && err == nil {
			t.Errorf("expected IP %s to be blocked", tt.ip)
		}
		if !tt.blocked && err != nil {
			t.Errorf("expected IP %s to be allowed, got: %v", tt.ip, err)
		}
	}
}

func TestGuardMiddleware_IPAllowlist(t *testing.T) {
	g := NewGuardMiddleware(GuardConfig{
		IPAllowlist: []string{"10.0.0.0/8"},
	}, slog.Default(), nil)

	if err := g.CheckIPAccess("10.1.2.3"); err != nil {
		t.Errorf("expected 10.1.2.3 allowed: %v", err)
	}
	if err := g.CheckIPAccess("192.168.1.1"); err == nil {
		t.Error("expected 192.168.1.1 to be rejected (not in allowlist)")
	}
}
