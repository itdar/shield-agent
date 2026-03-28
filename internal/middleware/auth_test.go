package middleware

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/jsonrpc"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 10}))
}

// mapKeyStore is a simple in-memory KeyStore for testing.
type mapKeyStore struct {
	keys map[string]ed25519.PublicKey
}

func (m *mapKeyStore) PublicKey(agentID string) (ed25519.PublicKey, error) {
	k, ok := m.keys[agentID]
	if !ok {
		return nil, fmt.Errorf("agent %q not found", agentID)
	}
	return k, nil
}

func newTestAuthMiddleware(store auth.KeyStore, mode string) (*AuthMiddleware, *string) {
	var lastStatus string
	mw := NewAuthMiddleware(store, mode, discardLogger(), func(s string) {
		lastStatus = s
	}, nil)
	return mw, &lastStatus
}

func unsignedRequest(method string) *jsonrpc.Request {
	params := map[string]interface{}{"foo": "bar"}
	p, _ := json.Marshal(params)
	return &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      jsonrpc.NumberID(1),
		Method:  method,
		Params:  json.RawMessage(p),
	}
}

func TestAuthMiddlewareUnsigned(t *testing.T) {
	store := &mapKeyStore{keys: map[string]ed25519.PublicKey{}}

	t.Run("open", func(t *testing.T) {
		mw, status := newTestAuthMiddleware(store, "open")
		req := unsignedRequest("tools/list")

		out, err := mw.ProcessRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out == nil {
			t.Fatal("expected request passthrough, got nil")
		}
		if *status != "unsigned" {
			t.Errorf("status=%q, want \"unsigned\"", *status)
		}
	})

	// verified and closed modes reject unsigned requests.
	for _, mode := range []string{"verified", "closed"} {
		t.Run(mode, func(t *testing.T) {
			mw, status := newTestAuthMiddleware(store, mode)
			req := unsignedRequest("tools/list")

			_, err := mw.ProcessRequest(context.Background(), req)
			if err == nil {
				t.Fatalf("mode=%s: expected error for unsigned request", mode)
			}
			if *status != "unsigned" {
				t.Errorf("mode=%s: status=%q, want \"unsigned\"", mode, *status)
			}
		})
	}
}

func TestAuthMiddlewareValidSig(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentID := "agent-valid"
	store := &mapKeyStore{keys: map[string]ed25519.PublicKey{agentID: pub}}
	mw, status := newTestAuthMiddleware(store, "closed")

	req := buildSignedRequestCanonical("tools/call", agentID, priv)

	out, err := mw.ProcessRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("expected passthrough, got nil")
	}
	if *status != "verified" {
		t.Errorf("status=%q, want \"verified\"", *status)
	}
}

func TestAuthMiddlewareTamperedSig(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentID := "agent-tamper"
	store := &mapKeyStore{keys: map[string]ed25519.PublicKey{agentID: pub}}

	req := buildSignedRequestCanonical("tools/call", agentID, priv)
	req.Method = "tools/list" // tamper after signing

	t.Run("closed", func(t *testing.T) {
		mw, status := newTestAuthMiddleware(store, "closed")
		out, err := mw.ProcessRequest(context.Background(), req)
		if err == nil {
			t.Fatal("expected error for tampered request in closed mode, got nil")
		}
		if out != nil {
			t.Errorf("expected nil request on error")
		}
		if *status != "failed" {
			t.Errorf("status=%q, want \"failed\"", *status)
		}
	})

	t.Run("open", func(t *testing.T) {
		mw, status := newTestAuthMiddleware(store, "open")
		out, err := mw.ProcessRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("open mode: unexpected error: %v", err)
		}
		if out == nil {
			t.Fatal("open mode: expected passthrough, got nil")
		}
		if *status != "failed" {
			t.Errorf("status=%q, want \"failed\"", *status)
		}
	})
}

func TestAuthMiddlewareUnknownAgent(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := &mapKeyStore{keys: map[string]ed25519.PublicKey{}}
	agentID := "unknown-agent"

	req := buildSignedRequestCanonical("tools/call", agentID, priv)

	t.Run("closed", func(t *testing.T) {
		mw, _ := newTestAuthMiddleware(store, "closed")
		_, err := mw.ProcessRequest(context.Background(), req)
		if err == nil {
			t.Fatal("expected error for unknown agent in closed mode")
		}
	})

	t.Run("open", func(t *testing.T) {
		mw, status := newTestAuthMiddleware(store, "open")
		out, err := mw.ProcessRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("open mode: unexpected error: %v", err)
		}
		if out == nil {
			t.Fatal("open mode: expected passthrough, got nil")
		}
		if *status != "failed" {
			t.Errorf("status=%q, want \"failed\"", *status)
		}
	})
}

func TestAuthMiddlewareModeSwitch(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentID := "agent-mode"
	store := &mapKeyStore{keys: map[string]ed25519.PublicKey{agentID: pub}}

	req := buildSignedRequestCanonical("tools/call", agentID, priv)
	req.Method = "tools/list"

	t.Run("closed_rejects", func(t *testing.T) {
		mw, _ := newTestAuthMiddleware(store, "closed")
		_, err := mw.ProcessRequest(context.Background(), req)
		if err == nil {
			t.Fatal("closed mode should reject bad signature")
		}
	})

	t.Run("open_passes", func(t *testing.T) {
		mw, _ := newTestAuthMiddleware(store, "open")
		out, err := mw.ProcessRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("open mode should pass through, got error: %v", err)
		}
		if out == nil {
			t.Fatal("open mode: expected non-nil request")
		}
	})
}

// buildSignedRequestCanonical creates a properly signed *jsonrpc.Request.
// hashPayload strips _mcp_signature before hashing, so we:
//  1. Build params with only _mcp_agent_id (no _mcp_signature).
//  2. Compute hashPayload on that request — identical to what the verifier
//     will compute after stripping _mcp_signature from the final params.
//  3. Sign the hash, add _mcp_signature to params.
func buildSignedRequestCanonical(method, agentID string, priv ed25519.PrivateKey) *jsonrpc.Request {
	params := map[string]interface{}{
		"_mcp_agent_id": agentID,
	}
	paramsJSON, _ := json.Marshal(params)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      jsonrpc.NumberID(1),
		Method:  method,
		Params:  json.RawMessage(paramsJSON),
	}

	hash := hashPayload(req)

	sig := ed25519.Sign(priv, hash)
	params["_mcp_signature"] = hex.EncodeToString(sig)
	finalParams, _ := json.Marshal(params)
	req.Params = json.RawMessage(finalParams)
	return req
}
