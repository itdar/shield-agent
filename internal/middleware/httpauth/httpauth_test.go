package httpauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"log/slog"
)

// stubKeyStore returns a fixed public key for any agent ID.
type stubKeyStore struct {
	pub ed25519.PublicKey
}

func (s *stubKeyStore) PublicKey(agentID string) (ed25519.PublicKey, error) {
	if s.pub == nil {
		return nil, fmt.Errorf("unknown agent: %s", agentID)
	}
	return s.pub, nil
}

func TestWrapHandler_UnsignedOpenMode(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	var lastStatus string
	handler := WrapHandler(Config{
		Store:           &stubKeyStore{},
		Mode:            "open",
		Logger:          slog.Default(),
		OnAuth:          func(s string) { lastStatus = s },
		AgentIDHeader:   "X-Agent-ID",
		SignatureHeader: "X-Agent-Signature",
	}, next)

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if lastStatus != "unsigned" {
		t.Fatalf("expected status 'unsigned', got %q", lastStatus)
	}
}

func TestWrapHandler_UnsignedClosedMode(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WrapHandler(Config{
		Store:           &stubKeyStore{},
		Mode:            "closed",
		Logger:          slog.Default(),
		AgentIDHeader:   "X-Agent-ID",
		SignatureHeader: "X-Agent-Signature",
	}, next)

	// In closed mode, unsigned requests still pass through (unsigned = no headers at all).
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for unsigned (no headers), got %d", w.Code)
	}
}

func TestWrapHandler_ValidSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	var lastStatus string
	handler := WrapHandler(Config{
		Store:           &stubKeyStore{pub: pub},
		Mode:            "closed",
		Logger:          slog.Default(),
		OnAuth:          func(s string) { lastStatus = s },
		AgentIDHeader:   "X-Agent-ID",
		SignatureHeader: "X-Agent-Signature",
	}, next)

	body := `{"jsonrpc":"2.0","method":"test","id":1}`
	hash := HashRequest(http.MethodPost, "/test", []byte(body))
	sig := ed25519.Sign(priv, hash)

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set("X-Agent-ID", "agent-1")
	req.Header.Set("X-Agent-Signature", hex.EncodeToString(sig))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if lastStatus != "verified" {
		t.Fatalf("expected status 'verified', got %q", lastStatus)
	}
}

func TestWrapHandler_InvalidSignature_Closed(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	var lastStatus string
	handler := WrapHandler(Config{
		Store:           &stubKeyStore{pub: pub},
		Mode:            "closed",
		Logger:          slog.Default(),
		OnAuth:          func(s string) { lastStatus = s },
		AgentIDHeader:   "X-Agent-ID",
		SignatureHeader: "X-Agent-Signature",
	}, next)

	body := `{"jsonrpc":"2.0","method":"test","id":1}`
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set("X-Agent-ID", "agent-1")
	req.Header.Set("X-Agent-Signature", hex.EncodeToString(make([]byte, 64))) // bad sig
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad sig in closed mode, got %d", w.Code)
	}
	if lastStatus != "failed" {
		t.Fatalf("expected status 'failed', got %q", lastStatus)
	}
}

func TestWrapHandler_UnknownAgent_Closed(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WrapHandler(Config{
		Store:           &stubKeyStore{pub: nil}, // returns error
		Mode:            "closed",
		Logger:          slog.Default(),
		AgentIDHeader:   "X-Agent-ID",
		SignatureHeader: "X-Agent-Signature",
	}, next)

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`))
	req.Header.Set("X-Agent-ID", "unknown")
	req.Header.Set("X-Agent-Signature", "deadbeef")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestWrapHandler_BodyPreserved(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)

	var receivedBody string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
	})
	handler := WrapHandler(Config{
		Store:           &stubKeyStore{pub: pub},
		Mode:            "open",
		Logger:          slog.Default(),
		AgentIDHeader:   "X-Agent-ID",
		SignatureHeader: "X-Agent-Signature",
	}, next)

	body := `{"test":"data"}`
	hash := HashRequest(http.MethodPost, "/api", []byte(body))
	sig := ed25519.Sign(priv, hash)

	req := httptest.NewRequest(http.MethodPost, "/api", strings.NewReader(body))
	req.Header.Set("X-Agent-ID", "agent-1")
	req.Header.Set("X-Agent-Signature", hex.EncodeToString(sig))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if receivedBody != body {
		t.Fatalf("body not preserved: got %q, want %q", receivedBody, body)
	}
}

func TestHashRequest(t *testing.T) {
	h := HashRequest("POST", "/test", []byte("body"))
	expected := sha256.Sum256([]byte("POST /test\nbody"))
	if hex.EncodeToString(h) != hex.EncodeToString(expected[:]) {
		t.Fatal("hash mismatch")
	}
}

func TestWrapHandler_InvalidSigEncoding_Closed(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	var lastStatus string
	handler := WrapHandler(Config{
		Store:           &stubKeyStore{pub: pub},
		Mode:            "closed",
		Logger:          slog.Default(),
		OnAuth:          func(s string) { lastStatus = s },
		AgentIDHeader:   "X-Agent-ID",
		SignatureHeader: "X-Agent-Signature",
	}, next)

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`))
	req.Header.Set("X-Agent-ID", "agent-1")
	req.Header.Set("X-Agent-Signature", "not-hex!")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad encoding in closed mode, got %d", w.Code)
	}
	if lastStatus != "failed" {
		t.Fatalf("expected status 'failed', got %q", lastStatus)
	}
}
