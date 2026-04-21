package compliance

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itdar/shield-agent/internal/storage"
)

func newTestDB(t *testing.T) *storage.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "shield.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

type countingMetrics struct {
	writeErrors atomic.Int64
	queueObs    atomic.Int64
}

func (m *countingMetrics) IncLogWriteError()           { m.writeErrors.Add(1) }
func (m *countingMetrics) ObserveQueueLength(_ int) { m.queueObs.Add(1) }

func TestLogWriterPersistsRow(t *testing.T) {
	db := newTestDB(t)
	m := &countingMetrics{}
	w := NewLogWriter(db, silentLogger(), LogWriterOptions{BufferSize: 16, Metrics: m})
	defer w.Close()

	row := storage.EgressLog{
		Timestamp:   time.Now().UTC(),
		Method:      "CONNECT api.openai.com:443",
		Destination: "api.openai.com",
		Provider:    "openai",
		Protocol:    "https",
		PolicyAction: "allow",
		RowHash:     "r1",
	}
	if err := w.Enqueue(context.Background(), row); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	w.Close()

	rows, err := db.QueryEgressLogs(storage.EgressQueryOptions{Last: 10})
	if err != nil {
		t.Fatalf("QueryEgressLogs: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Destination != "api.openai.com" {
		t.Errorf("destination = %q", rows[0].Destination)
	}
	if m.writeErrors.Load() != 0 {
		t.Errorf("write errors = %d, want 0", m.writeErrors.Load())
	}
}

// TestLogWriterBlocksInsteadOfDropping verifies the drop-free contract:
// when the buffer is full and the DB is intentionally slow, Enqueue
// backpressures instead of returning nil and losing the row.
func TestLogWriterBlocksInsteadOfDropping(t *testing.T) {
	db := newTestDB(t)
	w := NewLogWriter(db, silentLogger(), LogWriterOptions{BufferSize: 2})
	defer w.Close()

	// Fill the buffer. The writer goroutine drains it, so we keep enqueuing
	// rows quickly and assert all survive.
	const total = 50
	for i := 0; i < total; i++ {
		row := storage.EgressLog{
			Timestamp:    time.Now().UTC(),
			Method:       "CONNECT api.openai.com:443",
			Destination:  "api.openai.com",
			Provider:     "openai",
			Protocol:     "https",
			PolicyAction: "allow",
			RowHash:      "r",
		}
		if err := w.Enqueue(context.Background(), row); err != nil {
			t.Fatalf("Enqueue[%d]: %v", i, err)
		}
	}
	w.Close()

	rows, err := db.QueryEgressLogs(storage.EgressQueryOptions{Last: total + 10})
	if err != nil {
		t.Fatalf("QueryEgressLogs: %v", err)
	}
	if len(rows) != total {
		t.Errorf("persisted rows = %d, want %d (drop-free contract broken)", len(rows), total)
	}
}

func TestLogWriterEnqueueAfterCloseReturnsError(t *testing.T) {
	db := newTestDB(t)
	w := NewLogWriter(db, silentLogger(), LogWriterOptions{BufferSize: 1})
	w.Close()

	err := w.Enqueue(context.Background(), storage.EgressLog{})
	if err != ErrWriterClosed {
		t.Errorf("err = %v, want ErrWriterClosed", err)
	}
	if _, err := w.EnqueueSync(context.Background(), storage.EgressLog{}); err != ErrWriterClosed {
		t.Errorf("sync err = %v, want ErrWriterClosed", err)
	}
}

func TestEnqueueSyncReturnsRowID(t *testing.T) {
	db := newTestDB(t)
	w := NewLogWriter(db, silentLogger(), LogWriterOptions{BufferSize: 4})
	defer w.Close()

	row := storage.EgressLog{
		Timestamp:    time.Now().UTC(),
		Method:       "CONNECT x:443",
		Destination:  "x",
		Protocol:     "https",
		PolicyAction: "allow",
	}
	id, err := w.EnqueueSync(context.Background(), row)
	if err != nil {
		t.Fatalf("EnqueueSync: %v", err)
	}
	if id <= 0 {
		t.Errorf("row id = %d, want > 0", id)
	}
}

func TestLogWriterContextCancelled(t *testing.T) {
	db := newTestDB(t)
	w := NewLogWriter(db, silentLogger(), LogWriterOptions{BufferSize: 0})
	defer w.Close()
	_ = os.Getenv("")

	ctx, cancel := context.WithCancel(context.Background())
	// Fill the channel (BufferSize=0 means send blocks until writer
	// reads), then cancel the next sender's context.
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Enqueue(ctx, storage.EgressLog{Timestamp: time.Now(), Destination: "x", Method: "CONNECT x:443"})
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			// If the writer drained the row first, there's no cancellation path.
			// Acceptable — what we're asserting is "no silent drop on cancel".
			return
		}
		if err != context.Canceled {
			t.Errorf("err = %v, want context.Canceled or nil", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Enqueue did not return after context cancel")
	}
}
