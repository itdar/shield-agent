package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/itdar/shield-agent/internal/jsonrpc"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/telemetry"
)

// Recorder forwards telemetry events to an external collector.
// Satisfied by *telemetry.Collector.
type Recorder interface {
	Record(event telemetry.Event)
}

// pendingRequest tracks an in-flight request waiting for its response.
type pendingRequest struct {
	method     string
	agentHash  string
	authStatus string
	startedAt  time.Time
}

// LogMiddleware records request/response pairs to the database.
type LogMiddleware struct {
	PassthroughMiddleware
	db        *storage.DB
	logger    *slog.Logger
	writeCh   chan storage.ActionLog
	mu        sync.Mutex
	pending   map[string]pendingRequest
	recorder  Recorder                                   // may be nil
	onMessage func(direction, method string, latencyMs float64) // may be nil; Prometheus callback
}

// NewLogMiddleware creates a LogMiddleware and starts its background writer.
// recorder may be nil to disable telemetry forwarding.
// onMessage may be nil to skip Prometheus counter/histogram updates.
func NewLogMiddleware(db *storage.DB, logger *slog.Logger, recorder Recorder, onMessage func(direction, method string, latencyMs float64)) *LogMiddleware {
	lm := &LogMiddleware{
		db:        db,
		logger:    logger,
		writeCh:   make(chan storage.ActionLog, 512),
		pending:   make(map[string]pendingRequest),
		recorder:  recorder,
		onMessage: onMessage,
	}
	go lm.writer()
	return lm
}

// Name returns the name of this middleware.
func (lm *LogMiddleware) Name() string { return "log" }

// SetAuthStatus updates the pending request's auth status and agent hash.
func (lm *LogMiddleware) SetAuthStatus(reqID, status, agentHash string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	if p, ok := lm.pending[reqID]; ok {
		p.authStatus = status
		p.agentHash = agentHash
		lm.pending[reqID] = p
	}
}

// ProcessRequest records the request and stores pending state.
func (lm *LogMiddleware) ProcessRequest(ctx context.Context, req *jsonrpc.Request) (*jsonrpc.Request, error) {
	if req.IsNotification() {
		now := time.Now().UTC()
		lm.enqueue(storage.ActionLog{
			Timestamp:   now,
			AgentIDHash: "",
			Method:      req.Method,
			Direction:   "in",
			Success:     true,
			PayloadSize: len(req.Params),
		})
		if lm.recorder != nil {
			lm.recorder.Record(telemetry.Event{
				Timestamp:        now,
				Method:           req.Method,
				Direction:        "request",
				Success:          true,
				PayloadSizeBytes: len(req.Params),
				AuthStatus:       "unsigned",
			})
		}
		if lm.onMessage != nil {
			lm.onMessage("in", req.Method, 0)
		}
		return req, nil
	}

	key := idString(req.ID)
	lm.mu.Lock()
	lm.pending[key] = pendingRequest{
		method:    req.Method,
		startedAt: time.Now(),
	}
	lm.mu.Unlock()

	return req, nil
}

// ProcessResponse looks up the pending request, computes latency, and enqueues a log entry.
func (lm *LogMiddleware) ProcessResponse(ctx context.Context, resp *jsonrpc.Response) (*jsonrpc.Response, error) {
	key := idString(resp.ID)

	lm.mu.Lock()
	p, ok := lm.pending[key]
	if ok {
		delete(lm.pending, key)
	}
	lm.mu.Unlock()

	var latencyMs float64
	var method string
	var agentHash string
	var authStatus string

	if ok {
		latencyMs = float64(time.Since(p.startedAt).Milliseconds())
		method = p.method
		agentHash = p.agentHash
		authStatus = p.authStatus
	}

	// Read auth result and IP from context (set by auth middleware / transport layer).
	if ar := GetAuthResult(ctx); ar != nil {
		if ar.AgentIDHash != "" {
			agentHash = ar.AgentIDHash
		}
		if ar.Status != "" {
			authStatus = ar.Status
		}
	}

	success := resp.Error == nil
	errorCode := ""
	if resp.Error != nil {
		errorCode = fmt.Sprintf("%d", resp.Error.Code)
	}

	now := time.Now().UTC()

	var ipAddr string
	if ar := GetAuthResult(ctx); ar != nil {
		ipAddr = ar.IPAddress
	}

	lm.enqueue(storage.ActionLog{
		Timestamp:   now,
		AgentIDHash: agentHash,
		Method:      method,
		Direction:   "out",
		Success:     success,
		LatencyMs:   latencyMs,
		PayloadSize: len(resp.Result),
		AuthStatus:  authStatus,
		ErrorCode:   errorCode,
		IPAddress:   ipAddr,
	})

	if lm.recorder != nil {
		effectiveAuthStatus := authStatus
		if effectiveAuthStatus == "" {
			effectiveAuthStatus = "unsigned"
		}
		lm.recorder.Record(telemetry.Event{
			AgentIDHash:      agentHash,
			Timestamp:        now,
			Method:           method,
			Direction:        "response",
			Success:          success,
			LatencyMs:        latencyMs,
			PayloadSizeBytes: len(resp.Result),
			AuthStatus:       effectiveAuthStatus,
			ErrorCode:        errorCode,
		})
	}

	if lm.onMessage != nil {
		lm.onMessage("out", method, latencyMs)
	}

	return resp, nil
}

// enqueue sends a log entry to the write channel non-blocking.
func (lm *LogMiddleware) enqueue(log storage.ActionLog) {
	select {
	case lm.writeCh <- log:
	default:
		lm.logger.Warn("log write channel full, dropping entry")
	}
}

// writer drains the write channel and persists entries.
func (lm *LogMiddleware) writer() {
	for log := range lm.writeCh {
		if err := lm.db.Insert(log); err != nil {
			lm.logger.Error("failed to insert log entry", slog.String("error", err.Error()))
		}
	}
}

// Close shuts down the background writer.
func (lm *LogMiddleware) Close() {
	close(lm.writeCh)
}

// idString returns a string representation of a JSON-RPC ID.
func idString(id *jsonrpc.ID) string {
	if id == nil {
		return "null"
	}
	b, err := json.Marshal(id)
	if err != nil {
		return "null"
	}
	return string(b)
}
