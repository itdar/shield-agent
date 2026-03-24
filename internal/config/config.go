package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// MiddlewareEntry describes a single middleware in the pipeline.
type MiddlewareEntry struct {
	Name    string         `yaml:"name"`
	Enabled *bool          `yaml:"enabled"` // pointer so we can detect omission, default true
	Config  map[string]any `yaml:"config,omitempty"`
}

// Config is the top-level configuration structure.
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Security    SecurityConfig    `yaml:"security"`
	Logging     LoggingConfig     `yaml:"logging"`
	Telemetry   TelemetryConfig   `yaml:"telemetry"`
	Storage     StorageConfig     `yaml:"storage"`
	Middlewares []MiddlewareEntry `yaml:"middlewares,omitempty"`
}

// ServerConfig holds HTTP monitoring server settings.
type ServerConfig struct {
	MonitorAddr        string   `yaml:"monitor_addr"`
	TLSCert            string   `yaml:"tls_cert"`
	TLSKey             string   `yaml:"tls_key"`
	CORSAllowedOrigins []string `yaml:"cors_allowed_origins"`
}

// SecurityConfig controls authentication/authorization behavior.
type SecurityConfig struct {
	Mode         string `yaml:"mode"`          // "open" or "closed"
	KeyStorePath string `yaml:"key_store_path"` // path to keys.yaml
}

// LoggingConfig controls structured log output.
type LoggingConfig struct {
	Level  string `yaml:"level"`  // debug/info/warn/error
	Format string `yaml:"format"` // json/text
}

// TelemetryConfig controls anonymous usage telemetry.
type TelemetryConfig struct {
	Enabled       bool    `yaml:"enabled"`
	Endpoint      string  `yaml:"endpoint"`
	BatchInterval int     `yaml:"batch_interval"` // seconds
	Epsilon       float64 `yaml:"epsilon"`        // differential privacy epsilon
}

// StorageConfig controls local SQLite storage.
type StorageConfig struct {
	DBPath        string `yaml:"db_path"`
	RetentionDays int    `yaml:"retention_days"`
}

// boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool { return &b }

// Defaults returns a Config populated with all default values.
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			MonitorAddr:        "127.0.0.1:9090",
			CORSAllowedOrigins: []string{"*"},
		},
		Security: SecurityConfig{
			Mode:         "open",
			KeyStorePath: "keys.yaml",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Telemetry: TelemetryConfig{
			Enabled:       false,
			Endpoint:      "http://localhost:8080",
			BatchInterval: 60,
			Epsilon:       1.0,
		},
		Storage: StorageConfig{
			DBPath:        "shield-agent.db",
			RetentionDays: 30,
		},
		Middlewares: []MiddlewareEntry{
			{Name: "auth", Enabled: boolPtr(true)},
			{Name: "guard", Enabled: boolPtr(true)},
			{Name: "log", Enabled: boolPtr(true)},
		},
	}
}

// Load reads a config file (if path is non-empty), applies environment
// variable overrides, then applies any explicit CLI overrides.
//
// Priority: cliOverrides > env vars > file > defaults
func Load(path string, cliOverrides map[string]string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		if err := loadFile(path, &cfg); err != nil {
			return cfg, fmt.Errorf("loading config file %q: %w", path, err)
		}
	}

	applyEnv(&cfg)

	if err := applyCLI(&cfg, cliOverrides); err != nil {
		return cfg, fmt.Errorf("applying CLI flags: %w", err)
	}

	if err := Validate(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// loadFile parses a YAML config file into cfg, merging on top of existing values.
func loadFile(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// A missing file is not an error — the caller may pass a default path.
			return nil
		}
		return err
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("YAML decode error: %w", err)
	}
	return nil
}

