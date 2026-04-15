package reputation

// Config holds reputation system configuration.
type Config struct {
	Enabled        bool               `yaml:"enabled"`
	RecalcInterval int                `yaml:"recalc_interval"` // seconds
	WindowHours    int                `yaml:"window_hours"`
	Weights        ScoreWeights       `yaml:"weights"`
	Thresholds     Thresholds         `yaml:"thresholds"`
	RateMultipliers map[string]float64 `yaml:"rate_multipliers"`
	CacheTTL       int                `yaml:"cache_ttl"` // seconds
	API            APIConfig          `yaml:"api"`
}

// ScoreWeights controls how different factors contribute to the trust score.
type ScoreWeights struct {
	SuccessRate  float64 `yaml:"success_rate"`
	ErrorPenalty float64 `yaml:"error_penalty"`
	Latency      float64 `yaml:"latency"`
	Volume       float64 `yaml:"volume"`
	AuthFailure  float64 `yaml:"auth_failure"`
	RateLimit    float64 `yaml:"rate_limit"`
}

// Thresholds defines the score boundaries for each trust level.
type Thresholds struct {
	Trusted    float64 `yaml:"trusted"`
	Normal     float64 `yaml:"normal"`
	Suspicious float64 `yaml:"suspicious"`
}

// APIConfig controls the shared reputation API (Phase B).
type APIConfig struct {
	Enabled      bool     `yaml:"enabled"`
	Peers        []string `yaml:"peers"`
	RemoteWeight float64  `yaml:"remote_weight"`
	Epsilon      float64  `yaml:"epsilon"`
}

// DefaultConfig returns sensible defaults for the reputation system.
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		RecalcInterval: 300, // 5 minutes
		WindowHours:    24,
		Weights: ScoreWeights{
			SuccessRate:  0.35,
			ErrorPenalty: 0.25,
			Latency:      0.10,
			Volume:       0.10,
			AuthFailure:  0.15,
			RateLimit:    0.05,
		},
		Thresholds: Thresholds{
			Trusted:    0.8,
			Normal:     0.4,
			Suspicious: 0.1,
		},
		RateMultipliers: map[string]float64{
			"trusted":    2.0,
			"normal":     1.0,
			"suspicious": 0.25,
			"blocked":    0.0,
		},
		CacheTTL: 60,
		API: APIConfig{
			Enabled:      false,
			RemoteWeight: 0.3,
			Epsilon:      1.0,
		},
	}
}
