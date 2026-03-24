package middleware

import (
	"context"
	"log/slog"
	"testing"

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
