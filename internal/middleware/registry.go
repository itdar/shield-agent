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
	case "log":
		mw := NewLogMiddleware(deps.DB, deps.Logger, deps.TelCol)
		return mw, func() { mw.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("unknown middleware: %q", entry.Name)
	}
}
