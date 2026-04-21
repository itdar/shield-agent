package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// correlationKey tags the per-request correlation id stored in context.
type correlationKey struct{}

// NewCorrelationID returns a 16-byte hex id suitable for stitching
// ingress and egress records together. It is intentionally not a ULID
// (to avoid a new dependency) but has equivalent uniqueness properties
// for audit timeframes: 128 random bits = collision probability ~0 for
// any realistic log volume.
func NewCorrelationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// WithCorrelationID attaches the id to ctx. If the context already has
// one (e.g. the client forwarded X-Shield-Correlation-Id through an
// earlier hop), WithCorrelationID returns ctx unchanged.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	if existing := GetCorrelationID(ctx); existing != "" {
		return ctx
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

// GetCorrelationID returns the id previously stored on ctx, or "".
func GetCorrelationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, ok := ctx.Value(correlationKey{}).(string)
	if !ok {
		return ""
	}
	return v
}
