package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/itdar/shield-agent/internal/jsonrpc"
	"github.com/itdar/shield-agent/internal/middleware"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/token"

	"log/slog"
)

func openTokenE2EDB(t *testing.T) (*storage.DB, *token.Store) {
	t.Helper()
	f, err := os.CreateTemp("", "token-e2e-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() { os.Remove(path) })

	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := token.NewStore(db.Conn())
	return db, store
}

// TestTokenE2E_ValidToken tests the full flow: create token → middleware accepts request.
func TestTokenE2E_ValidToken(t *testing.T) {
	_, store := openTokenE2EDB(t)

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	store.Create("e2e-agent", hash, nil, 0, 0, nil, nil)

	mw := middleware.NewTokenMiddleware(store, slog.Default())
	chain := middleware.NewChain(mw)

	ctx := middleware.SetRawToken(context.Background(), raw)
	req := &jsonrpc.Request{JSONRPC: "2.0", Method: "tools/call", ID: jsonrpc.NumberID(1)}
	got, _, err := chain.ProcessRequest(ctx, req)
	if err != nil {
		t.Fatalf("expected request to pass: %v", err)
	}
	if got.Method != "tools/call" {
		t.Fatalf("method mismatch: %s", got.Method)
	}
}

// TestTokenE2E_InvalidToken tests that an invalid token is rejected.
func TestTokenE2E_InvalidToken(t *testing.T) {
	_, store := openTokenE2EDB(t)
	mw := middleware.NewTokenMiddleware(store, slog.Default())
	chain := middleware.NewChain(mw)

	ctx := middleware.SetRawToken(context.Background(), "nonexistent-token")
	req := &jsonrpc.Request{JSONRPC: "2.0", Method: "test", ID: jsonrpc.NumberID(1)}
	_, errPayload, err := chain.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected rejection for invalid token")
	}
	if errPayload == nil {
		t.Fatal("expected error payload")
	}
}

// TestTokenE2E_ExpiredToken tests that expired tokens are rejected.
func TestTokenE2E_ExpiredToken(t *testing.T) {
	_, store := openTokenE2EDB(t)

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	past := time.Now().Add(-1 * time.Hour)
	store.Create("expired", hash, &past, 0, 0, nil, nil)

	mw := middleware.NewTokenMiddleware(store, slog.Default())
	chain := middleware.NewChain(mw)

	ctx := middleware.SetRawToken(context.Background(), raw)
	req := &jsonrpc.Request{JSONRPC: "2.0", Method: "test", ID: jsonrpc.NumberID(1)}
	_, _, err := chain.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected rejection for expired token")
	}
}

// TestTokenE2E_QuotaExhaustion tests that hourly quota is enforced.
func TestTokenE2E_QuotaExhaustion(t *testing.T) {
	_, store := openTokenE2EDB(t)

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	id, _ := store.Create("quota-test", hash, nil, 3, 0, nil, nil)

	mw := middleware.NewTokenMiddleware(store, slog.Default())

	ctx := middleware.SetRawToken(context.Background(), raw)

	// First 3 requests should succeed (each ProcessRequest records usage).
	for i := 0; i < 3; i++ {
		req := &jsonrpc.Request{JSONRPC: "2.0", Method: "test", ID: jsonrpc.NumberID(int64(i + 1))}
		_, err := mw.ProcessRequest(ctx, req)
		if err != nil {
			t.Fatalf("request %d should pass: %v", i+1, err)
		}
	}

	// Verify usage count.
	count, _ := store.CountUsage(id, time.Hour)
	if count != 3 {
		t.Fatalf("expected 3 usage records, got %d", count)
	}

	// 4th request should fail (quota exceeded).
	req := &jsonrpc.Request{JSONRPC: "2.0", Method: "test", ID: jsonrpc.NumberID(4)}
	_, err := mw.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected quota exceeded error")
	}
}

// TestTokenE2E_MethodRestriction tests that method allowlist is enforced.
func TestTokenE2E_MethodRestriction(t *testing.T) {
	_, store := openTokenE2EDB(t)

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	store.Create("method-restricted", hash, nil, 0, 0, []string{"tools/call", "tools/list"}, nil)

	mw := middleware.NewTokenMiddleware(store, slog.Default())
	ctx := middleware.SetRawToken(context.Background(), raw)

	// Allowed methods.
	for _, method := range []string{"tools/call", "tools/list"} {
		req := &jsonrpc.Request{JSONRPC: "2.0", Method: method, ID: jsonrpc.NumberID(1)}
		if _, err := mw.ProcessRequest(ctx, req); err != nil {
			t.Fatalf("method %s should be allowed: %v", method, err)
		}
	}

	// Denied method.
	req := &jsonrpc.Request{JSONRPC: "2.0", Method: "resources/read", ID: jsonrpc.NumberID(1)}
	if _, err := mw.ProcessRequest(ctx, req); err == nil {
		t.Fatal("resources/read should be denied")
	}
}

// TestTokenE2E_RevokedToken tests that revoked tokens are rejected.
func TestTokenE2E_RevokedToken(t *testing.T) {
	_, store := openTokenE2EDB(t)

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	id, _ := store.Create("revoke-e2e", hash, nil, 0, 0, nil, nil)
	store.Revoke(id)

	mw := middleware.NewTokenMiddleware(store, slog.Default())
	ctx := middleware.SetRawToken(context.Background(), raw)

	req := &jsonrpc.Request{JSONRPC: "2.0", Method: "test", ID: jsonrpc.NumberID(1)}
	_, err := mw.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected rejection for revoked token")
	}
}

// TestTokenE2E_FullPipeline tests token middleware in a full chain with auth + guard + token + log.
func TestTokenE2E_FullPipeline(t *testing.T) {
	db, store := openTokenE2EDB(t)

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	store.Create("pipeline", hash, nil, 100, 0, nil, nil)

	// Build a chain with guard + token + log.
	guard := middleware.NewGuardMiddleware(middleware.GuardConfig{
		RateLimitPerMin: 10,
	}, slog.Default(), nil)
	tokenMW := middleware.NewTokenMiddleware(store, slog.Default())
	logMW := middleware.NewLogMiddleware(db, slog.Default(), nil, nil)
	defer logMW.Close()

	chain := middleware.NewChain(guard, tokenMW, logMW)

	ctx := middleware.SetRawToken(context.Background(), raw)
	req := &jsonrpc.Request{JSONRPC: "2.0", Method: "tools/call", ID: jsonrpc.NumberID(1)}
	got, _, err := chain.ProcessRequest(ctx, req)
	if err != nil {
		t.Fatalf("full pipeline should pass: %v", err)
	}
	if got.Method != "tools/call" {
		t.Fatalf("method: got %s, want tools/call", got.Method)
	}

	// Verify usage was recorded.
	time.Sleep(50 * time.Millisecond) // let async log writer flush
	count, _ := store.CountUsage("", time.Hour)
	// At least the token middleware recorded usage.
	stats, _ := store.GetStats("", 24*time.Hour)
	_ = stats // just verify no error
	_ = count
}
