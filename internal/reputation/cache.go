package reputation

import (
	"sync"
	"time"
)

// cache holds agent scores in memory for fast guard middleware lookups.
type cache struct {
	mu          sync.RWMutex
	scores      map[string]*Score
	multipliers map[string]float64 // precomputed for hot path
	ttl         time.Duration
	updatedAt   time.Time
}

func newCache(ttl time.Duration) *cache {
	return &cache{
		scores:      make(map[string]*Score),
		multipliers: make(map[string]float64),
		ttl:         ttl,
	}
}

// getMultiplier returns the cached rate multiplier for an agent.
// Returns 1.0 (normal) if the agent is unknown or cache is stale.
func (c *cache) getMultiplier(agentIDHash string) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	m, ok := c.multipliers[agentIDHash]
	if !ok {
		return 1.0
	}
	return m
}

// getScore returns the cached score for an agent, or nil if not found.
func (c *cache) getScore(agentIDHash string) *Score {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.scores[agentIDHash]
}

// allScores returns a copy of all cached scores.
func (c *cache) allScores() []Score {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]Score, 0, len(c.scores))
	for _, s := range c.scores {
		result = append(result, *s)
	}
	return result
}

// update replaces all cached scores and precomputes multipliers.
func (c *cache) update(scores []Score, multipliers map[string]float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.scores = make(map[string]*Score, len(scores))
	c.multipliers = make(map[string]float64, len(scores))

	for i := range scores {
		s := scores[i]
		c.scores[s.AgentIDHash] = &s
		if m, ok := multipliers[string(s.TrustLevel)]; ok {
			c.multipliers[s.AgentIDHash] = m
		} else {
			c.multipliers[s.AgentIDHash] = 1.0
		}
	}
	c.updatedAt = time.Now()
}

// lastUpdate returns when the cache was last refreshed.
func (c *cache) lastUpdate() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.updatedAt
}
