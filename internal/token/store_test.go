package token

import (
	"os"
	"testing"
	"time"

	"github.com/itdar/shield-agent/internal/storage"
)

func openTestDB(t *testing.T) *storage.DB {
	t.Helper()
	f, err := os.CreateTemp("", "token-test-*.db")
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

func TestGenerateAndHashToken(t *testing.T) {
	raw, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 64 { // 32 bytes = 64 hex chars
		t.Fatalf("expected 64 hex chars, got %d", len(raw))
	}

	hash := HashToken(raw)
	if len(hash) != 64 {
		t.Fatalf("expected 64 hex hash, got %d", len(hash))
	}
	if hash == raw {
		t.Fatal("hash should differ from raw token")
	}

	// Same input gives same hash.
	if HashToken(raw) != hash {
		t.Fatal("hash not deterministic")
	}
}

func TestCreateAndGetByHash(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	raw, _ := GenerateToken()
	hash := HashToken(raw)

	id, err := store.Create("test-agent", hash, nil, 100, 10000, []string{"tools/call"}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	tok, err := store.GetByHash(hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if tok == nil {
		t.Fatal("expected token, got nil")
	}
	if tok.Name != "test-agent" {
		t.Errorf("Name: got %q, want %q", tok.Name, "test-agent")
	}
	if tok.QuotaHourly != 100 {
		t.Errorf("QuotaHourly: got %d, want 100", tok.QuotaHourly)
	}
	if len(tok.AllowedMethods) != 1 || tok.AllowedMethods[0] != "tools/call" {
		t.Errorf("AllowedMethods: got %v", tok.AllowedMethods)
	}
	if !tok.Active {
		t.Error("expected token to be active")
	}
}

func TestGetByID(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	hash := HashToken("test")
	id, err := store.Create("by-id", hash, nil, 0, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	tok, err := store.GetByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if tok == nil || tok.Name != "by-id" {
		t.Fatalf("unexpected token: %+v", tok)
	}
}

func TestGetByHash_NotFound(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	tok, err := store.GetByHash("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if tok != nil {
		t.Fatal("expected nil for nonexistent hash")
	}
}

func TestList(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	store.Create("a", HashToken("a"), nil, 0, 0, nil, nil)
	store.Create("b", HashToken("b"), nil, 0, 0, nil, nil)

	tokens, err := store.List(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
}

func TestRevoke(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	hash := HashToken("revoke-me")
	id, _ := store.Create("revoke-test", hash, nil, 0, 0, nil, nil)

	if err := store.Revoke(id); err != nil {
		t.Fatal(err)
	}

	tok, _ := store.GetByID(id)
	if tok.Active {
		t.Error("expected token to be revoked (inactive)")
	}

	// List active only should exclude revoked token.
	active, _ := store.List(true)
	for _, a := range active {
		if a.ID == id {
			t.Error("revoked token should not appear in active list")
		}
	}
}

func TestRevokeNotFound(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	if err := store.Revoke("nonexistent"); err == nil {
		t.Error("expected error for nonexistent token")
	}
}

func TestDelete(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	hash := HashToken("delete-me")
	id, _ := store.Create("delete-test", hash, nil, 0, 0, nil, nil)
	store.RecordUsage(id, "test", true, 1.0)

	if err := store.Delete(id); err != nil {
		t.Fatal(err)
	}

	tok, _ := store.GetByID(id)
	if tok != nil {
		t.Error("expected nil after delete")
	}
}

func TestTokenExpiry(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)

	expired := &Token{ExpiresAt: &past}
	if !expired.IsExpired() {
		t.Error("expected expired")
	}

	valid := &Token{ExpiresAt: &future}
	if valid.IsExpired() {
		t.Error("expected not expired")
	}

	noExpiry := &Token{ExpiresAt: nil}
	if noExpiry.IsExpired() {
		t.Error("nil expiry should never be expired")
	}
}

func TestIsMethodAllowed(t *testing.T) {
	all := &Token{AllowedMethods: []string{}}
	if !all.IsMethodAllowed("anything") {
		t.Error("empty list should allow all")
	}

	restricted := &Token{AllowedMethods: []string{"tools/call", "tools/list"}}
	if !restricted.IsMethodAllowed("tools/call") {
		t.Error("should allow tools/call")
	}
	if restricted.IsMethodAllowed("resources/read") {
		t.Error("should deny resources/read")
	}
}

func TestRecordAndCountUsage(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	hash := HashToken("usage")
	id, _ := store.Create("usage-test", hash, nil, 0, 0, nil, nil)

	for i := 0; i < 5; i++ {
		if err := store.RecordUsage(id, "test", true, 10.0); err != nil {
			t.Fatal(err)
		}
	}

	count, err := store.CountUsage(id, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("expected 5, got %d", count)
	}
}

func TestGetStats(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	hash := HashToken("stats")
	id, _ := store.Create("stats-test", hash, nil, 0, 0, nil, nil)

	store.RecordUsage(id, "a", true, 10.0)
	store.RecordUsage(id, "b", true, 20.0)
	store.RecordUsage(id, "c", false, 5.0)

	stats, err := store.GetStats(id, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalRequests != 3 {
		t.Errorf("TotalRequests: got %d, want 3", stats.TotalRequests)
	}
	if stats.SuccessCount != 2 {
		t.Errorf("SuccessCount: got %d, want 2", stats.SuccessCount)
	}
	if stats.FailCount != 1 {
		t.Errorf("FailCount: got %d, want 1", stats.FailCount)
	}
}

func TestCreateWithExpiry(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db.Conn())

	expires := time.Now().Add(24 * time.Hour)
	hash := HashToken("expiry")
	id, err := store.Create("expiry-test", hash, &expires, 0, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	tok, _ := store.GetByID(id)
	if tok.ExpiresAt == nil {
		t.Fatal("expected expires_at to be set")
	}
	if tok.IsExpired() {
		t.Error("token should not be expired yet")
	}
}
