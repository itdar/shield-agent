package config

import (
	"os"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Security.Mode != "open" {
		t.Errorf("expected mode=open, got %q", cfg.Security.Mode)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("expected level=info, got %q", cfg.Logging.Level)
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("expected format=json, got %q", cfg.Logging.Format)
	}
	if cfg.Telemetry.BatchInterval != 60 {
		t.Errorf("expected batch_interval=60, got %d", cfg.Telemetry.BatchInterval)
	}
	if cfg.Telemetry.Epsilon != 1.0 {
		t.Errorf("expected epsilon=1.0, got %v", cfg.Telemetry.Epsilon)
	}
	if cfg.Storage.RetentionDays != 30 {
		t.Errorf("expected retention_days=30, got %d", cfg.Storage.RetentionDays)
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/mcp-shield.yaml", nil)
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	// Should get defaults.
	if cfg.Security.Mode != "open" {
		t.Errorf("expected default mode=open, got %q", cfg.Security.Mode)
	}
}

func TestEnvVarOverride(t *testing.T) {
	t.Setenv("MCP_SHIELD_SECURITY_MODE", "closed")
	t.Setenv("MCP_SHIELD_LOG_LEVEL", "debug")

	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Security.Mode != "closed" {
		t.Errorf("expected mode=closed, got %q", cfg.Security.Mode)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("expected level=debug, got %q", cfg.Logging.Level)
	}

	// Clean up.
	os.Unsetenv("MCP_SHIELD_SECURITY_MODE")
	os.Unsetenv("MCP_SHIELD_LOG_LEVEL")
}

func TestCLIOverridePriority(t *testing.T) {
	t.Setenv("MCP_SHIELD_LOG_LEVEL", "warn")
	defer os.Unsetenv("MCP_SHIELD_LOG_LEVEL")

	cfg, err := Load("", map[string]string{"log-level": "error"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// CLI should win over env.
	if cfg.Logging.Level != "error" {
		t.Errorf("expected level=error (CLI wins), got %q", cfg.Logging.Level)
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"bad_mode", func(c *Config) { c.Security.Mode = "strict" }, true},
		{"bad_level", func(c *Config) { c.Logging.Level = "verbose" }, true},
		{"bad_format", func(c *Config) { c.Logging.Format = "xml" }, true},
		{"bad_epsilon", func(c *Config) { c.Telemetry.Epsilon = -1.0 }, true},
		{"bad_batch_interval", func(c *Config) { c.Telemetry.BatchInterval = 0 }, true},
		{"bad_retention_days", func(c *Config) { c.Storage.RetentionDays = 0 }, true},
		{"valid", func(c *Config) {}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			tt.mutate(&cfg)
			err := Validate(&cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
