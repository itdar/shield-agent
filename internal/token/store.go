// Package token provides token-based access control for shield-agent.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Token represents an API access token.
type Token struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	TokenHash      string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Active         bool      `json:"active"`
	QuotaHourly    int       `json:"quota_hourly"`
	QuotaMonthly   int       `json:"quota_monthly"`
	AllowedMethods []string  `json:"allowed_methods"`
	IPAllowlist    []string  `json:"ip_allowlist"`
}

// UsageRecord represents a single token usage entry.
type UsageRecord struct {
	TokenID   string    `json:"token_id"`
	Timestamp time.Time `json:"timestamp"`
	Method    string    `json:"method"`
	Success   bool      `json:"success"`
	LatencyMs float64   `json:"latency_ms"`
}

// UsageStats holds aggregated usage statistics for a token.
type UsageStats struct {
	TotalRequests int     `json:"total_requests"`
	SuccessCount  int     `json:"success_count"`
	FailCount     int     `json:"fail_count"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	HourlyUsage   int     `json:"hourly_usage"`
	MonthlyUsage  int     `json:"monthly_usage"`
}

// Store manages tokens in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore creates a Store using the given database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// GenerateToken creates a random 32-byte hex token string.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashToken returns the SHA-256 hex hash of a raw token.
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// Create stores a new token and returns its ID.
func (s *Store) Create(name, tokenHash string, expiresAt *time.Time, quotaHourly, quotaMonthly int, allowedMethods, ipAllowlist []string) (string, error) {
	id, err := generateID()
	if err != nil {
		return "", err
	}

	methodsJSON, _ := json.Marshal(allowedMethods)
	ipJSON, _ := json.Marshal(ipAllowlist)

	var expiresVal interface{}
	if expiresAt != nil {
		expiresVal = expiresAt.UTC()
	}

	const q = `INSERT INTO tokens (id, name, token_hash, created_at, expires_at, active, quota_hourly, quota_monthly, allowed_methods, ip_allowlist) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`
	_, err = s.db.Exec(q, id, name, tokenHash, time.Now().UTC(), expiresVal, quotaHourly, quotaMonthly, string(methodsJSON), string(ipJSON))
	if err != nil {
		return "", fmt.Errorf("inserting token: %w", err)
	}
	return id, nil
}

// GetByHash looks up a token by its hash. Returns nil if not found.
func (s *Store) GetByHash(hash string) (*Token, error) {
	return s.scanOne("SELECT id, name, token_hash, created_at, expires_at, active, quota_hourly, quota_monthly, allowed_methods, ip_allowlist FROM tokens WHERE token_hash = ?", hash)
}

// GetByID looks up a token by its ID. Returns nil if not found.
func (s *Store) GetByID(id string) (*Token, error) {
	return s.scanOne("SELECT id, name, token_hash, created_at, expires_at, active, quota_hourly, quota_monthly, allowed_methods, ip_allowlist FROM tokens WHERE id = ?", id)
}

// List returns all tokens, optionally filtered by active status.
func (s *Store) List(activeOnly bool) ([]Token, error) {
	q := "SELECT id, name, token_hash, created_at, expires_at, active, quota_hourly, quota_monthly, allowed_methods, ip_allowlist FROM tokens"
	if activeOnly {
		q += " WHERE active = 1"
	}
	q += " ORDER BY created_at DESC"

	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}
	defer rows.Close()

	var tokens []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, *t)
	}
	return tokens, rows.Err()
}

// Revoke deactivates a token by ID.
func (s *Store) Revoke(id string) error {
	res, err := s.db.Exec("UPDATE tokens SET active = 0 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("revoking token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("token %q not found", id)
	}
	return nil
}

// Delete removes a token and its usage records.
func (s *Store) Delete(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec("DELETE FROM token_usage WHERE token_id = ?", id); err != nil {
		return fmt.Errorf("deleting token usage: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM tokens WHERE id = ?", id); err != nil {
		return fmt.Errorf("deleting token: %w", err)
	}
	return tx.Commit()
}

// RecordUsage inserts a usage record for a token.
func (s *Store) RecordUsage(tokenID, method string, success bool, latencyMs float64) error {
	const q = `INSERT INTO token_usage (token_id, timestamp, method, success, latency_ms) VALUES (?, ?, ?, ?, ?)`
	_, err := s.db.Exec(q, tokenID, time.Now().UTC(), method, success, latencyMs)
	return err
}

// CountUsage returns the number of requests for a token within a time window.
func (s *Store) CountUsage(tokenID string, window time.Duration) (int, error) {
	cutoff := time.Now().Add(-window).UTC()
	row := s.db.QueryRow("SELECT COUNT(*) FROM token_usage WHERE token_id = ? AND timestamp >= ?", tokenID, cutoff)
	var count int
	err := row.Scan(&count)
	return count, err
}

// GetStats returns aggregated usage statistics for a token within a time range.
func (s *Store) GetStats(tokenID string, since time.Duration) (*UsageStats, error) {
	cutoff := time.Now().Add(-since).UTC()
	row := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN NOT success THEN 1 ELSE 0 END), 0),
		       COALESCE(AVG(latency_ms), 0)
		FROM token_usage WHERE token_id = ? AND timestamp >= ?`, tokenID, cutoff)

	stats := &UsageStats{}
	if err := row.Scan(&stats.TotalRequests, &stats.SuccessCount, &stats.FailCount, &stats.AvgLatencyMs); err != nil {
		return nil, err
	}

	hourly, err := s.CountUsage(tokenID, time.Hour)
	if err != nil {
		return nil, err
	}
	stats.HourlyUsage = hourly

	monthly, err := s.CountUsage(tokenID, 30*24*time.Hour)
	if err != nil {
		return nil, err
	}
	stats.MonthlyUsage = monthly

	return stats, nil
}

// IsExpired returns true if the token has an expiration time that has passed.
func (t *Token) IsExpired() bool {
	if t.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*t.ExpiresAt)
}

// IsMethodAllowed returns true if the given method is permitted by this token.
// An empty allowed_methods list means all methods are permitted.
func (t *Token) IsMethodAllowed(method string) bool {
	if len(t.AllowedMethods) == 0 {
		return true
	}
	for _, m := range t.AllowedMethods {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}

func (s *Store) scanOne(query string, args ...interface{}) (*Token, error) {
	row := s.db.QueryRow(query, args...)
	t, err := scanTokenRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanToken(rows *sql.Rows) (*Token, error) {
	return scanTokenRow(rows)
}

func scanTokenRow(row scannable) (*Token, error) {
	var t Token
	var expiresAt sql.NullTime
	var createdStr string
	var methodsJSON, ipJSON string

	if err := row.Scan(&t.ID, &t.Name, &t.TokenHash, &createdStr, &expiresAt, &t.Active, &t.QuotaHourly, &t.QuotaMonthly, &methodsJSON, &ipJSON); err != nil {
		return nil, err
	}

	// Parse created_at
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, createdStr); err == nil {
			t.CreatedAt = parsed
			break
		}
	}

	if expiresAt.Valid {
		t.ExpiresAt = &expiresAt.Time
	}

	json.Unmarshal([]byte(methodsJSON), &t.AllowedMethods) //nolint:errcheck
	json.Unmarshal([]byte(ipJSON), &t.IPAllowlist)         //nolint:errcheck

	if t.AllowedMethods == nil {
		t.AllowedMethods = []string{}
	}
	if t.IPAllowlist == nil {
		t.IPAllowlist = []string{}
	}

	return &t, nil
}

func generateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
