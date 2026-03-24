package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/telemetry"
)

// Recorder forwards telemetry events to an external collector.
// Satisfied by *telemetry.Collector.
type Recorder interface {
	Record(event telemetry.Event)
}

// LogMiddleware records agent→HTTP API request/response pairs to the database.
type LogMiddleware struct {
	db       *storage.DB
	logger   *slog.Logger
	writeCh  chan storage.ActionLog
	recorder Recorder
}

// NewLogMiddleware creates a LogMiddleware and starts its background writer.
// recorder may be nil to disable telemetry forwarding.
func NewLogMiddleware(db *storage.DB, logger *slog.Logger, recorder Recorder) *LogMiddleware {
	lm := &LogMiddleware{
		db:       db,
		logger:   logger,
		writeCh:  make(chan storage.ActionLog, 512),
		recorder: recorder,
	}
	go lm.writer()
	return lm
}

// WrapHandler returns an http.Handler that logs agent→API request/response pairs.
func (lm *LogMiddleware) WrapHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()

		// Read and restore body to measure payload size.
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))

		// Use "METHOD /path" as the method label for HTTP API calls.
		apiMethod := fmt.Sprintf("%s %s", r.Method, r.URL.Path)

		agentID := r.Header.Get(agentIDHeader)
		agentHash := ""
		if agentID != "" {
			agentHash = auth.AgentIDHash(agentID)
		}

		rw := wrapResponseWriter(w)
		next.ServeHTTP(rw, r)

		latencyMs := float64(time.Since(startedAt).Milliseconds())
		success := rw.status < 400
		now := time.Now().UTC()

		lm.enqueue(storage.ActionLog{
			Timestamp:   now,
			AgentIDHash: agentHash,
			Method:      apiMethod,
			Direction:   "in",
			Success:     success,
			LatencyMs:   latencyMs,
			PayloadSize: len(body),
			ErrorCode:   httpErrorCode(rw.status),
		})

		if lm.recorder != nil {
			lm.recorder.Record(telemetry.Event{
				AgentIDHash:      agentHash,
				Timestamp:        now,
				Method:           apiMethod,
				Direction:        "request",
				Success:          success,
				LatencyMs:        latencyMs,
				PayloadSizeBytes: len(body),
			})
		}
	})
}

// httpErrorCode returns the HTTP status code as a string for non-2xx responses.
func httpErrorCode(status int) string {
	if status >= 400 {
		return strconv.Itoa(status)
	}
	return ""
}

// enqueue sends a log entry to the write channel non-blocking.
func (lm *LogMiddleware) enqueue(log storage.ActionLog) {
	select {
	case lm.writeCh <- log:
	default:
		lm.logger.Warn("httpapi log write channel full, dropping entry")
	}
}

// writer drains the write channel and persists entries.
func (lm *LogMiddleware) writer() {
	for log := range lm.writeCh {
		if err := lm.db.Insert(log); err != nil {
			lm.logger.Error("failed to insert HTTP API log entry", slog.String("error", err.Error()))
		}
	}
}

// Close shuts down the background writer.
func (lm *LogMiddleware) Close() {
	close(lm.writeCh)
}
