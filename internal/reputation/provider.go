package reputation

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"time"
)

// LocalProvider implements Provider using local SQLite data.
type LocalProvider struct {
	calc   *calculator
	store  *store
	cache  *cache
	cfg    Config
	logger *slog.Logger
}

// NewLocalProvider creates a reputation provider backed by local action_logs.
// It loads persisted scores into the cache on creation (warm start).
func NewLocalProvider(db *sql.DB, logger *slog.Logger, cfg Config) *LocalProvider {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	p := &LocalProvider{
		calc:   newCalculator(db, cfg.Weights, cfg.Thresholds, cfg.WindowHours),
		store:  newStore(db),
		cache:  newCache(time.Duration(cfg.CacheTTL) * time.Second),
		cfg:    cfg,
		logger: logger,
	}

	// Warm cache from persisted scores.
	if scores, err := p.store.loadAll(); err == nil && len(scores) > 0 {
		p.cache.update(scores, cfg.RateMultipliers)
		logger.Info("reputation cache warmed from DB", slog.Int("agents", len(scores)))
	}

	return p
}

// GetScore returns the cached score for an agent.
func (p *LocalProvider) GetScore(_ context.Context, agentIDHash string) (*Score, error) {
	if s := p.cache.getScore(agentIDHash); s != nil {
		return s, nil
	}
	// Fall back to DB for agents not yet in cache.
	return p.store.loadOne(agentIDHash)
}

// GetRateMultiplier returns the cached rate limit multiplier (hot path).
func (p *LocalProvider) GetRateMultiplier(_ context.Context, agentIDHash string) float64 {
	return p.cache.getMultiplier(agentIDHash)
}

// ListScores returns all cached scores.
func (p *LocalProvider) ListScores(_ context.Context) ([]Score, error) {
	return p.cache.allScores(), nil
}

// Stats returns aggregate reputation statistics.
func (p *LocalProvider) Stats(_ context.Context) (*AggregateStats, error) {
	scores := p.cache.allScores()
	stats := &AggregateStats{
		TotalAgents:  len(scores),
		ByTrustLevel: make(map[string]int),
		LastRecalcAt: p.cache.lastUpdate(),
	}
	var totalScore float64
	for _, s := range scores {
		stats.ByTrustLevel[string(s.TrustLevel)]++
		totalScore += s.TrustScore
	}
	if stats.TotalAgents > 0 {
		stats.AvgTrustScore = totalScore / float64(stats.TotalAgents)
	}
	return stats, nil
}

// ReportScores accepts scores from a remote instance (Phase B).
func (p *LocalProvider) ReportScores(_ context.Context, _ string, scores []Score) (int, error) {
	// Mark as remote and persist.
	for i := range scores {
		scores[i].Source = "remote"
	}
	if err := p.store.saveAll(scores); err != nil {
		return 0, err
	}
	return len(scores), nil
}

// Recalculate forces an immediate score recalculation for all agents.
func (p *LocalProvider) Recalculate() error {
	scores, err := p.calc.recalculateAll()
	if err != nil {
		return err
	}
	if err := p.store.saveAll(scores); err != nil {
		p.logger.Error("failed to persist reputation scores", slog.String("error", err.Error()))
	}
	p.cache.update(scores, p.cfg.RateMultipliers)
	p.logger.Info("reputation scores recalculated", slog.Int("agents", len(scores)))
	return nil
}

// RecalculateOne forces recalculation for a single agent.
func (p *LocalProvider) RecalculateOne(agentIDHash string) (*Score, error) {
	return p.calc.recalculateOne(agentIDHash)
}

// RunRecalcLoop periodically recalculates all agent scores.
// It blocks until ctx is cancelled.
func (p *LocalProvider) RunRecalcLoop(ctx context.Context) {
	interval := time.Duration(p.cfg.RecalcInterval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	// Run once immediately.
	if err := p.Recalculate(); err != nil {
		p.logger.Error("initial reputation recalculation failed", slog.String("error", err.Error()))
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.Recalculate(); err != nil {
				p.logger.Error("reputation recalculation failed", slog.String("error", err.Error()))
			}
		}
	}
}
