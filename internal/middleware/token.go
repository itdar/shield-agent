package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/itdar/shield-agent/internal/jsonrpc"
	"github.com/itdar/shield-agent/internal/token"
)

// TokenMiddleware validates bearer tokens and enforces quotas.
type TokenMiddleware struct {
	PassthroughMiddleware
	store  *token.Store
	logger *slog.Logger
}

// NewTokenMiddleware creates a TokenMiddleware.
func NewTokenMiddleware(store *token.Store, logger *slog.Logger) *TokenMiddleware {
	return &TokenMiddleware{
		store:  store,
		logger: logger,
	}
}

// Name returns the middleware name.
func (tm *TokenMiddleware) Name() string { return "token" }

// tokenKey is a context key for storing the validated token.
type tokenContextKey struct{}

// SetToken stores a validated token in the request context.
func SetToken(ctx context.Context, t *token.Token) context.Context {
	return context.WithValue(ctx, tokenContextKey{}, t)
}

// GetToken retrieves the validated token from the context.
func GetToken(ctx context.Context) *token.Token {
	t, _ := ctx.Value(tokenContextKey{}).(*token.Token)
	return t
}

// rawTokenKey is a context key for the raw token string extracted from headers.
type rawTokenKey struct{}

// SetRawToken stores the raw bearer token string in the context.
// This should be called by the transport layer after extracting it from HTTP headers.
func SetRawToken(ctx context.Context, raw string) context.Context {
	return context.WithValue(ctx, rawTokenKey{}, raw)
}

// getRawToken retrieves the raw token from the context.
func getRawToken(ctx context.Context) string {
	s, _ := ctx.Value(rawTokenKey{}).(string)
	return s
}

// ProcessRequest validates the token and checks quotas/permissions.
func (tm *TokenMiddleware) ProcessRequest(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Request, error) {
	raw := getRawToken(ctx)
	if raw == "" {
		// No token provided — pass through (auth middleware handles identity).
		return req, nil
	}

	hash := token.HashToken(raw)
	tok, err := tm.store.GetByHash(hash)
	if err != nil {
		tm.logger.Error("token lookup failed", slog.String("error", err.Error()))
		return nil, fmt.Errorf("internal token error")
	}

	if tok == nil {
		tm.logger.Warn("invalid token provided")
		return nil, fmt.Errorf("invalid token")
	}

	if !tok.Active {
		tm.logger.Warn("revoked token used", slog.String("token_id", tok.ID))
		return nil, fmt.Errorf("token has been revoked")
	}

	if tok.IsExpired() {
		tm.logger.Warn("expired token used", slog.String("token_id", tok.ID))
		return nil, fmt.Errorf("token has expired")
	}

	// Check method restriction.
	if !tok.IsMethodAllowed(req.Method) {
		tm.logger.Warn("method not allowed for token",
			slog.String("token_id", tok.ID),
			slog.String("method", req.Method),
		)
		return nil, fmt.Errorf("method %q not allowed for this token", req.Method)
	}

	// Check hourly quota.
	if tok.QuotaHourly > 0 {
		count, err := tm.store.CountUsage(tok.ID, time.Hour)
		if err != nil {
			tm.logger.Error("quota check failed", slog.String("error", err.Error()))
			return nil, fmt.Errorf("internal token error")
		}
		if count >= tok.QuotaHourly {
			tm.logger.Warn("hourly quota exceeded",
				slog.String("token_id", tok.ID),
				slog.Int("usage", count),
				slog.Int("limit", tok.QuotaHourly),
			)
			return nil, fmt.Errorf("hourly quota exceeded (%d/%d)", count, tok.QuotaHourly)
		}
	}

	// Check monthly quota.
	if tok.QuotaMonthly > 0 {
		count, err := tm.store.CountUsage(tok.ID, 30*24*time.Hour)
		if err != nil {
			tm.logger.Error("quota check failed", slog.String("error", err.Error()))
			return nil, fmt.Errorf("internal token error")
		}
		if count >= tok.QuotaMonthly {
			tm.logger.Warn("monthly quota exceeded",
				slog.String("token_id", tok.ID),
				slog.Int("usage", count),
				slog.Int("limit", tok.QuotaMonthly),
			)
			return nil, fmt.Errorf("monthly quota exceeded (%d/%d)", count, tok.QuotaMonthly)
		}
	}

	// Record usage.
	if err := tm.store.RecordUsage(tok.ID, req.Method, true, 0); err != nil {
		tm.logger.Error("usage recording failed", slog.String("error", err.Error()))
	}

	tm.logger.Debug("token validated", slog.String("token_id", tok.ID), slog.String("method", req.Method))
	return req, nil
}
