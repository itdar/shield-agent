package reputation

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}

	// Create action_logs table (mimics migration v1+v2).
	_, err = db.Exec(`
CREATE TABLE action_logs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp       DATETIME NOT NULL,
	agent_id_hash   TEXT NOT NULL,
	method          TEXT,
	direction       TEXT,
	success         BOOLEAN,
	latency_ms      REAL,
	payload_size    INTEGER,
	auth_status     TEXT,
	error_code      TEXT,
	ip_address      TEXT DEFAULT '',
	upstream_name   TEXT DEFAULT ''
);
CREATE TABLE reputation_scores (
	agent_id_hash   TEXT PRIMARY KEY,
	trust_level     TEXT NOT NULL DEFAULT 'normal',
	trust_score     REAL NOT NULL DEFAULT 0.5,
	success_rate    REAL NOT NULL DEFAULT 0.0,
	error_rate      REAL NOT NULL DEFAULT 0.0,
	avg_latency_ms  REAL NOT NULL DEFAULT 0.0,
	request_count   INTEGER NOT NULL DEFAULT 0,
	rate_limit_hits INTEGER NOT NULL DEFAULT 0,
	auth_failures   INTEGER NOT NULL DEFAULT 0,
	first_seen      DATETIME,
	last_seen       DATETIME,
	computed_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	window_hours    INTEGER NOT NULL DEFAULT 24,
	source          TEXT NOT NULL DEFAULT 'local'
)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func insertLog(t *testing.T, db *sql.DB, agentHash, method string, success bool, latencyMs float64, authStatus, errorCode string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO action_logs (timestamp, agent_id_hash, method, direction, success, latency_ms, payload_size, auth_status, error_code)
		 VALUES (datetime('now'), ?, ?, 'in', ?, ?, 0, ?, ?)`,
		agentHash, method, success, latencyMs, authStatus, errorCode,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCalculator_ComputeScore(t *testing.T) {
	cfg := DefaultConfig()
	calc := newCalculator(nil, cfg.Weights, cfg.Thresholds, cfg.WindowHours)

	tests := []struct {
		name           string
		stats          agentStats
		expectedLevel  TrustLevel
		minScore       float64
		maxScore       float64
	}{
		{
			name: "perfect agent → trusted",
			stats: agentStats{
				RequestCount: 500,
				SuccessCount: 500,
				ErrorCount:   0,
				AvgLatencyMs: 50,
			},
			expectedLevel: TrustTrusted,
			minScore:      0.8,
			maxScore:      1.0,
		},
		{
			name: "50% error rate → normal (borderline)",
			stats: agentStats{
				RequestCount: 100,
				SuccessCount: 50,
				ErrorCount:   50,
				AvgLatencyMs: 200,
			},
			expectedLevel: TrustNormal,
			minScore:      0.4,
			maxScore:      0.7,
		},
		{
			name: "all failures + auth failures → blocked",
			stats: agentStats{
				RequestCount: 20,
				SuccessCount: 0,
				ErrorCount:   20,
				AvgLatencyMs: 1000,
				AuthFailures: 10,
			},
			expectedLevel: TrustBlocked,
			minScore:      0.0,
			maxScore:      0.1,
		},
		{
			name: "normal agent → normal",
			stats: agentStats{
				RequestCount: 200,
				SuccessCount: 170,
				ErrorCount:   30,
				AvgLatencyMs: 300,
				AuthFailures: 1,
			},
			expectedLevel: TrustNormal,
			minScore:      0.4,
			maxScore:      0.8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := calc.computeScore(tt.stats, time.Now())
			if score.TrustLevel != tt.expectedLevel {
				t.Errorf("TrustLevel = %v, want %v (score=%.3f)", score.TrustLevel, tt.expectedLevel, score.TrustScore)
			}
			if score.TrustScore < tt.minScore || score.TrustScore > tt.maxScore {
				t.Errorf("TrustScore = %.3f, want [%.3f, %.3f]", score.TrustScore, tt.minScore, tt.maxScore)
			}
		})
	}
}

func TestCalculator_RecalculateAll(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	cfg := DefaultConfig()
	calc := newCalculator(db, cfg.Weights, cfg.Thresholds, cfg.WindowHours)

	// Insert logs for two agents.
	for i := 0; i < 100; i++ {
		insertLog(t, db, "agent-good", "tools/call", true, 50, "verified", "")
	}
	for i := 0; i < 50; i++ {
		insertLog(t, db, "agent-bad", "tools/call", false, 2000, "failed", "429")
	}

	scores, err := calc.recalculateAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}

	scoreMap := make(map[string]Score)
	for _, s := range scores {
		scoreMap[s.AgentIDHash] = s
	}

	good := scoreMap["agent-good"]
	if good.TrustLevel != TrustTrusted && good.TrustLevel != TrustNormal {
		t.Errorf("agent-good: expected trusted or normal, got %v (score=%.3f)", good.TrustLevel, good.TrustScore)
	}

	bad := scoreMap["agent-bad"]
	if bad.TrustLevel != TrustBlocked && bad.TrustLevel != TrustSuspicious {
		t.Errorf("agent-bad: expected blocked or suspicious, got %v (score=%.3f)", bad.TrustLevel, bad.TrustScore)
	}
}

