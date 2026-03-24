package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/middleware/httpauth"
)

// AuthMiddleware validates agent identity on intercepted HTTP API calls.
//
// Agents include X-Agent-ID and X-Agent-Signature headers so shield-agent can
// verify who is making the outbound API call before forwarding it upstream.
//
// mode "closed": reject requests with invalid or missing signatures (HTTP 401).
// mode "open":   log failures but pass all requests through.
type AuthMiddleware struct {
	cfg httpauth.Config
}

// NewAuthMiddleware creates an AuthMiddleware.
// onAuth is called with "verified", "failed", or "unsigned" for each request.
func NewAuthMiddleware(store auth.KeyStore, mode string, logger *slog.Logger, onAuth func(string)) *AuthMiddleware {
	return &AuthMiddleware{
		cfg: httpauth.Config{
			Store:          store,
			Mode:           mode,
			Logger:         logger,
			OnAuth:         onAuth,
			AgentIDHeader:  "X-Agent-ID",
			SignatureHeader: "X-Agent-Signature",
		},
	}
}

// WrapHandler returns an http.Handler that authenticates agent HTTP API calls.
func (a *AuthMiddleware) WrapHandler(next http.Handler) http.Handler {
	return httpauth.WrapHandler(a.cfg, next)
}
