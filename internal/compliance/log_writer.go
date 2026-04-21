// Package compliance holds the egress-side policy and audit machinery.
//
// The egress compliance layer treats log persistence as a regulatory
// obligation (EU AI Act Art. 12, Korea AI Basic Act audit requirements)
// and therefore must not silently drop entries. LogWriter enforces that
// guarantee with a bounded-but-blocking channel and retry-backed writer
// goroutine. It differs from the ingress middleware/log.go pattern,
// which drops on overflow.
package compliance

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/itdar/shield-agent/internal/storage"
)

// defaultWriteRetries bounds how many times the writer goroutine re-tries
// a single failed Insert before giving up and incrementing the error counter.
const defaultWriteRetries = 3

// defaultRetryBackoff is the base delay between retries (doubled each attempt).
const defaultRetryBackoff = 50 * time.Millisecond

// ErrWriterClosed is returned by EnqueueSync when the writer is shutting down.
var ErrWriterClosed = errors.New("compliance log writer is closed")

// WriterMetrics captures the observable counters the Prometheus collector
// (internal/monitor) can read. The concrete LogWriter updates these atomically.
type WriterMetrics interface {
	IncLogWriteError()
	ObserveQueueLength(n int)
}

// noopMetrics is used when Prometheus wiring is absent (tests).
type noopMetrics struct{}

func (noopMetrics) IncLogWriteError()       {}
func (noopMetrics) ObserveQueueLength(int)  {}

// LogWriter persists EgressLog rows without dropping on backpressure.
//
// Enqueue is blocking: when the channel is full, callers wait until the
// writer drains. This is the intentional "Auditability > Performance"
// trade-off. Use EnqueueSync when the caller (policy_mode=block path)
// must observe the write's outcome before responding to the client.
type LogWriter struct {
	db      *storage.DB
	logger  *slog.Logger
	metrics WriterMetrics

	writeCh chan writeJob
	done    chan struct{}
	wg      sync.WaitGroup
	closed  chan struct{}
	closeMu sync.Mutex

	retries int
	backoff time.Duration
}

// writeJob carries a single egress log row plus an optional ack channel.
// ackCh is nil for fire-and-forget Enqueue; EnqueueSync supplies a channel
// so the caller learns whether the insert ultimately succeeded.
type writeJob struct {
	log   storage.EgressLog
	ackCh chan writeResult
}

type writeResult struct {
	id  int64
	err error
}

// LogWriterOptions configures optional behaviour.
type LogWriterOptions struct {
	// BufferSize caps the number of queued log rows before Enqueue blocks.
	// A small buffer gives tighter backpressure; a large one smooths bursts.
	// Defaults to 256 when zero.
	BufferSize int
	// Metrics is the Prometheus collector. Optional; noopMetrics when nil.
	Metrics WriterMetrics
	// Retries bounds per-row insert retries. Defaults to defaultWriteRetries.
	Retries int
	// RetryBackoff is the initial sleep between retries (doubles each time).
	RetryBackoff time.Duration
}

// NewLogWriter creates and starts a writer goroutine.
// Close must be called to flush and shut down cleanly.
func NewLogWriter(db *storage.DB, logger *slog.Logger, opts LogWriterOptions) *LogWriter {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 256
	}
	if opts.Retries <= 0 {
		opts.Retries = defaultWriteRetries
	}
	if opts.RetryBackoff <= 0 {
		opts.RetryBackoff = defaultRetryBackoff
	}
	m := opts.Metrics
	if m == nil {
		m = noopMetrics{}
	}

	w := &LogWriter{
		db:      db,
		logger:  logger,
		metrics: m,
		writeCh: make(chan writeJob, opts.BufferSize),
		done:    make(chan struct{}),
		closed:  make(chan struct{}),
		retries: opts.Retries,
		backoff: opts.RetryBackoff,
	}
	w.wg.Add(1)
	go w.writer()
	return w
}

// Enqueue queues a row for asynchronous persistence. It blocks until
// channel space is available, which is the drop-free contract. It returns
// ErrWriterClosed if Close has already been initiated.
func (w *LogWriter) Enqueue(ctx context.Context, log storage.EgressLog) error {
	select {
	case <-w.done:
		return ErrWriterClosed
	default:
	}

	select {
	case w.writeCh <- writeJob{log: log}:
		w.metrics.ObserveQueueLength(len(w.writeCh))
		return nil
	case <-w.done:
		return ErrWriterClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EnqueueSync queues a row and waits for the write to complete.
// Used by the policy_mode=block path: when the write fails after all
// retries, the caller should reject the outbound request (HTTP 503).
// Returns the inserted row id on success.
func (w *LogWriter) EnqueueSync(ctx context.Context, log storage.EgressLog) (int64, error) {
	select {
	case <-w.done:
		return 0, ErrWriterClosed
	default:
	}

	ackCh := make(chan writeResult, 1)
	select {
	case w.writeCh <- writeJob{log: log, ackCh: ackCh}:
		w.metrics.ObserveQueueLength(len(w.writeCh))
	case <-w.done:
		return 0, ErrWriterClosed
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	select {
	case res := <-ackCh:
		return res.id, res.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// QueueLength returns the current queued row count (for metrics / tests).
func (w *LogWriter) QueueLength() int {
	return len(w.writeCh)
}

// Close initiates shutdown: no further Enqueue is accepted, in-flight
// queue is drained, writer goroutine exits, and Close returns after the
// final DB write. Safe to call multiple times (second call is a no-op).
func (w *LogWriter) Close() {
	w.closeMu.Lock()
	select {
	case <-w.closed:
		w.closeMu.Unlock()
		return
	default:
	}
	close(w.closed)
	w.closeMu.Unlock()

	// Signal Enqueue to stop accepting. close(done) must happen before
	// close(writeCh) so in-flight Enqueues observe done and return
	// ErrWriterClosed instead of sending on a closed channel.
	close(w.done)
	// Drain existing queue — writer goroutine sees the close and exits
	// when the channel is empty.
	close(w.writeCh)
	w.wg.Wait()
}

// writer is the single goroutine that drains writeCh. It retries each
// row up to w.retries times with exponential backoff, then logs + counts
// the failure and moves on. The hash chain continuity is preserved by
// the egress middleware (which computes row_hash before Enqueue), so
// a dropped row still shows up in the verifier as a gap.
func (w *LogWriter) writer() {
	defer w.wg.Done()
	for job := range w.writeCh {
		id, err := w.insertWithRetry(job.log)
		if err != nil {
			w.metrics.IncLogWriteError()
			w.logger.Error("egress log write failed after retries",
				slog.String("destination", job.log.Destination),
				slog.String("row_hash", job.log.RowHash),
				slog.String("error", err.Error()),
			)
		}
		if job.ackCh != nil {
			job.ackCh <- writeResult{id: id, err: err}
		}
	}
}

func (w *LogWriter) insertWithRetry(log storage.EgressLog) (int64, error) {
	var lastErr error
	backoff := w.backoff
	for attempt := 0; attempt < w.retries; attempt++ {
		id, err := w.db.InsertEgressLog(log)
		if err == nil {
			return id, nil
		}
		lastErr = err
		if attempt < w.retries-1 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return 0, lastErr
}
