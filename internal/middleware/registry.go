package middleware

import (
	"fmt"
	"log/slog"

	"github.com/itdar/shield-agent/internal/auth"
	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/monitor"
	"github.com/itdar/shield-agent/internal/storage"
	"github.com/itdar/shield-agent/internal/telemetry"
)

// Dependencies holds shared resources needed by middleware factories.
type Dependencies struct {
	DB       *storage.DB
	Logger   *slog.Logger
	Metrics  *monitor.Metrics
	KeyStore auth.KeyStore
	TelCol   *telemetry.Collector
	SecMode  string // "open" or "closed"
}

// BuildChain creates a middleware Chain from config entries and dependencies.
func BuildChain(entries []config.MiddlewareEntry, deps Dependencies) (*Chain, func(), error) {
	var items []Middleware
	var closers []func()

	for _, entry := range entries {
		if entry.Enabled != nil && !*entry.Enabled {
			deps.Logger.Info("middleware disabled", slog.String("name", entry.Name))
			continue
		}

		mw, closer, err := createMiddleware(entry, deps)
		if err != nil {
			// Close any already-created closers.
			for _, c := range closers {
				c()
			}
			return nil, nil, fmt.Errorf("creating middleware %q: %w", entry.Name, err)
		}
		items = append(items, mw)
		if closer != nil {
			closers = append(closers, closer)
		}
		deps.Logger.Info("middleware enabled", slog.String("name", entry.Name))
	}

	closeAll := func() {
		for _, c := range closers {
			c()
		}
	}

	return NewChain(items...), closeAll, nil
}

func createMiddleware(entry config.MiddlewareEntry, deps Dependencies) (Middleware, func(), error) {
	switch entry.Name {
	case "auth":
		mw := NewAuthMiddleware(deps.KeyStore, deps.SecMode, deps.Logger, func(status string) {
			deps.Metrics.AuthTotal.WithLabelValues(status).Inc()
		})
		return mw, nil, nil
	case "guard":
		cfg := GuardConfig{}
		if v, ok := entry.Config["rate_limit_per_min"]; ok {
			switch n := v.(type) {
			case int:
				cfg.RateLimitPerMin = n
			case float64:
				cfg.RateLimitPerMin = int(n)
			}
		}
		if v, ok := entry.Config["max_body_size"]; ok {
			switch n := v.(type) {
			case int:
				cfg.MaxBodySize = int64(n)
			case float64:
				cfg.MaxBodySize = int64(n)
			case int64:
				cfg.MaxBodySize = n
			}
		}
		if v, ok := entry.Config["ip_blocklist"]; ok {
			if list, ok := v.([]any); ok {
				for _, item := range list {
					if s, ok := item.(string); ok {
						cfg.IPBlocklist = append(cfg.IPBlocklist, s)
					}
				}
			}
		}
		if v, ok := entry.Config["ip_allowlist"]; ok {
			if list, ok := v.([]any); ok {
				for _, item := range list {
					if s, ok := item.(string); ok {
						cfg.IPAllowlist = append(cfg.IPAllowlist, s)
					}
				}
			}
		}
		if v, ok := entry.Config["brute_force_max_fails"]; ok {
			switch n := v.(type) {
			case int:
				cfg.BruteForceMaxFails = n
			case float64:
				cfg.BruteForceMaxFails = int(n)
			}
		}
		if v, ok := entry.Config["validate_jsonrpc"]; ok {
			if b, ok := v.(bool); ok {
				cfg.ValidateJSONRPC = b
			}
		}
		mw := NewGuardMiddleware(cfg, deps.Logger, func() {
			if deps.Metrics != nil {
				deps.Metrics.RateLimitRejected.WithLabelValues("unknown").Inc()
			}
		})
		return mw, nil, nil
	case "log":
		onMsg := func(direction, method string, latencyMs float64) {
			deps.Metrics.MessagesTotal.WithLabelValues(direction, method).Inc()
			if direction == "out" && latencyMs > 0 {
				deps.Metrics.MessageLatency.WithLabelValues(method).Observe(latencyMs / 1000.0)
			}
		}
		mw := NewLogMiddleware(deps.DB, deps.Logger, deps.TelCol, onMsg)
		return mw, func() { mw.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("unknown middleware: %q", entry.Name)
	}
}
