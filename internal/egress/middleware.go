// Package egress implements the forward-proxy egress mode of shield-agent.
//
// Unlike the ingress middleware pipeline (which is JSON-RPC-shaped),
// egress intercepts raw HTTP requests, so it needs its own middleware
// interface and chain plumbing. The two systems intentionally do not
// share types — the ingress Middleware takes *jsonrpc.Request and
// EgressMiddleware takes *egress.Request.
package egress

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Phase reports which phase a middleware belongs to. Phase 1 middlewares
// run on all deployments; Phase 2 middlewares only engage when TLS MITM
// is enabled for the target host.
type Phase int

const (
	Phase1 Phase = 1
	Phase2 Phase = 2
)

// Request captures everything an egress middleware needs to know about
// an outbound call. In Phase 1 the body fields are empty because the
// connection is tunneled without decryption.
type Request struct {
	// Destination is the host:port the client is connecting to.
	Destination string
	// Host is Destination without the port.
	Host string
	// Port is the numeric port parsed from Destination.
	Port int
	// Method is the inbound HTTP method for plaintext requests,
	// or "CONNECT" for tunneled HTTPS.
	Method string
	// Protocol is "http" for plaintext, "https" for CONNECT tunnels,
	// and may be elevated to "mcp" / "a2a" in Phase 2 after detection.
	Protocol string
	// Provider is the AI provider detected from Host (Phase 1 level).
	Provider string
	// StartedAt is set when the proxy begins handling the request.
	StartedAt time.Time
	// ClientRequest is the intercepted *http.Request. It is nil for
	// CONNECT tunnels in Phase 1 because the body is encrypted.
	ClientRequest *http.Request
	// Body is the request body for Phase 2 MITM'd requests. Always nil
	// in Phase 1.
	Body []byte
}

// Response is populated after the upstream exchange completes.
type Response struct {
	// StatusCode is 0 for CONNECT tunnels (metadata-only).
	StatusCode int
	// RequestSize is TCP bytes sent in Phase 1, HTTP body bytes in Phase 2.
	RequestSize int64
	// ResponseSize is TCP bytes received in Phase 1, HTTP body bytes in Phase 2.
	ResponseSize int64
	// LatencyMs is wall-clock time between ProcessRequest and ProcessResponse.
	LatencyMs float64
	// ErrorDetail captures any transport-level error ("" on success).
	ErrorDetail string
	// Headers (Phase 2 MITM only).
	Headers http.Header
	// Body (Phase 2 MITM only).
	Body []byte
}

// EgressMiddleware processes intercepted outbound traffic.
//
// Middlewares form a pipeline: ProcessRequest runs in order before the
// upstream exchange, ProcessResponse runs in reverse order after it.
// A middleware that returns a non-nil error aborts the pipeline; the proxy
// is then expected to reject the outbound request (policy block path).
type EgressMiddleware interface {
	Name() string
	ProcessRequest(ctx context.Context, req *Request) (*Request, error)
	ProcessResponse(ctx context.Context, req *Request, resp *Response) (*Response, error)
}

// EgressChain runs middlewares in sequence.
type EgressChain struct {
	items []EgressMiddleware
}

// NewEgressChain creates a chain from the provided middlewares.
func NewEgressChain(items ...EgressMiddleware) *EgressChain {
	return &EgressChain{items: items}
}

// ProcessRequest walks the chain in order. On error it returns the
// middleware that produced the error along with the error, so the caller
// can translate it into the right transport-level response.
func (c *EgressChain) ProcessRequest(ctx context.Context, req *Request) (*Request, EgressMiddleware, error) {
	cur := req
	for _, m := range c.items {
		next, err := m.ProcessRequest(ctx, cur)
		if err != nil {
			return nil, m, err
		}
		cur = next
	}
	return cur, nil, nil
}

// ProcessResponse walks the chain in reverse order so symmetrical
// middlewares (e.g. content transformers) see request→response pairs
// in LIFO order.
func (c *EgressChain) ProcessResponse(ctx context.Context, req *Request, resp *Response) (*Response, error) {
	cur := resp
	for i := len(c.items) - 1; i >= 0; i-- {
		next, err := c.items[i].ProcessResponse(ctx, req, cur)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

// Items returns the middlewares in pipeline order (for diagnostics and
// SIGHUP-time swapping). The returned slice is a copy.
func (c *EgressChain) Items() []EgressMiddleware {
	out := make([]EgressMiddleware, len(c.items))
	copy(out, c.items)
	return out
}

// SwappableEgressChain wraps an EgressChain and allows atomic replacement
// for SIGHUP-driven config reload. Reads are lock-free in the hot path.
type SwappableEgressChain struct {
	mu    sync.RWMutex
	chain *EgressChain
}

// NewSwappableEgressChain creates a swappable wrapper.
func NewSwappableEgressChain(chain *EgressChain) *SwappableEgressChain {
	return &SwappableEgressChain{chain: chain}
}

// Swap atomically replaces the current chain. Concurrent requests mid-flight
// are not interrupted — they complete against the old chain.
func (sc *SwappableEgressChain) Swap(chain *EgressChain) {
	sc.mu.Lock()
	sc.chain = chain
	sc.mu.Unlock()
}

// ProcessRequest forwards to the current chain.
func (sc *SwappableEgressChain) ProcessRequest(ctx context.Context, req *Request) (*Request, EgressMiddleware, error) {
	sc.mu.RLock()
	c := sc.chain
	sc.mu.RUnlock()
	return c.ProcessRequest(ctx, req)
}

// ProcessResponse forwards to the current chain.
func (sc *SwappableEgressChain) ProcessResponse(ctx context.Context, req *Request, resp *Response) (*Response, error) {
	sc.mu.RLock()
	c := sc.chain
	sc.mu.RUnlock()
	return c.ProcessResponse(ctx, req, resp)
}

// Current returns the chain currently installed. The caller must not mutate it.
func (sc *SwappableEgressChain) Current() *EgressChain {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.chain
}

// PassthroughEgressMiddleware is a no-op middleware useful for embedding
// and tests.
type PassthroughEgressMiddleware struct{ MiddlewareName string }

// Name returns the configured name (default "passthrough").
func (p PassthroughEgressMiddleware) Name() string {
	if p.MiddlewareName == "" {
		return "passthrough"
	}
	return p.MiddlewareName
}

// ProcessRequest passes the request through unchanged.
func (PassthroughEgressMiddleware) ProcessRequest(_ context.Context, req *Request) (*Request, error) {
	return req, nil
}

// ProcessResponse passes the response through unchanged.
func (PassthroughEgressMiddleware) ProcessResponse(_ context.Context, _ *Request, resp *Response) (*Response, error) {
	return resp, nil
}