func TestCache_GetMultiplier(t *testing.T) {
	c := newCache(60 * time.Second)

	// Unknown agent returns 1.0.
	if m := c.getMultiplier("unknown"); m != 1.0 {
		t.Errorf("unknown agent: got %f, want 1.0", m)
	}

	// Update cache and check multipliers.
	scores := []Score{
		{AgentIDHash: "trusted-agent", TrustLevel: TrustTrusted, TrustScore: 0.9},
		{AgentIDHash: "blocked-agent", TrustLevel: TrustBlocked, TrustScore: 0.05},
	}
	multipliers := map[string]float64{
		"trusted": 2.0, "normal": 1.0, "suspicious": 0.25, "blocked": 0.0,
	}
	c.update(scores, multipliers)

	if m := c.getMultiplier("trusted-agent"); m != 2.0 {
		t.Errorf("trusted: got %f, want 2.0", m)
	}
	if m := c.getMultiplier("blocked-agent"); m != 0.0 {
		t.Errorf("blocked: got %f, want 0.0", m)
	}
	if m := c.getMultiplier("still-unknown"); m != 1.0 {
		t.Errorf("unknown after update: got %f, want 1.0", m)
	}
}

func TestCache_AllScores(t *testing.T) {
	c := newCache(60 * time.Second)

	scores := []Score{
		{AgentIDHash: "a1", TrustLevel: TrustNormal, TrustScore: 0.5},
		{AgentIDHash: "a2", TrustLevel: TrustTrusted, TrustScore: 0.9},
	}
	c.update(scores, map[string]float64{"normal": 1.0, "trusted": 2.0})

	all := c.allScores()
	if len(all) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(all))
	}
}

func TestStore_SaveAndLoad(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	s := newStore(db)

	scores := []Score{
		{
			AgentIDHash: "store-test",
			TrustLevel:  TrustNormal,
			TrustScore:  0.65,
			SuccessRate: 0.9,
			ErrorRate:   0.1,
			FirstSeen:   time.Now().Add(-1 * time.Hour),
			LastSeen:    time.Now(),
			ComputedAt:  time.Now(),
			WindowDuration: "24h",
			Source:      "local",
		},
	}

	if err := s.saveAll(scores); err != nil {
		t.Fatal(err)
	}

	loaded, err := s.loadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1, got %d", len(loaded))
	}
	if loaded[0].AgentIDHash != "store-test" {
		t.Errorf("agent hash mismatch: %s", loaded[0].AgentIDHash)
	}
	if loaded[0].TrustLevel != TrustNormal {
		t.Errorf("trust level mismatch: %s", loaded[0].TrustLevel)
	}

	one, err := s.loadOne("store-test")
	if err != nil {
		t.Fatal(err)
	}
	if one == nil || one.AgentIDHash != "store-test" {
		t.Error("loadOne failed")
	}

	missing, err := s.loadOne("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Error("expected nil for missing agent")
	}
}

func TestLocalProvider_Integration(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	cfg := DefaultConfig()
	cfg.Enabled = true

	provider := NewLocalProvider(db, nil, cfg)

	// Insert activity.
	for i := 0; i < 80; i++ {
		insertLog(t, db, "int-agent", "tools/call", true, 100, "verified", "")
	}
	for i := 0; i < 20; i++ {
		insertLog(t, db, "int-agent", "tools/call", false, 500, "verified", "")
	}

	// Recalculate.
	if err := provider.Recalculate(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	score, err := provider.GetScore(ctx, "int-agent")
	if err != nil {
		t.Fatal(err)
	}
	if score == nil {
		t.Fatal("expected score, got nil")
	}
	if score.RequestCount != 100 {
		t.Errorf("request count = %d, want 100", score.RequestCount)
	}

	multiplier := provider.GetRateMultiplier(ctx, "int-agent")
	if multiplier <= 0 {
		t.Errorf("expected positive multiplier, got %f", multiplier)
	}

	stats, err := provider.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalAgents != 1 {
		t.Errorf("total agents = %d, want 1", stats.TotalAgents)
	}
}

func TestClassifyTrustLevel(t *testing.T) {
	th := Thresholds{Trusted: 0.8, Normal: 0.4, Suspicious: 0.1}

	tests := []struct {
		score    float64
		expected TrustLevel
	}{
		{0.95, TrustTrusted},
		{0.80, TrustTrusted},
		{0.79, TrustNormal},
		{0.40, TrustNormal},
		{0.39, TrustSuspicious},
		{0.10, TrustSuspicious},
		{0.09, TrustBlocked},
		{0.00, TrustBlocked},
	}

	for _, tt := range tests {
		got := classifyTrustLevel(tt.score, th)
		if got != tt.expected {
			t.Errorf("classifyTrustLevel(%.2f) = %v, want %v", tt.score, got, tt.expected)
		}
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		v, lo, hi, expected float64
	}{
		{0.5, 0, 1, 0.5},
		{-0.1, 0, 1, 0},
		{1.5, 0, 1, 1},
		{0, 0, 1, 0},
		{1, 0, 1, 1},
	}
	for _, tt := range tests {
		got := clamp(tt.v, tt.lo, tt.hi)
		if got != tt.expected {
			t.Errorf("clamp(%f, %f, %f) = %f, want %f", tt.v, tt.lo, tt.hi, got, tt.expected)
		}
	}
}
