package middleware

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/jsonrpc"
)

// AuthMiddleware verifies Ed25519 signatures on incoming JSON-RPC requests.
type AuthMiddleware struct {
	PassthroughMiddleware
	store  auth.KeyStore
	mode   string
	logger *slog.Logger
	onAuth func(string)
}

// NewAuthMiddleware creates a new AuthMiddleware.
// mode should be "open" or "closed".
// onAuth is called with "verified", "failed", or "unsigned" for each request.
func NewAuthMiddleware(store auth.KeyStore, mode string, logger *slog.Logger, onAuth func(string)) *AuthMiddleware {
	return &AuthMiddleware{
		store:  store,
		mode:   mode,
		logger: logger,
		onAuth: onAuth,
	}
}

// Name returns the name of this middleware.
func (a *AuthMiddleware) Name() string { return "auth" }

// ProcessRequest verifies the request signature and enforces the auth policy.
func (a *AuthMiddleware) ProcessRequest(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Request, error) {
	agentID, sigHex := extractAuth(req)

	record := func(status string) {
		if a.onAuth != nil {
			a.onAuth(status)
		}
	}

	if agentID == "" || sigHex == "" {
		record("unsigned")
		a.logger.Warn("unsigned request", slog.String("method", req.Method))
		return req, nil
	}

	// Resolve public key.
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
			slog.String("method", req.Method),
			slog.String("error", resolveErr.Error()),
		)
		if a.mode == "closed" {
			return nil, fmt.Errorf("unknown agent: %w", resolveErr)
		}
		return req, nil
	}

	// Verify signature.
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		record("failed")
		a.logger.Warn("invalid signature encoding",
			slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
			slog.String("method", req.Method),
		)
		if a.mode == "closed" {
			return nil, errors.New("invalid signature encoding")
		}
		return req, nil
	}

	hash := hashPayload(req)
	if !ed25519.Verify(pubKey, hash, sigBytes) {
		record("failed")
		a.logger.Warn("signature verification failed",
			slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
			slog.String("method", req.Method),
		)
		if a.mode == "closed" {
			return nil, errors.New("signature verification failed")
		}
		return req, nil
	}

	record("verified")
	a.logger.Info("request verified",
		slog.String("agent_id_hash", auth.AgentIDHash(agentID)),
		slog.String("method", req.Method),
	)
	return req, nil
}

// signingPayload is the canonical structure hashed for signature verification.
type signingPayload struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// extractAuth extracts _mcp_agent_id and _mcp_signature from request params.
func extractAuth(req *jsonrpc.Request) (agentID, sigHex string) {
	if len(req.Params) == 0 {
		return "", ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(req.Params, &m); err != nil {
		return "", ""
	}
	if v, ok := m["_mcp_agent_id"]; ok {
		_ = json.Unmarshal(v, &agentID)
	}
	if v, ok := m["_mcp_signature"]; ok {
		_ = json.Unmarshal(v, &sigHex)
	}
	return agentID, sigHex
}

// hashPayload computes sha256 of the canonical JSON {method, params} for req.
// _mcp_signature is stripped from params before hashing so that the signer
// can compute the hash over the same payload without knowing the signature yet.
func hashPayload(req *jsonrpc.Request) []byte {
	params := req.Params
	if len(params) > 0 {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(params, &m); err == nil {
			delete(m, "_mcp_signature")
			if stripped, err := json.Marshal(m); err == nil {
				params = stripped
			}
		}
	}
	payload := signingPayload{
		Method: req.Method,
		Params: params,
	}
	b, _ := json.Marshal(payload)
	h := sha256.Sum256(b)
	return h[:]
}
