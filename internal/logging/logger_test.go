package logging

import (
	"log/slog"
	"testing"

	"github.com/itdar/shield-agent/internal/config"
)

// TestInitLoggerJSONHandler verifies that InitLogger creates a logger with
// JSON format handler and doesn't panic.
func TestInitLoggerJSONHandler(t *testing.T) {
	cfg := config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}

	logger := InitLogger(cfg)
	if logger == nil {
		t.Fatal("expected non-nil logger, got nil")
	}

	// Test that we can write a log without panic
	logger.Info("test message", slog.String("test_key", "test_value"))
}

// TestInitLoggerTextHandler verifies that InitLogger creates a logger with
// text format handler and doesn't panic.
func TestInitLoggerTextHandler(t *testing.T) {
	cfg := config.LoggingConfig{
		Level:  "debug",
		Format: "text",
	}

	logger := InitLogger(cfg)
	if logger == nil {
		t.Fatal("expected non-nil logger, got nil")
	}

	// Test that we can write a log without panic
	logger.Debug("test message", slog.String("test_key", "test_value"))
}

// TestParseLevel verifies that parseLevel correctly converts level strings
// to slog.Level values, and defaults to info for unknown values.
func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG", slog.LevelDebug},   // case-insensitive
		{"Info", slog.LevelInfo},     // case-insensitive
		{"WARN", slog.LevelWarn},     // case-insensitive
		{"ERROR", slog.LevelError},   // case-insensitive
		{"unknown", slog.LevelInfo},  // unknown defaults to info
		{"", slog.LevelInfo},         // empty defaults to info
		{"fatal", slog.LevelInfo},    // unknown defaults to info
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseLevel(tt.input)
			if result != tt.expected {
				t.Errorf("parseLevel(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestWithFields verifies that the With* functions return non-nil loggers.
func TestWithFields(t *testing.T) {
	cfg := config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}
	baseLogger := InitLogger(cfg)

	t.Run("WithComponent", func(t *testing.T) {
		logger := WithComponent(baseLogger, "test-component")
		if logger == nil {
			t.Fatal("expected non-nil logger from WithComponent")
		}
	})

	t.Run("WithRequestID", func(t *testing.T) {
		logger := WithRequestID(baseLogger, "req-123")
		if logger == nil {
			t.Fatal("expected non-nil logger from WithRequestID")
		}
	})

	t.Run("WithAgentID", func(t *testing.T) {
		logger := WithAgentID(baseLogger, "agent-456")
		if logger == nil {
			t.Fatal("expected non-nil logger from WithAgentID")
		}
	})

	t.Run("WithMethod", func(t *testing.T) {
		logger := WithMethod(baseLogger, "GET")
		if logger == nil {
			t.Fatal("expected non-nil logger from WithMethod")
		}
	})
}

// TestWithFieldsChaining verifies that With* functions can be chained.
func TestWithFieldsChaining(t *testing.T) {
	cfg := config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}
	baseLogger := InitLogger(cfg)

	logger := WithComponent(baseLogger, "comp")
	logger = WithRequestID(logger, "req")
	logger = WithAgentID(logger, "agent")
	logger = WithMethod(logger, "POST")

	if logger == nil {
		t.Fatal("expected non-nil logger after chaining")
	}

	// Verify chained logger can still log without panic
	logger.Info("chained log message", slog.String("key", "value"))
}
