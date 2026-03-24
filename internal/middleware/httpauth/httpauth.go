// Package httpauth provides shared HTTP-based agent authentication logic
// used by both A2A and HTTP API middleware.
package httpauth

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

	"github.com/itdar/shield-agent/internal/auth"
)

// Config holds configuration for HTTP-based agent authentication.
type Config struct {
	Store           auth.KeyStore
	Mode            string // "open" or "closed"
	Logger          *slog.Logger
	OnAuth          func(string) // called with "verified", "failed", or "unsigned"
	AgentIDHeader   string       // e.g. "X-Agent-ID"
	SignatureHeader  string       // e.g. "X-A2A-Signature" or "X-Agent-Signature"
}

// WrapHandler returns an http.Handler that authenticates requests.
func WrapHandler(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentID := r.Header.Get(cfg.AgentIDHeader)
		sigHex := r.Header.Get(cfg.SignatureHeader)

		record := func(status string) {
			if cfg.OnAuth != nil {
				cfg.OnAuth(status)
			}
		}

		if agentID == "" || sigHex == "" {
			record("unsigned")
			cfg.Logger.Warn("unsigned request",
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
			pubKey, resolveErr = cfg.Store.PublicKey(agentID)
		}

		if resolveErr != nil {
			record("failed")
			cfg.Logger.Warn("unknown agent",
				slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
				slog.String("path", r.URL.Path),
				slog.String("error", resolveErr.Error()),
			)
			if cfg.Mode == "closed" {
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
			cfg.Logger.Warn("invalid signature encoding",
				slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
			)
			if cfg.Mode == "closed" {
				http.Error(w, "unauthorized: invalid signature encoding", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		hash := HashRequest(r.Method, r.URL.Path, body)
		if !ed25519.Verify(pubKey, hash, sigBytes) {
			record("failed")
			cfg.Logger.Warn("signature verification failed",
				slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
				slog.String("path", r.URL.Path),
			)
			if cfg.Mode == "closed" {
				http.Error(w, "unauthorized: signature verification failed", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		record("verified")
		cfg.Logger.Info("request verified",
			slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
			slog.String("path", r.URL.Path),
		)
		next.ServeHTTP(w, r)
	})
}

// HashRequest computes sha256(method SP path LF body) for signature verification.
func HashRequest(method, path string, body []byte) []byte {
	h := sha256.New()
	fmt.Fprintf(h, "%s %s\n", method, path)
	h.Write(body)
	return h.Sum(nil)
}
