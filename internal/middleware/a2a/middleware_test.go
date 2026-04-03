package a2a

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/storage"
)

// --- Chain tests ---

// recordingMiddleware records the order in which WrapHandler is called.
type recordingMiddleware struct {
	label  string
	record *[]string
}

func (r *recordingMiddleware) WrapHandler(next http.Handler) http.Handler {
	*r.record = append(*r.record, r.label)
	return next
}

func TestNewChain_CreatesChain(t *testing.T) {
	var order []string
	a := &recordingMiddleware{label: "A", record: &order}
	b := &recordingMiddleware{label: "B", record: &order}
	c := &recordingMiddleware{label: "C", record: &order}

	chain := NewChain(a, b, c)
	if chain == nil {
		t.Fatal("NewChain returned nil")
	}
	if len(chain.items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(chain.items))
	}
}

func TestChain_HandlerWrapsInOrder(t *testing.T) {
	// We verify that the outermost middleware (index 0) wraps first by tracking
	// call order in WrapHandler. Chain.Handler iterates in reverse so index 0
	// ends up as the outermost layer.
	var wrapOrder []string
	newMW := func(label string) Middleware {
		return &recordingMiddleware{label: label, record: &wrapOrder}
	}

	chain := NewChain(newMW("A"), newMW("B"), newMW("C"))
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	chain.Handler(final)

	// WrapHandler is called in reverse order (C, B, A) so outermost is A.
	if len(wrapOrder) != 3 {
		t.Fatalf("expected 3 WrapHandler calls, got %d", len(wrapOrder))
	}
	if wrapOrder[0] != "C" || wrapOrder[1] != "B" || wrapOrder[2] != "A" {
		t.Errorf("unexpected wrap order: %v (want [C B A])", wrapOrder)
	}
}

func TestChain_EmptyPassesThrough(t *testing.T) {
	chain := NewChain()
	called := false
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := chain.Handler(final)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatal("expected final handler to be called for empty chain")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- responseWriter tests ---

func TestWrapResponseWriter_DefaultStatus(t *testing.T) {
	w := httptest.NewRecorder()
	rw := wrapResponseWriter(w)
	if rw.status != http.StatusOK {
		t.Errorf("expected default status 200, got %d", rw.status)
	}
	if rw.size != 0 {
		t.Errorf("expected initial size 0, got %d", rw.size)
	}
}

func TestResponseWriter_WriteHeaderCapturesStatus(t *testing.T) {
	tests := []struct {
		code int
	}{
		{http.StatusCreated},
		{http.StatusBadRequest},
		{http.StatusUnauthorized},
		{http.StatusInternalServerError},
	}
	for _, tc := range tests {
		w := httptest.NewRecorder()
		rw := wrapResponseWriter(w)
		rw.WriteHeader(tc.code)
		if rw.status != tc.code {
			t.Errorf("WriteHeader(%d): got status %d", tc.code, rw.status)
		}
		if w.Code != tc.code {
			t.Errorf("WriteHeader(%d): underlying writer got %d", tc.code, w.Code)
		}
	}
}

func TestResponseWriter_WriteCapturesSize(t *testing.T) {
	w := httptest.NewRecorder()
	rw := wrapResponseWriter(w)

	payload := []byte("hello world")
	n, err := rw.Write(payload)
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write returned %d, want %d", n, len(payload))
	}
	if rw.size != len(payload) {
		t.Errorf("size: got %d, want %d", rw.size, len(payload))
	}

	// A second write accumulates.
	extra := []byte("!")
	rw.Write(extra)
	if rw.size != len(payload)+len(extra) {
		t.Errorf("size after second write: got %d, want %d", rw.size, len(payload)+len(extra))
	}
}

// --- Auth tests ---

// stubKeyStore satisfies auth.KeyStore and always returns an error (no keys).
type stubKeyStore struct{}

func (s *stubKeyStore) PublicKey(agentID string) (interface{ Equal(x interface{}) bool }, error) {
	return nil, nil
}

func TestNewAuthMiddleware_SetsCorrectHeaders(t *testing.T) {
	store := &auth.FileKeyStore{}
	logger := slog.Default()
	am := NewAuthMiddleware(store, "open", logger, nil)
	if am == nil {
		t.Fatal("NewAuthMiddleware returned nil")
	}
	if am.cfg.AgentIDHeader != "X-Agent-ID" {
		t.Errorf("AgentIDHeader: got %q, want %q", am.cfg.AgentIDHeader, "X-Agent-ID")
	}
	if am.cfg.SignatureHeader != "X-A2A-Signature" {
		t.Errorf("SignatureHeader: got %q, want %q", am.cfg.SignatureHeader, "X-A2A-Signature")
	}
}

