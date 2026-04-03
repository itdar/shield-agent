package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itdar/shield-agent/internal/storage"
)

// --- Chain tests ---

// recordMiddleware records calls to WrapHandler and delegates to next.
type recordMiddleware struct {
	label  string
	order  *[]string
	mu     *sync.Mutex
}

func (r *recordMiddleware) WrapHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		*r.order = append(*r.order, r.label)
		r.mu.Unlock()
		next.ServeHTTP(w, req)
	})
}

func TestNewChain(t *testing.T) {
	var order []string
	mu := &sync.Mutex{}
	a := &recordMiddleware{label: "A", order: &order, mu: mu}
	b := &recordMiddleware{label: "B", order: &order, mu: mu}
	c := &recordMiddleware{label: "C", order: &order, mu: mu}

	chain := NewChain(a, b, c)
	if chain == nil {
		t.Fatal("NewChain returned nil")
	}

	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := chain.Handler(final)
	if handler == nil {
		t.Fatal("Handler returned nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if len(order) != 3 {
		t.Fatalf("expected 3 middleware calls, got %d", len(order))
	}
	// First middleware added = outermost = called first.
	if order[0] != "A" || order[1] != "B" || order[2] != "C" {
		t.Errorf("unexpected order: %v", order)
	}
}

func TestEmptyChain(t *testing.T) {
	chain := NewChain()

	called := false
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := chain.Handler(final)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("expected final handler to be called with empty chain")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- responseWriter tests ---

func TestWrapResponseWriterDefaultStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)

	if rw.status != http.StatusOK {
		t.Errorf("default status = %d, want %d", rw.status, http.StatusOK)
	}
	if rw.size != 0 {
		t.Errorf("default size = %d, want 0", rw.size)
	}
}

func TestResponseWriterWriteHeader(t *testing.T) {
	cases := []struct {
		code int
	}{
		{http.StatusOK},
		{http.StatusCreated},
		{http.StatusBadRequest},
		{http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.code), func(t *testing.T) {
			rec := httptest.NewRecorder()
			rw := wrapResponseWriter(rec)
			rw.WriteHeader(tc.code)

			if rw.status != tc.code {
				t.Errorf("status = %d, want %d", rw.status, tc.code)
			}
			if rec.Code != tc.code {
				t.Errorf("underlying writer code = %d, want %d", rec.Code, tc.code)
			}
		})
	}
}

func TestResponseWriterWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)

	payload := []byte("hello world")
	n, err := rw.Write(payload)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write returned %d, want %d", n, len(payload))
	}
	if rw.size != len(payload) {
		t.Errorf("size = %d, want %d", rw.size, len(payload))
	}
	if rec.Body.String() != string(payload) {
		t.Errorf("body = %q, want %q", rec.Body.String(), string(payload))
	}
}

func TestResponseWriterWriteAccumulates(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)

	rw.Write([]byte("foo"))
	rw.Write([]byte("bar"))

	if rw.size != 6 {
		t.Errorf("size = %d, want 6", rw.size)
	}
}

// --- Auth tests ---

func TestNewAuthMiddleware_Headers(t *testing.T) {
	am := NewAuthMiddleware(nil, "open", slog.Default(), nil)
	if am == nil {
		t.Fatal("NewAuthMiddleware returned nil")
	}
	if am.cfg.AgentIDHeader != "X-Agent-ID" {
		t.Errorf("AgentIDHeader = %q, want X-Agent-ID", am.cfg.AgentIDHeader)
	}
	if am.cfg.SignatureHeader != "X-Agent-Signature" {
		t.Errorf("SignatureHeader = %q, want X-Agent-Signature", am.cfg.SignatureHeader)
	}
}

func TestNewAuthMiddleware_WrapHandlerNonNil(t *testing.T) {
	am := NewAuthMiddleware(nil, "open", slog.Default(), nil)
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := am.WrapHandler(final)
	if handler == nil {
		t.Fatal("WrapHandler returned nil")
	}
}

// --- httpErrorCode tests ---

func TestHTTPErrorCode(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{200, ""},
		{201, ""},
		{301, ""},
		{399, ""},
		{400, "400"},
		{401, "401"},
		{404, "404"},
		{500, "500"},
		{503, "503"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			got := httpErrorCode(tc.status)
			if got != tc.want {
				t.Errorf("httpErrorCode(%d) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// --- LogMiddleware tests ---

func openTempDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNewLogMiddleware_WrapHandlerRecordsEntry(t *testing.T) {
	db := openTempDB(t)
	lm := NewLogMiddleware(db, slog.Default(), nil)

	const agentID = "agent-123"
	const path = "/v1/chat"

	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := lm.WrapHandler(final)

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"hello":"world"}`))
	req.Header.Set("X-Agent-ID", agentID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Close flushes the background writer.
	lm.Close()

	// Poll until the writer goroutine finishes persisting.
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

	wantMethod := fmt.Sprintf("%s %s", http.MethodPost, path)
	if entry.Method != wantMethod {
		t.Errorf("Method = %q, want %q", entry.Method, wantMethod)
	}
	if entry.Direction != "in" {
		t.Errorf("Direction = %q, want in", entry.Direction)
	}
	if !entry.Success {
		t.Errorf("Success = false, want true for 200 response")
	}
	if entry.ErrorCode != "" {
		t.Errorf("ErrorCode = %q, want empty for 200 response", entry.ErrorCode)
	}
	if entry.AgentIDHash == "" {
		t.Error("AgentIDHash should not be empty when X-Agent-ID header is set")
	}
}

func TestLogMiddleware_MethodLabel(t *testing.T) {
	cases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/resources"},
		{"POST", "/chat/completions"},
		{"DELETE", "/items/42"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			db := openTempDB(t)
			lm := NewLogMiddleware(db, slog.Default(), nil)

			final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			handler := lm.WrapHandler(final)

			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			lm.Close()

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

			want := fmt.Sprintf("%s %s", tc.method, tc.path)
			if logs[0].Method != want {
				t.Errorf("Method = %q, want %q", logs[0].Method, want)
			}
		})
	}
}

func TestLogMiddleware_ErrorStatusRecorded(t *testing.T) {
	db := openTempDB(t)
	lm := NewLogMiddleware(db, slog.Default(), nil)

	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	handler := lm.WrapHandler(final)

	req := httptest.NewRequest(http.MethodGet, "/bad", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	lm.Close()

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
	if entry.Success {
		t.Error("Success = true, want false for 400 response")
	}
	if entry.ErrorCode != "400" {
		t.Errorf("ErrorCode = %q, want 400", entry.ErrorCode)
	}
}

func TestLogMiddleware_Close(t *testing.T) {
	db := openTempDB(t)
	lm := NewLogMiddleware(db, slog.Default(), nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		lm.Close()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() deadlocked")
	}
}
