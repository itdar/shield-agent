package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/itdar/shield-agent/internal/reputation"
	"gopkg.in/yaml.v3"
)

// MiddlewareEntry describes a single middleware in the pipeline.
type MiddlewareEntry struct {
	Name    string         `yaml:"name"`
	Enabled *bool          `yaml:"enabled"` // pointer so we can detect omission, default true
	Config  map[string]any `yaml:"config,omitempty"`
}

// UpstreamMatch defines how incoming requests are matched to an upstream.
type UpstreamMatch struct {
	Host        string `yaml:"host"`
	PathPrefix  string `yaml:"path_prefix"`
	StripPrefix bool   `yaml:"strip_prefix"`
}

// UpstreamConfig describes a single upstream target for gateway mode.
type UpstreamConfig struct {
	Name          string        `yaml:"name"`
	URL           string        `yaml:"url"`
	Match         UpstreamMatch `yaml:"match"`
	Transport     string        `yaml:"transport"`        // "sse" or "streamable-http"
	Protocol      string        `yaml:"protocol"`         // "auto", "mcp", "a2a", "http" (default: auto)
	TLSSkipVerify bool          `yaml:"tls_skip_verify"`  // skip upstream cert verification
	TLSClientCert string        `yaml:"tls_client_cert"`  // mTLS client cert path
	TLSClientKey  string        `yaml:"tls_client_key"`   // mTLS client key path
}

// Config is the top-level configuration structure.
//
// Rollback note: The Egress field uses yaml:"egress,omitempty" with KnownFields(true).
// Config files authored against this version with an "egress:" section will fail to
// parse under an older shield-agent binary. When downgrading, remove the egress:
// section from the YAML before starting the older binary.
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Security    SecurityConfig    `yaml:"security"`
	Logging     LoggingConfig     `yaml:"logging"`
	Telemetry   TelemetryConfig   `yaml:"telemetry"`
	Storage     StorageConfig     `yaml:"storage"`
	Reputation  reputation.Config `yaml:"reputation,omitempty"`
	Middlewares []MiddlewareEntry `yaml:"middlewares,omitempty"`
	Upstreams   []UpstreamConfig  `yaml:"upstreams,omitempty"`
	Egress      EgressConfig      `yaml:"egress,omitempty"`
}

// EgressConfig controls the forward-proxy egress mode.
// Phase 1 uses metadata-only interception (CONNECT tunneling, no TLS MITM).
// Phase 2 fields (MITM, PII scrub, content tagging) are schema-only in this version
// and get honoured once the corresponding middleware is enabled.
type EgressConfig struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"`

	// Phase 1: when non-empty, only these destination hosts are permitted.
	// Empty means allow all and log metadata.
	UpstreamAllow []string `yaml:"upstream_allow,omitempty"`

	// PolicyMode: "warn" (record only) or "block" (reject on violation, fail-closed on log error).
	PolicyMode string `yaml:"policy_mode"`

	// RetentionDays overrides storage.retention_days for egress_logs.
	// Zero means fall back to StorageConfig.RetentionDays.
	RetentionDays int `yaml:"retention_days,omitempty"`

	HashChain HashChainConfig `yaml:"hash_chain,omitempty"`

	// --- Phase 2 fields ---

	MITMHosts           []string `yaml:"mitm_hosts,omitempty"`
	TLSPassthroughHosts []string `yaml:"tls_passthrough_hosts,omitempty"`
	CACert              string   `yaml:"ca_cert,omitempty"`
	CAKey               string   `yaml:"ca_key,omitempty"`
	CAAutoGenerate      bool     `yaml:"ca_auto_generate,omitempty"`
	CAValidityDays      int      `yaml:"ca_validity_days,omitempty"`
	LeafCacheTTLMin     int      `yaml:"leaf_cache_ttl_min,omitempty"`

	PIIScrub       PIIScrubConfig       `yaml:"pii_scrub,omitempty"`
	ContentTagging ContentTaggingConfig `yaml:"content_tagging,omitempty"`

	// LogFullBody: when true, store request/response body verbatim instead of a hash.
	// High PII-exposure risk; keep false unless legal review approves.
	LogFullBody bool `yaml:"log_full_body,omitempty"`

	// Middlewares pipeline (empty means use Defaults).
	Middlewares []MiddlewareEntry `yaml:"middlewares,omitempty"`
}

// HashChainConfig controls tamper-evident logging.
type HashChainConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Algorithm string `yaml:"algorithm,omitempty"` // only "sha256" supported in Phase 1
}

// PIIScrubConfig (Phase 2).
type PIIScrubConfig struct {
	Enabled        bool     `yaml:"enabled"`
	CustomPatterns []string `yaml:"custom_patterns,omitempty"`
	RedactionMode  string   `yaml:"redaction_mode,omitempty"` // "mask" | "hash"
}