func TestAuthMiddleware_WrapHandlerNonNil(t *testing.T) {
	store := &auth.FileKeyStore{}
	am := NewAuthMiddleware(store, "open", slog.Default(), nil)
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := am.WrapHandler(final)
	if h == nil {
		t.Fatal("WrapHandler returned nil")
	}
}

// --- Log tests ---

func TestExtractA2AMethod_ValidJSON(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		want   string
	}{
		{"simple method", `{"method":"tasks/send","id":1}`, "tasks/send"},
		{"jsonrpc envelope", `{"jsonrpc":"2.0","method":"tools/call","id":2}`, "tools/call"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractA2AMethod([]byte(tc.body))
			if got != tc.want {
				t.Errorf("extractA2AMethod(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestExtractA2AMethod_InvalidJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"not json", "not-json"},
		{"no method field", `{"id":1}`},
		{"empty method", `{"method":""}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractA2AMethod([]byte(tc.body))
			if got != "unknown" {
				t.Errorf("extractA2AMethod(%q) = %q, want %q", tc.body, got, "unknown")
			}
		})
	}
}

func TestHTTPErrorCode(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{200, ""},
		{201, ""},
		{204, ""},
		{301, ""},
		{399, ""},
		{400, "400"},
		{401, "401"},
		{403, "403"},
		{404, "404"},
		{500, "500"},
		{503, "503"},
	}
	for _, tc := range tests {
		got := httpErrorCode(tc.status)
		if got != tc.want {
			t.Errorf("httpErrorCode(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func openTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestLogMiddleware_RecordsEntry(t *testing.T) {
	db := openTestDB(t)
	logger := slog.Default()

	lm := NewLogMiddleware(db, logger, nil)

	body := `{"jsonrpc":"2.0","method":"tasks/send","id":1}`
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := lm.WrapHandler(final)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Agent-ID", "test-agent")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Close to flush the background writer.
	lm.Close()

	// Allow a moment for the goroutine to drain before Close returns.
	// Close() closes the channel which causes writer() to return after draining.
	// Give the goroutine time to finish the final insert.
	deadline := time.Now().Add(2 * time.Second)
	var logs []storage.ActionLog
	var err error
	for time.Now().Before(deadline) {
		logs, err = db.QueryLogs(storage.QueryOptions{Last: 10})
		if err != nil {
			t.Fatalf("QueryLogs: %v", err)
		}
		if len(logs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	entry := logs[0]
	if entry.Method != "tasks/send" {
		t.Errorf("Method: got %q, want %q", entry.Method, "tasks/send")
	}
	if entry.Direction != "in" {
		t.Errorf("Direction: got %q, want %q", entry.Direction, "in")
	}
	if !entry.Success {
		t.Errorf("Success: expected true for 200 response")
	}
	if entry.ErrorCode != "" {
		t.Errorf("ErrorCode: expected empty for 200, got %q", entry.ErrorCode)
	}
	if entry.PayloadSize != len(body) {
		t.Errorf("PayloadSize: got %d, want %d", entry.PayloadSize, len(body))
	}
	wantHash := auth.AgentIDHash("test-agent")
	if entry.AgentIDHash != wantHash {
		t.Errorf("AgentIDHash: got %q, want %q", entry.AgentIDHash, wantHash)
	}
}

func TestLogMiddleware_ErrorResponse(t *testing.T) {
	db := openTestDB(t)
	lm := NewLogMiddleware(db, slog.Default(), nil)

	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	handler := lm.WrapHandler(final)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"method":"bad"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	lm.Close()

	deadline := time.Now().Add(2 * time.Second)
	var logs []storage.ActionLog
	for time.Now().Before(deadline) {
		logs, _ = db.QueryLogs(storage.QueryOptions{Last: 10})
		if len(logs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	if logs[0].Success {
		t.Error("Success: expected false for 400 response")
	}
	if logs[0].ErrorCode != "400" {
		t.Errorf("ErrorCode: got %q, want %q", logs[0].ErrorCode, "400")
	}
}

func TestLogMiddleware_Close(t *testing.T) {
	db := openTestDB(t)
	lm := NewLogMiddleware(db, slog.Default(), nil)

	// Close should not panic or block.
	done := make(chan struct{})
	go func() {
		lm.Close()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not return within 2 seconds")
	}
}
