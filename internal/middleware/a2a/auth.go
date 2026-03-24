package a2a

import (
	"log/slog"
	"net/http"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/middleware/httpauth"
)

// AuthMiddleware validates agent identity on incoming A2A requests.
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
			SignatureHeader: "X-A2A-Signature",
		},
	}
}

// WrapHandler returns an http.Handler that authenticates A2A requests.
func (a *AuthMiddleware) WrapHandler(next http.Handler) http.Handler {
	return httpauth.WrapHandler(a.cfg, next)
}
