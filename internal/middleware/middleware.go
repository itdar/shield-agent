package middleware

import (
	"context"
	"encoding/json"

	"github.com/itdar/shield-agent/internal/jsonrpc"
)

// Middleware processes JSON-RPC requests and responses in a pipeline.
type Middleware interface {
	Name() string
	ProcessRequest(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Request, error)
	ProcessResponse(ctx context.Context, resp *jsonrpc.Response) (*jsonrpc.Response, error)
}

// Chain runs a list of middlewares in order.
type Chain struct {
	items []Middleware
}

// NewChain creates a new Chain from the provided middlewares.
func NewChain(items ...Middleware) *Chain {
	return &Chain{items: items}
}

// ProcessRequest runs each middleware's ProcessRequest in order.
// On the first error, it returns nil, an error payload (JSON-encoded error
// response + newline) suitable for writing to the upstream caller, and the
// original error.
func (c *Chain) ProcessRequest(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Request, []byte, error) {
	cur := req
	for _, m := range c.items {
		next, err := m.ProcessRequest(ctx, cur)
		if err != nil {
			resp := jsonrpc.ErrorResponse(cur.ID, jsonrpc.CodeAuthFailed, err.Error())
			payload, _ := json.Marshal(resp)
			payload = append(payload, '\n')
			return nil, payload, err
		}
		cur = next
	}
	return cur, nil, nil
}

// ProcessResponse runs each middleware's ProcessResponse in order.
// On the first error it returns nil and the error.
func (c *Chain) ProcessResponse(ctx context.Context, resp *jsonrpc.Response) (*jsonrpc.Response, error) {
	cur := resp
	for _, m := range c.items {
		next, err := m.ProcessResponse(ctx, cur)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

// PassthroughMiddleware is a no-op Middleware useful for embedding.
type PassthroughMiddleware struct{}

// Name returns the name of this middleware.
func (PassthroughMiddleware) Name() string { return "passthrough" }

// ProcessRequest passes the request through unchanged.
func (PassthroughMiddleware) ProcessRequest(_ context.Context, req *jsonrpc.Request) (*jsonrpc.Request, error) {
	return req, nil
}

// ProcessResponse passes the response through unchanged.
func (PassthroughMiddleware) ProcessResponse(_ context.Context, resp *jsonrpc.Response) (*jsonrpc.Response, error) {
	return resp, nil
}