// applyEnv overrides cfg fields from SHIELD_AGENT_* environment variables.
func applyEnv(cfg *Config) {
	if v := os.Getenv("SHIELD_AGENT_MONITOR_ADDR"); v != "" {
		cfg.Server.MonitorAddr = v
	}
	if v := os.Getenv("SHIELD_AGENT_TLS_CERT"); v != "" {
		cfg.Server.TLSCert = v
	}
	if v := os.Getenv("SHIELD_AGENT_TLS_KEY"); v != "" {
		cfg.Server.TLSKey = v
	}
	if v := os.Getenv("SHIELD_AGENT_SECURITY_MODE"); v != "" {
		cfg.Security.Mode = v
	}
	if v := os.Getenv("SHIELD_AGENT_KEY_STORE_PATH"); v != "" {
		cfg.Security.KeyStorePath = v
	}
	if v := os.Getenv("SHIELD_AGENT_LOG_LEVEL"); v != "" {
		cfg.Logging.Level = v
	}
	if v := os.Getenv("SHIELD_AGENT_LOG_FORMAT"); v != "" {
		cfg.Logging.Format = v
	}
	if v := os.Getenv("SHIELD_AGENT_TELEMETRY_ENABLED"); v != "" {
		cfg.Telemetry.Enabled = parseBool(v, cfg.Telemetry.Enabled)
	}
	if v := os.Getenv("SHIELD_AGENT_TELEMETRY_ENDPOINT"); v != "" {
		cfg.Telemetry.Endpoint = v
	}
	if v := os.Getenv("SHIELD_AGENT_TELEMETRY_BATCH_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Telemetry.BatchInterval = n
		}
	}
	if v := os.Getenv("SHIELD_AGENT_TELEMETRY_EPSILON"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Telemetry.Epsilon = f
		}
	}
	if v := os.Getenv("SHIELD_AGENT_DB_PATH"); v != "" {
		cfg.Storage.DBPath = v
	}
	if v := os.Getenv("SHIELD_AGENT_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Storage.RetentionDays = n
		}
	}
}

// applyCLI merges explicit CLI key/value overrides into cfg.
// Recognized keys: monitor-addr, log-level, telemetry, disable-middleware, enable-middleware.
func applyCLI(cfg *Config, overrides map[string]string) error {
	for k, v := range overrides {
		switch k {
		case "monitor-addr":
			cfg.Server.MonitorAddr = v
		case "log-level":
			cfg.Logging.Level = v
		case "telemetry":
			cfg.Telemetry.Enabled = parseBool(v, cfg.Telemetry.Enabled)
		case "disable-middleware":
			SetMiddlewareEnabled(cfg, v, false)
		case "enable-middleware":
			SetMiddlewareEnabled(cfg, v, true)
		// "verbose" and "config" are consumed by the CLI layer, not stored here.
		default:
			return fmt.Errorf("unknown CLI override key %q", k)
		}
	}
	return nil
}

// SetMiddlewareEnabled enables or disables a named middleware entry.
// If no entry with the given name exists, a new one is appended.
func SetMiddlewareEnabled(cfg *Config, name string, enabled bool) {
	for i := range cfg.Middlewares {
		if cfg.Middlewares[i].Name == name {
			cfg.Middlewares[i].Enabled = boolPtr(enabled)
			return
		}
	}
	cfg.Middlewares = append(cfg.Middlewares, MiddlewareEntry{Name: name, Enabled: boolPtr(enabled)})
}

// Validate checks cfg for semantic correctness and returns a descriptive error
// if any constraint is violated.
func Validate(cfg *Config) error {
	// Security mode
	switch cfg.Security.Mode {
	case "open", "closed":
	default:
		return fmt.Errorf("security.mode must be \"open\" or \"closed\", got %q", cfg.Security.Mode)
	}

	// Log level
	switch strings.ToLower(cfg.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be one of debug/info/warn/error, got %q", cfg.Logging.Level)
	}

	// Log format
	switch strings.ToLower(cfg.Logging.Format) {
	case "json", "text":
	default:
		return fmt.Errorf("logging.format must be \"json\" or \"text\", got %q", cfg.Logging.Format)
	}

	// Telemetry epsilon must be positive
	if cfg.Telemetry.Epsilon <= 0 {
		return fmt.Errorf("telemetry.epsilon must be > 0, got %v", cfg.Telemetry.Epsilon)
	}

	// Batch interval must be positive
	if cfg.Telemetry.BatchInterval <= 0 {
		return fmt.Errorf("telemetry.batch_interval must be > 0 seconds, got %d", cfg.Telemetry.BatchInterval)
	}

	// Retention days must be positive
	if cfg.Storage.RetentionDays <= 0 {
		return fmt.Errorf("storage.retention_days must be > 0, got %d", cfg.Storage.RetentionDays)
	}

	// monitor_addr must be non-empty
	if strings.TrimSpace(cfg.Server.MonitorAddr) == "" {
		return fmt.Errorf("server.monitor_addr must not be empty")
	}

	return nil
}

// parseBool parses a string as a boolean, returning fallback on parse failure.
func parseBool(s string, fallback bool) bool {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fallback
	}
	return v
}
