package compliance

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/itdar/shield-agent/internal/storage"
)

// DigestWriter periodically appends the egress hash-chain tail to an
// external, append-only JSONL file. The point of the external file is
// defense-in-depth against a full-DB-access attacker: even if the
// attacker rewrites every egress_logs row + anchor consistently, the
// external digest retains a historical witness of the old tail.
//
// Each digest line is one JSON object: {"ts":"...","tail":"...","rows":N}.
// The file is O_APPEND|O_CREATE with 0600 permissions and is never
// rotated or truncated by shield-agent. Operators rotate externally.
type DigestWriter struct {
	path     string
	interval time.Duration
	logger   *slog.Logger
	db       *storage.DB
	hashes   *HashChain

	mu     sync.Mutex
	once   sync.Once
	done   chan struct{}
	ticker *time.Ticker
}

// DigestEntry is the on-disk JSON shape. Kept flat and small so parsing
// remains trivial in whatever tool processes the file.
type DigestEntry struct {
	Timestamp time.Time `json:"ts"`
	Tail      string    `json:"tail"`
	RowCount  int64     `json:"rows"`
	Anchors   int       `json:"anchors"`
}

// NewDigestWriter constructs a writer. interval <= 0 defaults to 24h.
// The writer is not started until Run() is called.
func NewDigestWriter(db *storage.DB, hc *HashChain, path string, interval time.Duration, logger *slog.Logger) *DigestWriter {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &DigestWriter{
		path:     path,
		interval: interval,
		logger:   logger,
		db:       db,
		hashes:   hc,
		done:     make(chan struct{}),
	}
}

// Run blocks until Stop() is called (or the writer is GC'd). It writes
// one digest on startup and then one every interval. Call from a
// dedicated goroutine.
func (w *DigestWriter) Run() {
	w.once.Do(func() {
		w.ticker = time.NewTicker(w.interval)
	})
	if err := w.writeOnce(); err != nil {
		w.logger.Error("digest write failed", slog.String("err", err.Error()))
	}
	for {
		select {
		case <-w.done:
			return
		case <-w.ticker.C:
			if err := w.writeOnce(); err != nil {
				w.logger.Error("digest write failed", slog.String("err", err.Error()))
			}
		}
	}
}

// Stop ends the Run loop and closes the ticker.
func (w *DigestWriter) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.done:
	default:
		close(w.done)
		if w.ticker != nil {
			w.ticker.Stop()
		}
	}
}

// writeOnce appends a single digest line. Disk and SQL errors are
// propagated so Run can log them; partial writes are prevented by
// building the line in memory first.
func (w *DigestWriter) writeOnce() error {
	count, err := w.db.CountEgressLogs(0)
	if err != nil {
		return fmt.Errorf("digest: count rows: %w", err)
	}
	anchors, err := w.db.ListEgressAnchors()
	if err != nil {
		return fmt.Errorf("digest: list anchors: %w", err)
	}
	tail := ""
	if w.hashes != nil {
		tail = w.hashes.Tail()
	}
	entry := DigestEntry{
		Timestamp: time.Now().UTC(),
		Tail:      tail,
		RowCount:  count,
		Anchors:   len(anchors),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("digest: marshal: %w", err)
	}
	line = append(line, '\n')

	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return fmt.Errorf("digest: mkdir: %w", err)
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("digest: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("digest: write: %w", err)
	}
	return f.Sync()
}

// ReadDigestFile parses a digest JSONL file into a slice of entries.
// Used by the audit export and external tooling.
func ReadDigestFile(path string) ([]DigestEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []DigestEntry
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var entry DigestEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("parse digest line: %w", err)
		}
		out = append(out, entry)
	}
	return out, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
