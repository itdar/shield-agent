package a2a

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"rua/internal/auth"
)

// agentIDHeader is the HTTP header carrying the agent identifier.
const agentIDHeader = "X-Agent-ID"

// signatureHeader is the HTTP header carrying the hex-encoded Ed25519 signature.
// The signature covers sha256(method + " " + path + "\n" + body).
const signatureHeader = "X-A2A-Signature"

// AuthMiddleware validates agent identity on incoming A2A requests.
//
// mode "closed": reject requests with invalid or missing signatures (HTTP 401).
// mode "open":   log failures but pass all requests through.
type AuthMiddleware struct {
	store  auth.KeyStore
	mode   string
	logger *slog.Logger
	onAuth func(string)
}

// NewAuthMiddleware creates an AuthMiddleware.
// onAuth is called with "verified", "failed", or "unsigned" for each request.
func NewAuthMiddleware(store auth.KeyStore, mode string, logger *slog.Logger, onAuth func(string)) *AuthMiddleware {
	return &AuthMiddleware{
		store:  store,
		mode:   mode,
		logger: logger,
		onAuth: onAuth,
	}
}

// WrapHandler returns an http.Handler that authenticates A2A requests.
func (a *AuthMiddleware) WrapHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentID := r.Header.Get(agentIDHeader)
		sigHex := r.Header.Get(signatureHeader)

		record := func(status string) {
			if a.onAuth != nil {
				a.onAuth(status)
			}
		}

		if agentID == "" || sigHex == "" {
			record("unsigned")
			a.logger.Warn("unsigned A2A request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
			)
			next.ServeHTTP(w, r)
			return
		}

		// Resolve public key — supports did:key: URIs and key store lookups.
		var pubKey ed25519.PublicKey
		var resolveErr error
		if strings.HasPrefix(agentID, "did:key:") {
			pubKey, resolveErr = auth.ResolveDIDKey(agentID)
		} else {
			pubKey, resolveErr = a.store.PublicKey(agentID)
		}

		if resolveErr != nil {
			record("failed")
			a.logger.Warn("unknown agent",
				slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
				slog.String("path", r.URL.Path),
				slog.String("error", resolveErr.Error()),
			)
			if a.mode == "closed" {
				http.Error(w, "unauthorized: unknown agent", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Read and restore body for signature verification.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			record("failed")
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		sigBytes, err := hex.DecodeString(sigHex)
		if err != nil {
			record("failed")
			a.logger.Warn("invalid signature encoding",
				slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
			)
			if a.mode == "closed" {
				http.Error(w, "unauthorized: invalid signature encoding", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		hash := hashRequest(r.Method, r.URL.Path, body)
		if !ed25519.Verify(pubKey, hash, sigBytes) {
			record("failed")
			a.logger.Warn("A2A signature verification failed",
				slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
				slog.String("path", r.URL.Path),
			)
			if a.mode == "closed" {
				http.Error(w, "unauthorized: signature verification failed", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		record("verified")
		a.logger.Info("A2A request verified",
			slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
			slog.String("path", r.URL.Path),
		)
		next.ServeHTTP(w, r)
	})
}

// hashRequest computes sha256(method SP path LF body) for signature verification.
func hashRequest(method, path string, body []byte) []byte {
	h := sha256.New()
	fmt.Fprintf(h, "%s %s\n", method, path)
	h.Write(body)
	return h.Sum(nil)
}
