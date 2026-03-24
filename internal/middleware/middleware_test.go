package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/itdar/shield-agent/internal/jsonrpc"
)

func TestNewChainPassthrough(t *testing.T) {
	chain := NewChain()
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      jsonrpc.NumberID(1),
		Method:  "tools/list",
	}
	ctx := context.Background()

	got, payload, err := chain.ProcessRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload != nil {
		t.Errorf("expected nil payload, got %q", payload)
	}
	if got != req {
		t.Error("expected same request pointer")
	}
}

func TestNewChainPassthroughResponse(t *testing.T) {
	chain := NewChain()
	resp := &jsonrpc.Response{
		JSONRPC: jsonrpc.Version,
		ID:      jsonrpc.NumberID(1),
	}
	ctx := context.Background()

	got, err := chain.ProcessResponse(ctx, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != resp {
		t.Error("expected same response pointer")
	}
}

// blockingMiddleware rejects all requests with a fixed error.
type blockingMiddleware struct {
	PassthroughMiddleware
	msg string
}

func (b *blockingMiddleware) ProcessRequest(_ context.Context, req *jsonrpc.Request) (*jsonrpc.Request, error) {
	return nil, errors.New(b.msg)
}

func TestChainBlockingMiddleware(t *testing.T) {
	bm := &blockingMiddleware{msg: "auth denied"}
	chain := NewChain(bm)
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      jsonrpc.NumberID(1),
		Method:  "tools/call",
	}
	ctx := context.Background()

	got, payload, err := chain.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected error from blocking middleware")
	}
	if got != nil {
		t.Error("expected nil request on error")
	}
	if len(payload) == 0 {
		t.Error("expected non-empty error payload")
	}
}

func TestChainOrdering(t *testing.T) {
	var order []string

	type tracingMW struct {
		PassthroughMiddleware
		name string
	}
	newTracing := func(name string) Middleware {
		return &struct {
			PassthroughMiddleware
			name string
		}{name: name}
	}
	_ = newTracing // unused; use inline impl below

	// We'll use a custom impl to capture ordering.
	type captureMW struct {
		PassthroughMiddleware
		label string
	}
	var middlewares []Middleware
	for _, label := range []string{"A", "B", "C"} {
		l := label // capture
		mw := &struct {
			PassthroughMiddleware
			label string
		}{label: l}
		_ = mw
		// Use a real implementation.
		middlewares = append(middlewares, &orderTracker{label: l, order: &order})
	}

	chain := NewChain(middlewares...)
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "ping",
	}
	ctx := context.Background()

	if _, _, err := chain.ProcessRequest(ctx, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(order) != 3 || order[0] != "A" || order[1] != "B" || order[2] != "C" {
		t.Errorf("unexpected ordering: %v", order)
	}
}

// orderTracker records the order in which middlewares are called.
type orderTracker struct {
	PassthroughMiddleware
	label string
	order *[]string
}

func (o *orderTracker) ProcessRequest(_ context.Context, req *jsonrpc.Request) (*jsonrpc.Request, error) {
	*o.order = append(*o.order, o.label)
	return req, nil
}