// ContentTaggingConfig (Phase 2).
type ContentTaggingConfig struct {
	Enabled      bool `yaml:"enabled"`
	InjectHeader bool `yaml:"inject_header,omitempty"`
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
	Mode         string   `yaml:"mode"`           // "open", "verified", or "closed"
	KeyStorePath string   `yaml:"key_store_path"` // path to keys.yaml
	DIDBlocklist []string `yaml:"did_blocklist"`  // blocked DID identifiers
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
		Reputation: reputation.DefaultConfig(),
		Middlewares: []MiddlewareEntry{
			{Name: "auth", Enabled: boolPtr(true)},
			{Name: "guard", Enabled: boolPtr(true)},
			{Name: "log", Enabled: boolPtr(true)},
		},
		Egress: defaultEgress(),
	}
}

// defaultEgress returns the default EgressConfig (disabled Phase 1 metadata-only proxy).
// Listen defaults to loopback so a fresh install is not an open HTTP relay.
// Operators who want network-wide access must set egress.listen to 0.0.0.0:PORT explicitly.
func defaultEgress() EgressConfig {
	return EgressConfig{
		Enabled:    false,
		Listen:     "127.0.0.1:8889",
		PolicyMode: "warn",
		HashChain: HashChainConfig{
			Enabled:   true,
			Algorithm: "sha256",
		},
		CAValidityDays:  3650,
		LeafCacheTTLMin: 60,
		PIIScrub: PIIScrubConfig{
			Enabled:       true,
			RedactionMode: "mask",
		},
		ContentTagging: ContentTaggingConfig{
			Enabled: true,
		},
		Middlewares: []MiddlewareEntry{
			{Name: "policy", Enabled: boolPtr(true)},
			{Name: "egress_log", Enabled: boolPtr(true)},
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
	if v := os.Getenv("SHIELD_AGENT_REPUTATION_ENABLED"); v != "" {
		cfg.Reputation.Enabled = parseBool(v, cfg.Reputation.Enabled)
	}
	if v := os.Getenv("SHIELD_AGENT_REPUTATION_RECALC_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Reputation.RecalcInterval = n
		}
	}
	if v := os.Getenv("SHIELD_AGENT_REPUTATION_WINDOW_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Reputation.WindowHours = n
		}
	}
	if v := os.Getenv("SHIELD_AGENT_REPUTATION_CACHE_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Reputation.CacheTTL = n
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
	case "open", "verified", "closed":
	default:
		return fmt.Errorf("security.mode must be one of open/verified/closed, got %q", cfg.Security.Mode)
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

	// Upstreams validation
	names := make(map[string]bool)
	for _, u := range cfg.Upstreams {
		if u.Name == "" {
			return fmt.Errorf("upstream name must not be empty")
		}
		if u.URL == "" {
			return fmt.Errorf("upstream %q: url must not be empty", u.Name)
		}
		if names[u.Name] {
			return fmt.Errorf("duplicate upstream name %q", u.Name)
		}
		names[u.Name] = true
		if u.Transport != "" && u.Transport != "sse" && u.Transport != "streamable-http" {
			return fmt.Errorf("upstream %q: transport must be sse or streamable-http, got %q", u.Name, u.Transport)
		}
		if u.Protocol != "" {
			switch u.Protocol {
			case "auto", "mcp", "a2a", "http":
			default:
				return fmt.Errorf("upstream %q: protocol must be auto/mcp/a2a/http, got %q", u.Name, u.Protocol)
			}
		}
	}

	if err := validateEgress(&cfg.Egress); err != nil {
		return err
	}

	return nil
}

// validateEgress checks EgressConfig semantics when enabled.
func validateEgress(e *EgressConfig) error {
	if !e.Enabled {
		return nil
	}
	if strings.TrimSpace(e.Listen) == "" {
		return fmt.Errorf("egress.listen must not be empty when egress is enabled")
	}
	switch e.PolicyMode {
	case "warn", "block":
	default:
		return fmt.Errorf("egress.policy_mode must be warn or block, got %q", e.PolicyMode)
	}
	if e.RetentionDays < 0 {
		return fmt.Errorf("egress.retention_days must be >= 0, got %d", e.RetentionDays)
	}
	if e.HashChain.Enabled && e.HashChain.Algorithm != "" && e.HashChain.Algorithm != "sha256" {
		return fmt.Errorf("egress.hash_chain.algorithm only supports \"sha256\" in Phase 1, got %q", e.HashChain.Algorithm)
	}
	if e.PIIScrub.Enabled && e.PIIScrub.RedactionMode != "" {
		switch e.PIIScrub.RedactionMode {
		case "mask", "hash":
		default:
			return fmt.Errorf("egress.pii_scrub.redaction_mode must be mask or hash, got %q", e.PIIScrub.RedactionMode)
		}
	}
	return nil
}

// ApplyDBOverrides merges admin_config key-value overrides into cfg.
// Currently supports middleware_enabled_{name} keys.
func ApplyDBOverrides(cfg *Config, overrides map[string]string) {
	for k, v := range overrides {
		if strings.HasPrefix(k, "middleware_enabled_") {
			name := strings.TrimPrefix(k, "middleware_enabled_")
			enabled := v == "true"
			SetMiddlewareEnabled(cfg, name, enabled)
		}
	}
}

// parseBool parses a string as a boolean, returning fallback on parse failure.
func parseBool(s string, fallback bool) bool {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fallback
	}
	return v
}
