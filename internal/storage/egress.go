package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// EgressLog represents a single outbound-request log entry.
// Phase 1 populates metadata fields only; Phase 2 adds body-derived fields
// (prompt_hash, model, content_class, pii_*).
type EgressLog struct {
	ID              int64     `json:"id"`
	Timestamp       time.Time `json:"timestamp"`
	CorrelationID   string    `json:"correlation_id"`
	Provider        string    `json:"provider"`
	Model           string    `json:"model"`
	Method          string    `json:"method"`
	Protocol        string    `json:"protocol"`
	Destination     string    `json:"destination"`
	StatusCode      int       `json:"status_code"`
	RequestSize     int64     `json:"request_size"`
	ResponseSize    int64     `json:"response_size"`
	LatencyMs       float64   `json:"latency_ms"`
	ContentClass    string    `json:"content_class"`
	PromptHash      string    `json:"prompt_hash"`
	PIIDetected     bool      `json:"pii_detected"`
	PIIScrubbed     bool      `json:"pii_scrubbed"`
	PolicyAction    string    `json:"policy_action"`
	PolicyRule      string    `json:"policy_rule"`
	AIGeneratedTag  bool      `json:"ai_generated_tag"`
	ErrorDetail     string    `json:"error_detail"`
	PrevHash        string    `json:"prev_hash"`
	RowHash         string    `json:"row_hash"`
}

// EgressAnchor is a hash-chain anchor inserted when older rows are purged.
// It preserves the row_hash of the last purged row so chain verification
// can reconnect across the gap.
type EgressAnchor struct {
	ID              int64     `json:"id"`
	AnchorTimestamp time.Time `json:"anchor_timestamp"`
	PurgedUpToID    int64     `json:"purged_up_to_id"`
	PurgedCount     int64     `json:"purged_count"`
	ChainHash       string    `json:"chain_hash"`
	// NextRowID is the smallest surviving egress_logs.id after purge.
	// -1 means all rows were purged (chain reset).
	NextRowID int64 `json:"next_row_id"`
}

