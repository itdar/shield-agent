package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection.
type DB struct {
	conn *sql.DB
}

// ActionLog represents a single intercepted message log entry.
type ActionLog struct {
	Timestamp   time.Time
	AgentIDHash string
	Method      string
	Direction   string
	Success     bool
	LatencyMs   float64
	PayloadSize int
	AuthStatus  string
	ErrorCode   string
	IPAddress   string
}

// migration represents a single schema migration step.
type migration struct {
	version int
	sql     string
}

// migrations is the ordered list of schema migrations.
// Each migration is applied exactly once and tracked in schema_versions.
var migrations = []migration{
	{
		version: 1,
		sql: `
CREATE TABLE IF NOT EXISTS action_logs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp       DATETIME NOT NULL,
	agent_id_hash   TEXT NOT NULL,
	method          TEXT,
	direction       TEXT,
	success         BOOLEAN,
	latency_ms      REAL,
	payload_size    INTEGER,
	auth_status     TEXT,
	error_code      TEXT
);
CREATE INDEX IF NOT EXISTS idx_action_logs_timestamp
	ON action_logs (timestamp);
CREATE INDEX IF NOT EXISTS idx_action_logs_agent_timestamp
	ON action_logs (agent_id_hash, timestamp);
CREATE INDEX IF NOT EXISTS idx_action_logs_method
	ON action_logs (method);`,
	},
	{
		version: 2,
		sql:     `ALTER TABLE action_logs ADD COLUMN ip_address TEXT DEFAULT '';`,
	},
	{
		version: 3,
		sql: `
CREATE TABLE IF NOT EXISTS tokens (
	id               TEXT PRIMARY KEY,
	name             TEXT NOT NULL,
	token_hash       TEXT NOT NULL UNIQUE,
	created_at       DATETIME NOT NULL,
	expires_at       DATETIME,
	active           BOOLEAN NOT NULL DEFAULT 1,
	quota_hourly     INTEGER NOT NULL DEFAULT 0,
	quota_monthly    INTEGER NOT NULL DEFAULT 0,
	allowed_methods  TEXT DEFAULT '[]',
	ip_allowlist     TEXT DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_tokens_hash ON tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_tokens_active ON tokens (active);`,
	},
	{
		version: 4,
		sql: `
CREATE TABLE IF NOT EXISTS token_usage (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	token_id   TEXT NOT NULL,
	timestamp  DATETIME NOT NULL,
	method     TEXT,
	success    BOOLEAN,
	latency_ms REAL,
	FOREIGN KEY (token_id) REFERENCES tokens(id)
);
CREATE INDEX IF NOT EXISTS idx_token_usage_token_ts ON token_usage (token_id, timestamp);`,
	},
	{
		version: 5,
		sql: `
CREATE TABLE IF NOT EXISTS admin_config (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`,
	},
}

// Open opens (or creates) a SQLite database at path, enables WAL mode, and
// runs migrations.
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", path)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db %q: %w", path, err)
	}

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("pinging sqlite db: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	return db, nil
}

// migrate applies all pending schema migrations in order.
func (db *DB) migrate() error {
	// Ensure the schema_versions tracking table exists.
	const versionTable = `
CREATE TABLE IF NOT EXISTS schema_versions (
	version   INTEGER PRIMARY KEY,
	applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`
	if _, err := db.conn.Exec(versionTable); err != nil {
		return fmt.Errorf("creating schema_versions table: %w", err)
	}

	current, err := db.currentVersion()
	if err != nil {
		return fmt.Errorf("reading current schema version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if _, err := db.conn.Exec(m.sql); err != nil {
			return fmt.Errorf("applying migration %d: %w", m.version, err)
		}
		if _, err := db.conn.Exec("INSERT INTO schema_versions (version) VALUES (?)", m.version); err != nil {
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}
	}
	return nil
}

// currentVersion returns the highest applied migration version, or 0 if none.
func (db *DB) currentVersion() (int, error) {
	row := db.conn.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_versions")
	var v int
	err := row.Scan(&v)
	return v, err
}

// SchemaVersion returns the current schema version (for diagnostics).
func (db *DB) SchemaVersion() (int, error) {
	return db.currentVersion()
}

// Conn returns the underlying *sql.DB connection.
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// Close closes the underlying database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Insert stores a single ActionLog entry.
func (db *DB) Insert(log ActionLog) error {
	const q = `
INSERT INTO action_logs
	(timestamp, agent_id_hash, method, direction, success, latency_ms, payload_size, auth_status, error_code, ip_address)
VALUES
	(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := db.conn.Exec(q,
		log.Timestamp.UTC(),
		log.AgentIDHash,
		log.Method,
		log.Direction,
		log.Success,
		log.LatencyMs,
		log.PayloadSize,
		log.AuthStatus,
		log.ErrorCode,
		log.IPAddress,
	)
	return err
}

// QueryOptions controls which log entries are returned by QueryLogs.
type QueryOptions struct {
	Last      int
	AgentHash string
	Since     time.Duration
	Method    string
}

// QueryLogs returns log entries matching opts, ordered newest-first.
func (db *DB) QueryLogs(opts QueryOptions) ([]ActionLog, error) {
	var where []string
	var args []any

	if opts.AgentHash != "" {
		where = append(where, "agent_id_hash = ?")
		args = append(args, opts.AgentHash)
	}
	if opts.Since > 0 {
		cutoff := time.Now().Add(-opts.Since).UTC()
		where = append(where, "timestamp >= ?")
		args = append(args, cutoff)
	}
	if opts.Method != "" {
		where = append(where, "method = ?")
		args = append(args, opts.Method)
	}

	q := "SELECT timestamp, agent_id_hash, method, direction, success, latency_ms, payload_size, auth_status, error_code, ip_address FROM action_logs"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY timestamp DESC"

	limit := opts.Last
	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("querying logs: %w", err)
	}
	defer rows.Close()

	var logs []ActionLog
	for rows.Next() {
		var l ActionLog
		var ts string
		if err := rows.Scan(&ts, &l.AgentIDHash, &l.Method, &l.Direction,
			&l.Success, &l.LatencyMs, &l.PayloadSize, &l.AuthStatus, &l.ErrorCode, &l.IPAddress); err != nil {
			return nil, fmt.Errorf("scanning log row: %w", err)
		}
		if t, err := time.Parse("2006-01-02T15:04:05Z", ts); err == nil {
			l.Timestamp = t
		} else if t, err := time.Parse("2006-01-02 15:04:05", ts); err == nil {
			l.Timestamp = t
		} else {
			l.Timestamp = time.Now() // fallback
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// Purge deletes log entries older than retentionDays days.
// Returns the number of rows deleted.
func (db *DB) Purge(retentionDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays).UTC()
	res, err := db.conn.Exec("DELETE FROM action_logs WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("purging logs: %w", err)
	}
	return res.RowsAffected()
}
