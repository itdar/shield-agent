package middleware

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/itdar/shield-agent/internal/jsonrpc"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/token"
)

func openTokenTestDB(t *testing.T) *storage.DB {
	t.Helper()
	f, err := os.CreateTemp("", "token-mw-*.db")
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
	return db
}

func TestTokenMiddleware_Name(t *testing.T) {
	tm := NewTokenMiddleware(nil, slog.Default())
	if tm.Name() != "token" {
		t.Fatalf("expected 'token', got %q", tm.Name())
	}
}

func TestTokenMiddleware_NoToken(t *testing.T) {
	db := openTokenTestDB(t)
	store := token.NewStore(db.Conn())
	tm := NewTokenMiddleware(store, slog.Default())

	req := &jsonrpc.Request{Method: "test"}
	// No raw token in context — should pass through.
	got, err := tm.ProcessRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != req {
		t.Fatal("expected same request")
	}
}

func TestTokenMiddleware_ValidToken(t *testing.T) {
	db := openTokenTestDB(t)
	store := token.NewStore(db.Conn())
	tm := NewTokenMiddleware(store, slog.Default())

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	store.Create("test", hash, nil, 0, 0, nil, nil)

	ctx := SetRawToken(context.Background(), raw)
	req := &jsonrpc.Request{Method: "tools/call"}
	got, err := tm.ProcessRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != req {
		t.Fatal("expected same request")
	}
}

func TestTokenMiddleware_InvalidToken(t *testing.T) {
	db := openTokenTestDB(t)
	store := token.NewStore(db.Conn())
	tm := NewTokenMiddleware(store, slog.Default())

	ctx := SetRawToken(context.Background(), "bad-token")
	req := &jsonrpc.Request{Method: "test"}
	_, err := tm.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestTokenMiddleware_RevokedToken(t *testing.T) {
	db := openTokenTestDB(t)
	store := token.NewStore(db.Conn())
	tm := NewTokenMiddleware(store, slog.Default())

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	id, _ := store.Create("revoked", hash, nil, 0, 0, nil, nil)
	store.Revoke(id)

	ctx := SetRawToken(context.Background(), raw)
	req := &jsonrpc.Request{Method: "test"}
	_, err := tm.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected error for revoked token")
	}
}

func TestTokenMiddleware_ExpiredToken(t *testing.T) {
	db := openTokenTestDB(t)
	store := token.NewStore(db.Conn())
	tm := NewTokenMiddleware(store, slog.Default())

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	past := time.Now().Add(-1 * time.Hour)
	store.Create("expired", hash, &past, 0, 0, nil, nil)

	ctx := SetRawToken(context.Background(), raw)
	req := &jsonrpc.Request{Method: "test"}
	_, err := tm.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestTokenMiddleware_MethodRestriction(t *testing.T) {
	db := openTokenTestDB(t)
	store := token.NewStore(db.Conn())
	tm := NewTokenMiddleware(store, slog.Default())

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	store.Create("restricted", hash, nil, 0, 0, []string{"tools/call"}, nil)

	ctx := SetRawToken(context.Background(), raw)

	// Allowed method.
	req := &jsonrpc.Request{Method: "tools/call"}
	if _, err := tm.ProcessRequest(ctx, req); err != nil {
		t.Fatalf("expected allowed: %v", err)
	}

	// Denied method.
	req2 := &jsonrpc.Request{Method: "resources/list"}
	if _, err := tm.ProcessRequest(ctx, req2); err == nil {
		t.Fatal("expected error for disallowed method")
	}
}

func TestTokenMiddleware_HourlyQuotaExceeded(t *testing.T) {
	db := openTokenTestDB(t)
	store := token.NewStore(db.Conn())
	tm := NewTokenMiddleware(store, slog.Default())

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	id, _ := store.Create("quota", hash, nil, 3, 0, nil, nil) // 3/hour

	// Manually record 3 usages.
	for i := 0; i < 3; i++ {
		store.RecordUsage(id, "test", true, 1.0)
	}

	ctx := SetRawToken(context.Background(), raw)
	req := &jsonrpc.Request{Method: "test"}
	_, err := tm.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected hourly quota exceeded error")
	}
}

func TestTokenMiddleware_MonthlyQuotaExceeded(t *testing.T) {
	db := openTokenTestDB(t)
	store := token.NewStore(db.Conn())
	tm := NewTokenMiddleware(store, slog.Default())

	raw, _ := token.GenerateToken()
	hash := token.HashToken(raw)
	id, _ := store.Create("monthly", hash, nil, 0, 2, nil, nil) // 2/month

	store.RecordUsage(id, "test", true, 1.0)
	store.RecordUsage(id, "test", true, 1.0)

	ctx := SetRawToken(context.Background(), raw)
	req := &jsonrpc.Request{Method: "test"}
	_, err := tm.ProcessRequest(ctx, req)
	if err == nil {
		t.Fatal("expected monthly quota exceeded error")
	}
}

func TestTokenMiddleware_ContextRoundtrip(t *testing.T) {
	ctx := context.Background()
	ctx = SetRawToken(ctx, "my-token")
	if getRawToken(ctx) != "my-token" {
		t.Fatal("raw token context roundtrip failed")
	}

	tok := &token.Token{ID: "t1", Name: "test"}
	ctx = SetToken(ctx, tok)
	got := GetToken(ctx)
	if got == nil || got.ID != "t1" {
		t.Fatal("token context roundtrip failed")
	}
}
