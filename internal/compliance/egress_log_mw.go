package compliance

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/egress"
	"github.com/itdar/shield-agent/internal/storage"
)

// EgressLogMiddleware is the terminal audit middleware. It builds the
// egress_logs row from the request/response pair, runs it through the
// HashChain, and hands it to LogWriter for persistence.
//
// In policy_mode=block it uses EnqueueSync so a DB failure rejects the
// request (fail-closed). In policy_mode=warn it uses Enqueue and keeps
// going on failure (the writer still increments the error metric).
type EgressLogMiddleware struct {
	egress.PassthroughEgressMiddleware
	writer     *LogWriter
	chain      *HashChain
	policyMode string
	logger     *slog.Logger
}

// NewEgressLogMiddleware wires the middleware. Both writer and chain are
// shared resources so SIGHUP can swap the surrounding middleware chain
// without dropping in-flight writes.
func NewEgressLogMiddleware(writer *LogWriter, chain *HashChain, policyMode string, logger *slog.Logger) *EgressLogMiddleware {
	return &EgressLogMiddleware{
		writer:     writer,
		chain:      chain,
		policyMode: policyMode,
		logger:     logger,
	}
}

// Name identifies this middleware in config.
func (*EgressLogMiddleware) Name() string { return "egress_log" }

// ProcessRequest is a no-op — the audit row is built after the response.
func (m *EgressLogMiddleware) ProcessRequest(_ context.Context, req *egress.Request) (*egress.Request, error) {
	return req, nil
}

// ProcessResponse assembles the log row, hashes it, and enqueues it.
func (m *EgressLogMiddleware) ProcessResponse(ctx context.Context, req *egress.Request, resp *egress.Response) (*egress.Response, error) {
	action := req.PolicyAction
	if action == "" {
		action = "allow"
	}
	row := storage.EgressLog{
		Timestamp:      req.StartedAt,
		CorrelationID:  req.CorrelationID,
		Provider:       req.Provider,
		Model:          resp.Model,
		Method:         req.Method,
		Protocol:       req.Protocol,
		Destination:    req.Host,
		StatusCode:     resp.StatusCode,
		RequestSize:    resp.RequestSize,
		ResponseSize:   resp.ResponseSize,
		LatencyMs:      resp.LatencyMs,
		ContentClass:   resp.ContentClass,
		PromptHash:     resp.PromptHash,
		PIIDetected:    resp.PIIDetected,
		PIIScrubbed:    resp.PIIScrubbed,
		PolicyAction:   action,
		PolicyRule:     req.PolicyRule,
		AIGeneratedTag: resp.AIGenerated,
		ErrorDetail:    resp.ErrorDetail,
	}

	row = m.chain.ComputeRow(row)

	if m.policyMode == "block" {
		if _, err := m.writer.EnqueueSync(ctx, row); err != nil {
			m.logger.Error("egress_log fail-closed write failed",
				slog.String("destination", row.Destination),
				slog.String("err", err.Error()))
			return resp, fmt.Errorf("%w: %v", egress.ErrLogWriteFailed, err)
		}
		return resp, nil
	}

	// warn mode: fire-and-forget but still backpressure on buffer pressure.
	if err := m.writer.Enqueue(ctx, row); err != nil {
		m.logger.Warn("egress_log enqueue failed",
			slog.String("destination", row.Destination),
			slog.String("err", err.Error()))
	}
	return resp, nil
}

// Close gracefully shuts down the writer. Note: ownership is caller-side;
// egress.BuildEgressChain calls Close via the registry cleanup mechanism
// only for middlewares that own the writer. Our writer is shared so we
// deliberately leave Close as a no-op here.
func (*EgressLogMiddleware) Close() {}

// RegisterEgressMiddlewares hooks compliance middlewares into the egress
// registry. Callers invoke this from main once the shared LogWriter and
// HashChain are constructed.
func RegisterEgressMiddlewares() {
	egress.Register("egress_log", func(entry config.MiddlewareEntry, deps egress.EgressDependencies) (egress.EgressMiddleware, error) {
		if deps.LogWriter == nil {
			return nil, fmt.Errorf("egress_log requires LogWriter in dependencies")
		}
		if deps.HashChain == nil {
			return nil, fmt.Errorf("egress_log requires HashChain in dependencies")
		}
		lw, ok := deps.LogWriter.(*LogWriter)
		if !ok {
			return nil, fmt.Errorf("egress_log: LogWriter type mismatch (got %T)", deps.LogWriter)
		}
		hc, ok := deps.HashChain.(*HashChain)
		if !ok {
			return nil, fmt.Errorf("egress_log: HashChain type mismatch (got %T)", deps.HashChain)
		}
		return NewEgressLogMiddleware(lw, hc, deps.Cfg.PolicyMode, deps.Logger), nil
	})
	registerPolicy()
	registerCompliance()
}
