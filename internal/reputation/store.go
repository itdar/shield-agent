package reputation

import (
	"database/sql"
	"fmt"
	"time"
)

// store persists reputation scores to SQLite.
type store struct {
	db *sql.DB
}

func newStore(db *sql.DB) *store {
	return &store{db: db}
}

// saveAll upserts all scores into the reputation_scores table.
func (s *store) saveAll(scores []Score) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.Prepare(`
INSERT OR REPLACE INTO reputation_scores
	(agent_id_hash, trust_level, trust_score, success_rate, error_rate,
	 avg_latency_ms, request_count, rate_limit_hits, auth_failures,
	 first_seen, last_seen, computed_at, window_hours, source)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, sc := range scores {
		_, err := stmt.Exec(
			sc.AgentIDHash, string(sc.TrustLevel), sc.TrustScore,
			sc.SuccessRate, sc.ErrorRate, sc.AvgLatencyMs,
			sc.RequestCount, sc.RateLimitHits, sc.AuthFailures,
			sc.FirstSeen.UTC(), sc.LastSeen.UTC(), sc.ComputedAt.UTC(),
			parseWindowHours(sc.WindowDuration), sc.Source,
		)
		if err != nil {
			return fmt.Errorf("insert %s: %w", sc.AgentIDHash, err)
		}
	}

	return tx.Commit()
}

// loadAll reads all persisted scores (used for cache warm-up on startup).
func (s *store) loadAll() ([]Score, error) {
	rows, err := s.db.Query(`
SELECT agent_id_hash, trust_level, trust_score, success_rate, error_rate,
       avg_latency_ms, request_count, rate_limit_hits, auth_failures,
       first_seen, last_seen, computed_at, window_hours, source
FROM reputation_scores
ORDER BY trust_score DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scores []Score
	for rows.Next() {
		var sc Score
		var firstSeen, lastSeen, computedAt string
		var windowHours int
		if err := rows.Scan(
			&sc.AgentIDHash, &sc.TrustLevel, &sc.TrustScore,
			&sc.SuccessRate, &sc.ErrorRate, &sc.AvgLatencyMs,
			&sc.RequestCount, &sc.RateLimitHits, &sc.AuthFailures,
			&firstSeen, &lastSeen, &computedAt,
			&windowHours, &sc.Source,
		); err != nil {
			return nil, err
		}
		sc.FirstSeen = parseTime(firstSeen)
		sc.LastSeen = parseTime(lastSeen)
		sc.ComputedAt = parseTime(computedAt)
		sc.WindowDuration = fmt.Sprintf("%dh", windowHours)
		scores = append(scores, sc)
	}
	return scores, rows.Err()
}

// loadOne reads a single persisted score.
func (s *store) loadOne(agentIDHash string) (*Score, error) {
	var sc Score
	var firstSeen, lastSeen, computedAt string
	var windowHours int
	err := s.db.QueryRow(`
SELECT agent_id_hash, trust_level, trust_score, success_rate, error_rate,
       avg_latency_ms, request_count, rate_limit_hits, auth_failures,
       first_seen, last_seen, computed_at, window_hours, source
FROM reputation_scores
WHERE agent_id_hash = ?`, agentIDHash).Scan(
		&sc.AgentIDHash, &sc.TrustLevel, &sc.TrustScore,
		&sc.SuccessRate, &sc.ErrorRate, &sc.AvgLatencyMs,
		&sc.RequestCount, &sc.RateLimitHits, &sc.AuthFailures,
		&firstSeen, &lastSeen, &computedAt,
		&windowHours, &sc.Source,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sc.FirstSeen = parseTime(firstSeen)
	sc.LastSeen = parseTime(lastSeen)
	sc.ComputedAt = parseTime(computedAt)
	sc.WindowDuration = fmt.Sprintf("%dh", windowHours)
	return &sc, nil
}

func parseWindowHours(duration string) int {
	var h int
	if _, err := fmt.Sscanf(duration, "%dh", &h); err == nil {
		return h
	}
	return 24
}

// purgeStale removes scores older than the given threshold.
func (s *store) purgeStale(maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge).UTC()
	res, err := s.db.Exec("DELETE FROM reputation_scores WHERE computed_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
