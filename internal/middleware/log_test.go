package middleware

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"rua/internal/jsonrpc"
	"rua/internal/storage"
	"rua/internal/telemetry"
)

// mockRecorder captures telemetry events for assertions.
type mockRecorder struct {
	mu     sync.Mutex
	events []telemetry.Event
}

func (m *mockRecorder) Record(e telemetry.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

func (m *mockRecorder) all() []telemetry.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]telemetry.Event, len(m.events))
	copy(out, m.events)
	return out
}

func openTempDB(t *testing.T) *storage.DB {
	t.Helper()
	f, err := os.CreateTemp("", "*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() { os.Remove(path) })

	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("opening temp db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestMiddleware(t *testing.T) *LogMiddleware {
	t.Helper()
	db := openTempDB(t)
	lm := NewLogMiddleware(db, slog.Default(), nil)
	t.Cleanup(func() { lm.Close() })
	return lm
}

func TestLogMiddlewareRequest(t *testing.T) {
	lm := newTestMiddleware(t)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      jsonrpc.NumberID(1),
		Method:  "tools/call",
	}

	out, err := lm.ProcessRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessRequest returned error: %v", err)
	}
	if out == nil {
		t.Fatal("ProcessRequest returned nil request")
	}

	key := idString(req.ID)
	lm.mu.Lock()
	_, ok := lm.pending[key]
	lm.mu.Unlock()
	if !ok {
		t.Errorf("expected pending entry for key %q", key)
	}
}

func TestLogMiddlewareNotification(t *testing.T) {
	lm := newTestMiddleware(t)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      nil,
		Method:  "notifications/progress",
	}

	out, err := lm.ProcessRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessRequest (notification) returned error: %v", err)
	}
	if out == nil {
		t.Fatal("ProcessRequest returned nil request")
	}

	time.Sleep(10 * time.Millisecond)

	lm.mu.Lock()
	pendingLen := len(lm.pending)
	lm.mu.Unlock()
	if pendingLen != 0 {
		t.Errorf("expected no pending entries for notification, got %d", pendingLen)
	}
}

func TestLogMiddlewareResponse(t *testing.T) {
	lm := newTestMiddleware(t)

	id := jsonrpc.NumberID(1)
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      id,
		Method:  "tools/call",
	}

	if _, err := lm.ProcessRequest(context.Background(), req); err != nil {
		t.Fatalf("ProcessRequest: %v", err)
	}

	resp := &jsonrpc.Response{
		JSONRPC: jsonrpc.Version,
		ID:      id,
	}

	out, err := lm.ProcessResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("ProcessResponse returned error: %v", err)
	}
	if out == nil {
		t.Fatal("ProcessResponse returned nil response")
	}

	key := idString(id)
	lm.mu.Lock()
	_, ok := lm.pending[key]
	lm.mu.Unlock()
	if ok {
		t.Errorf("expected pending entry for key %q to be removed after response", key)
	}

	time.Sleep(10 * time.Millisecond)
}

func TestLogMiddlewareTelemetryNotification(t *testing.T) {
	db := openTempDB(t)
	rec := &mockRecorder{}
	lm := NewLogMiddleware(db, slog.Default(), rec)
	t.Cleanup(func() { lm.Close() })

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      nil,
		Method:  "notifications/progress",
	}

	if _, err := lm.ProcessRequest(context.Background(), req); err != nil {
		t.Fatalf("ProcessRequest: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("expected 1 telemetry event, got %d", len(events))
	}
	e := events[0]
	if e.Direction != "request" {
		t.Errorf("Direction = %q, want %q", e.Direction, "request")
	}
	if e.AuthStatus != "unsigned" {
		t.Errorf("AuthStatus = %q, want %q", e.AuthStatus, "unsigned")
	}
	if e.Method != "notifications/progress" {
		t.Errorf("Method = %q, want %q", e.Method, "notifications/progress")
	}
}

func TestLogMiddlewareTelemetryResponse(t *testing.T) {
	db := openTempDB(t)
	rec := &mockRecorder{}
	lm := NewLogMiddleware(db, slog.Default(), rec)
	t.Cleanup(func() { lm.Close() })

	id := jsonrpc.NumberID(42)
	req := &jsonrpc.Request{JSONRPC: jsonrpc.Version, ID: id, Method: "tools/call"}
	if _, err := lm.ProcessRequest(context.Background(), req); err != nil {
		t.Fatalf("ProcessRequest: %v", err)
	}

	resp := &jsonrpc.Response{JSONRPC: jsonrpc.Version, ID: id}
	if _, err := lm.ProcessResponse(context.Background(), resp); err != nil {
		t.Fatalf("ProcessResponse: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("expected 1 telemetry event, got %d", len(events))
	}
	e := events[0]
	if e.Direction != "response" {
		t.Errorf("Direction = %q, want %q", e.Direction, "response")
	}
	if e.AuthStatus != "unsigned" {
		t.Errorf("AuthStatus = %q, want %q (empty should default to unsigned)", e.AuthStatus, "unsigned")
	}
	if e.Method != "tools/call" {
		t.Errorf("Method = %q, want %q", e.Method, "tools/call")
	}
	if !e.Success {
		t.Errorf("Success = false, want true (no error in response)")
	}
}

func TestLogMiddlewareTelemetryNilRecorder(t *testing.T) {
	db := openTempDB(t)
	lm := NewLogMiddleware(db, slog.Default(), nil)
	t.Cleanup(func() { lm.Close() })

	req := &jsonrpc.Request{JSONRPC: jsonrpc.Version, ID: nil, Method: "notifications/x"}
	if _, err := lm.ProcessRequest(context.Background(), req); err != nil {
		t.Fatalf("ProcessRequest: %v", err)
	}
}

func TestLogMiddlewareClose(t *testing.T) {
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
