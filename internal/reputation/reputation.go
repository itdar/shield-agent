// Package reputation provides agent trust scoring based on historical behavior.
package reputation

import (
	"context"
	"time"
)

// TrustLevel represents the categorical trust classification.
type TrustLevel string

const (
	TrustTrusted    TrustLevel = "trusted"
	TrustNormal     TrustLevel = "normal"
	TrustSuspicious TrustLevel = "suspicious"
	TrustBlocked    TrustLevel = "blocked"
)

// Score holds the computed reputation for a single agent.
type Score struct {
	AgentIDHash    string     `json:"agent_id_hash"`
	TrustLevel     TrustLevel `json:"trust_level"`
	TrustScore     float64    `json:"trust_score"` // 0.0 - 1.0
	SuccessRate    float64    `json:"success_rate"`
	ErrorRate      float64    `json:"error_rate"`
	AvgLatencyMs   float64    `json:"avg_latency_ms"`
	RequestCount   int        `json:"request_count"`
	RateLimitHits  int        `json:"rate_limit_hits"`
	AuthFailures   int        `json:"auth_failures"`
	FirstSeen      time.Time  `json:"first_seen"`
	LastSeen       time.Time  `json:"last_seen"`
	ComputedAt     time.Time  `json:"computed_at"`
	WindowDuration string     `json:"window_duration"`
	Source         string     `json:"source"` // "local" or "remote"
}

// Provider is the core abstraction for reputation lookups.
// Implementations: LocalProvider (Phase A), future RemoteProvider (Phase B+).
type Provider interface {
	// GetScore returns the current reputation score for an agent.
	// Returns nil, nil if the agent has no history.
	GetScore(ctx context.Context, agentIDHash string) (*Score, error)

	// GetRateMultiplier returns the rate limit multiplier for an agent.
	// This is the hot path — must be fast (cached, no SQL per call).
	// Returns 1.0 for unknown agents, 0.0 for blocked agents.
	GetRateMultiplier(ctx context.Context, agentIDHash string) float64

	// ListScores returns all known agent scores.
	ListScores(ctx context.Context) ([]Score, error)

	// Stats returns aggregate reputation statistics.
	Stats(ctx context.Context) (*AggregateStats, error)
}

// Reporter accepts reputation data from external sources (Phase B).
type Reporter interface {
	ReportScores(ctx context.Context, instanceID string, scores []Score) (int, error)
}

// AggregateStats holds summary statistics across all agents.
type AggregateStats struct {
	TotalAgents     int            `json:"total_agents"`
	ByTrustLevel    map[string]int `json:"by_trust_level"`
	AvgTrustScore   float64        `json:"avg_trust_score"`
	LastRecalcAt    time.Time      `json:"last_recalc_at"`
}