// InsertEgressLog stores a single egress log row.
// RowHash and PrevHash must be pre-computed by the caller (compliance.HashChain).
// The inserted row's auto-increment ID is returned.
func (db *DB) InsertEgressLog(log EgressLog) (int64, error) {
	const q = `
INSERT INTO egress_logs
	(timestamp, correlation_id, provider, model, method, protocol, destination,
	 status_code, request_size, response_size, latency_ms, content_class,
	 prompt_hash, pii_detected, pii_scrubbed, policy_action, policy_rule,
	 ai_generated_tag, error_detail, prev_hash, row_hash)
VALUES
	(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	res, err := db.conn.Exec(q,
		log.Timestamp.UTC(),
		log.CorrelationID,
		log.Provider,
		log.Model,
		log.Method,
		log.Protocol,
		log.Destination,
		log.StatusCode,
		log.RequestSize,
		log.ResponseSize,
		log.LatencyMs,
		log.ContentClass,
		log.PromptHash,
		log.PIIDetected,
		log.PIIScrubbed,
		log.PolicyAction,
		log.PolicyRule,
		log.AIGeneratedTag,
		log.ErrorDetail,
		log.PrevHash,
		log.RowHash,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting egress log: %w", err)
	}
	return res.LastInsertId()
}

// LastEgressRowHash returns the row_hash of the newest egress_logs row,
// or the chain_hash of the newest anchor if no rows survive.
// Returns an empty string when the log is empty.
func (db *DB) LastEgressRowHash() (string, error) {
	var rowHash sql.NullString
	err := db.conn.QueryRow("SELECT row_hash FROM egress_logs ORDER BY id DESC LIMIT 1").Scan(&rowHash)
	if err == nil && rowHash.Valid {
		return rowHash.String, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return "", fmt.Errorf("loading last egress row hash: %w", err)
	}
	// No rows — check the anchor table.
	var anchor sql.NullString
	err = db.conn.QueryRow("SELECT chain_hash FROM egress_log_anchors ORDER BY id DESC LIMIT 1").Scan(&anchor)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("loading last anchor chain hash: %w", err)
	}
	return anchor.String, nil
}

// EgressQueryOptions controls QueryEgressLogs output.
type EgressQueryOptions struct {
	Last          int
	Since         time.Duration
	Provider      string
	Destination   string
	PolicyAction  string
	CorrelationID string
}

// QueryEgressLogs returns egress log rows newest-first.
func (db *DB) QueryEgressLogs(opts EgressQueryOptions) ([]EgressLog, error) {
	var where []string
	var args []any

	if opts.Since > 0 {
		where = append(where, "timestamp >= ?")
		args = append(args, time.Now().Add(-opts.Since).UTC())
	}
	if opts.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, opts.Provider)
	}
	if opts.Destination != "" {
		where = append(where, "destination = ?")
		args = append(args, opts.Destination)
	}
	if opts.PolicyAction != "" {
		where = append(where, "policy_action = ?")
		args = append(args, opts.PolicyAction)
	}
	if opts.CorrelationID != "" {
		where = append(where, "correlation_id = ?")
		args = append(args, opts.CorrelationID)
	}

	q := `SELECT id, timestamp, correlation_id, provider, model, method, protocol, destination,
	status_code, request_size, response_size, latency_ms, content_class, prompt_hash,
	pii_detected, pii_scrubbed, policy_action, policy_rule, ai_generated_tag, error_detail,
	prev_hash, row_hash
FROM egress_logs`

	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC"

	limit := opts.Last
	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("querying egress logs: %w", err)
	}
	defer rows.Close()

	var logs []EgressLog
	for rows.Next() {
		var l EgressLog
		var ts string
		if err := rows.Scan(
			&l.ID, &ts, &l.CorrelationID, &l.Provider, &l.Model, &l.Method, &l.Protocol, &l.Destination,
			&l.StatusCode, &l.RequestSize, &l.ResponseSize, &l.LatencyMs, &l.ContentClass, &l.PromptHash,
			&l.PIIDetected, &l.PIIScrubbed, &l.PolicyAction, &l.PolicyRule, &l.AIGeneratedTag, &l.ErrorDetail,
			&l.PrevHash, &l.RowHash,
		); err != nil {
			return nil, fmt.Errorf("scanning egress log row: %w", err)
		}
		l.Timestamp = parseTimestamp(ts)
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// ListEgressAnchors returns all anchors ordered by id.
func (db *DB) ListEgressAnchors() ([]EgressAnchor, error) {
	rows, err := db.conn.Query(`SELECT id, anchor_timestamp, purged_up_to_id, purged_count, chain_hash, next_row_id
FROM egress_log_anchors ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing egress anchors: %w", err)
	}
	defer rows.Close()

	var anchors []EgressAnchor
	for rows.Next() {
		var a EgressAnchor
		var ts string
		if err := rows.Scan(&a.ID, &ts, &a.PurgedUpToID, &a.PurgedCount, &a.ChainHash, &a.NextRowID); err != nil {
			return nil, fmt.Errorf("scanning anchor: %w", err)
		}
		a.AnchorTimestamp = parseTimestamp(ts)
		anchors = append(anchors, a)
	}
	return anchors, rows.Err()
}

// PurgeEgress removes egress_logs rows older than retentionDays, inserting
// an anchor row inside the same transaction so hash-chain verification can
// span the deletion gap. Returns the number of rows deleted.
// retentionDays <= 0 means "no purge" — returns 0.
func (db *DB) PurgeEgress(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).UTC()

	tx, err := db.conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("starting purge txn: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var lastID int64
	var lastHash string
	row := tx.QueryRow(
		`SELECT id, row_hash FROM egress_logs WHERE timestamp < ? ORDER BY id DESC LIMIT 1`, cutoff)
	if err := row.Scan(&lastID, &lastHash); err != nil {
		if err == sql.ErrNoRows {
			return 0, tx.Commit()
		}
		return 0, fmt.Errorf("scanning purge boundary: %w", err)
	}

	var nextID int64 = -1
	nextRow := tx.QueryRow(`SELECT id FROM egress_logs WHERE id > ? ORDER BY id ASC LIMIT 1`, lastID)
	if err := nextRow.Scan(&nextID); err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("scanning next row id: %w", err)
	}

	var count int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM egress_logs WHERE timestamp < ?`, cutoff).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting purge rows: %w", err)
	}

	if _, err := tx.Exec(
		`INSERT INTO egress_log_anchors (anchor_timestamp, purged_up_to_id, purged_count, chain_hash, next_row_id)
		 VALUES (?, ?, ?, ?, ?)`,
		time.Now().UTC(), lastID, count, lastHash, nextID); err != nil {
		return 0, fmt.Errorf("inserting anchor: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM egress_logs WHERE timestamp < ?`, cutoff); err != nil {
		return 0, fmt.Errorf("deleting purged rows: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing purge: %w", err)
	}
	return count, nil
}

// parseTimestamp tolerates the formats SQLite returns for DATETIME columns.
func parseTimestamp(s string) time.Time {
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Now()
}
