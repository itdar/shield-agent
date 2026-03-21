package logging

import (
	"log/slog"
	"os"
	"strings"

	"rua/internal/config"
)

// InitLogger creates and returns a *slog.Logger configured from cfg.
// The returned logger writes to stderr.
func InitLogger(cfg config.LoggingConfig) *slog.Logger {
	level := parseLevel(cfg.Level)

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if strings.ToLower(cfg.Format) == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}

	return slog.New(handler)
}

// WithComponent returns a child logger that includes a "component" field in
// every log record it produces.
func WithComponent(logger *slog.Logger, name string) *slog.Logger {
	return logger.With(slog.String("component", name))
}

// WithRequestID returns a child logger that includes a "request_id" field.
func WithRequestID(logger *slog.Logger, id string) *slog.Logger {
	return logger.With(slog.String("request_id", id))
}

// WithAgentID returns a child logger that includes an "agent_id" field.
func WithAgentID(logger *slog.Logger, id string) *slog.Logger {
	return logger.With(slog.String("agent_id", id))
}

// WithMethod returns a child logger that includes a "method" field.
func WithMethod(logger *slog.Logger, method string) *slog.Logger {
	return logger.With(slog.String("method", method))
}

// parseLevel converts a level string (debug/info/warn/error) to slog.Level.
// Unknown values default to slog.LevelInfo.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
