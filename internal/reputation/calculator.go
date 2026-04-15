package reputation

import (
	"database/sql"
	"fmt"
	"time"
)

// agentStats holds raw statistics queried from action_logs.
type agentStats struct {
	AgentIDHash   string
	RequestCount  int
	SuccessCount  int
	ErrorCount    int
	AvgLatencyMs  float64
	FirstSeen     time.Time
	LastSeen      time.Time
	AuthFailures  int
	RateLimitHits int
}

// calculator computes trust scores from action_logs data.
type calculator struct {
	db          *sql.DB
	weights     ScoreWeights
	thresholds  Thresholds
	windowHours int
}

func newCalculator(db *sql.DB, weights ScoreWeights, thresholds Thresholds, windowHours int) *calculator {
	return &calculator{
		db:          db,
		weights:     weights,
		thresholds:  thresholds,
		windowHours: windowHours,
	}
}

// recalculateAll queries action_logs and computes scores for all agents.
func (c *calculator) recalculateAll() ([]Score, error) {
	stats, err := c.queryAllAgentStats()
	if err != nil {
		return nil, fmt.Errorf("querying agent stats: %w", err)
	}

	now := time.Now().UTC()
	scores := make([]Score, 0, len(stats))
	for _, s := range stats {
		score := c.computeScore(s, now)
		scores = append(scores, score)
	}
	return scores, nil
}

// recalculateOne computes the score for a single agent.
func (c *calculator) recalculateOne(agentIDHash string) (*Score, error) {
	s, err := c.queryAgentStats(agentIDHash)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	score := c.computeScore(*s, time.Now().UTC())
	return &score, nil
}

func (c *calculator) computeScore(s agentStats, now time.Time) Score {
	total := float64(s.RequestCount)
	var successRate, errorRate float64
	if total > 0 {
		successRate = float64(s.SuccessCount) / total
		errorRate = float64(s.ErrorCount) / total
	}

	// Base score 0.5 (neutral). Good behavior pushes up, bad pushes down.
	trustScore := 0.5
	trustScore += c.weights.SuccessRate * successRate
	trustScore -= c.weights.ErrorPenalty * errorRate
	trustScore -= c.weights.Latency * clamp(s.AvgLatencyMs/5000.0, 0, 1)
	trustScore += c.weights.Volume * clamp(total/1000.0, 0, 1)
	trustScore -= c.weights.AuthFailure * clamp(float64(s.AuthFailures)/10.0, 0, 1)
	trustScore -= c.weights.RateLimit * clamp(float64(s.RateLimitHits)/20.0, 0, 1)
	trustScore = clamp(trustScore, 0, 1)

	return Score{
		AgentIDHash:    s.AgentIDHash,
		TrustLevel:     classifyTrustLevel(trustScore, c.thresholds),
		TrustScore:     trustScore,
		SuccessRate:    successRate,
		ErrorRate:      errorRate,
		AvgLatencyMs:   s.AvgLatencyMs,
		RequestCount:   s.RequestCount,
		RateLimitHits:  s.RateLimitHits,
		AuthFailures:   s.AuthFailures,
		FirstSeen:      s.FirstSeen,
		LastSeen:       s.LastSeen,
		ComputedAt:     now,
		WindowDuration: fmt.Sprintf("%dh", c.windowHours),
		Source:         "local",
	}
}

func classifyTrustLevel(score float64, t Thresholds) TrustLevel {
	switch {
	case score >= t.Trusted:
		return TrustTrusted
	case score >= t.Normal:
		return TrustNormal
	case score >= t.Suspicious:
		return TrustSuspicious
	default:
		return TrustBlocked
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// queryAllAgentStats aggregates action_logs for all agents within the time window.
func (c *calculator) queryAllAgentStats() ([]agentStats, error) {
	q := `
SELECT
	agent_id_hash,
	COUNT(*) AS request_count,
	SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) AS success_count,
	SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END) AS error_count,
	COALESCE(AVG(latency_ms), 0) AS avg_latency_ms,
	MIN(timestamp) AS first_seen,
	MAX(timestamp) AS last_seen,
	SUM(CASE WHEN auth_status = 'failed' THEN 1 ELSE 0 END) AS auth_failures,
	SUM(CASE WHEN error_code IN ('429', '-32001') THEN 1 ELSE 0 END) AS rate_limit_hits
FROM action_logs
WHERE agent_id_hash != ''
	AND timestamp >= datetime('now', ?)
GROUP BY agent_id_hash`

	window := fmt.Sprintf("-%d hours", c.windowHours)
	rows, err := c.db.Query(q, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAgentStats(rows)
}

// queryAgentStats aggregates action_logs for a single agent.
func (c *calculator) queryAgentStats(agentIDHash string) (*agentStats, error) {
	q := `
SELECT
	agent_id_hash,
	COUNT(*) AS request_count,
	SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) AS success_count,
	SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END) AS error_count,
	COALESCE(AVG(latency_ms), 0) AS avg_latency_ms,
	MIN(timestamp) AS first_seen,
	MAX(timestamp) AS last_seen,
	SUM(CASE WHEN auth_status = 'failed' THEN 1 ELSE 0 END) AS auth_failures,
	SUM(CASE WHEN error_code IN ('429', '-32001') THEN 1 ELSE 0 END) AS rate_limit_hits
FROM action_logs
WHERE agent_id_hash = ?
	AND timestamp >= datetime('now', ?)
GROUP BY agent_id_hash`

	window := fmt.Sprintf("-%d hours", c.windowHours)
	rows, err := c.db.Query(q, agentIDHash, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats, err := scanAgentStats(rows)
	if err != nil {
		return nil, err
	}
	if len(stats) == 0 {
		return nil, nil
	}
	return &stats[0], nil
}

func scanAgentStats(rows *sql.Rows) ([]agentStats, error) {
	var result []agentStats
	for rows.Next() {
		var s agentStats
		var firstSeen, lastSeen string
		if err := rows.Scan(
			&s.AgentIDHash,
			&s.RequestCount,
			&s.SuccessCount,
			&s.ErrorCount,
			&s.AvgLatencyMs,
			&firstSeen,
			&lastSeen,
			&s.AuthFailures,
			&s.RateLimitHits,
		); err != nil {
			return nil, err
		}
		s.FirstSeen = parseTime(firstSeen)
		s.LastSeen = parseTime(lastSeen)
		result = append(result, s)
	}
	return result, rows.Err()
}

func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.000Z",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
