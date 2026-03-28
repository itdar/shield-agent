package storage

import (
	"os"
	"testing"
	"time"
)

func openTempDB(t *testing.T) *DB {
	t.Helper()
	f, err := os.CreateTemp("", "*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() { os.Remove(path) })

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpen(t *testing.T) {
	f, err := os.CreateTemp("", "*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestMigrate(t *testing.T) {
	db := openTempDB(t)

	// action_logs table should exist after Open (which calls migrate).
	rows, err := db.conn.Query("SELECT COUNT(*) FROM action_logs")
	if err != nil {
		t.Fatalf("querying action_logs after migrate: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("expected a row from COUNT(*)")
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		t.Fatalf("scanning count: %v", err)
	}
}

func TestInsertAndQuery(t *testing.T) {
	db := openTempDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	entry := ActionLog{
		Timestamp:   now,
		AgentIDHash: "abc123",
		Method:      "tools/call",
		Direction:   "in",
		Success:     true,
		LatencyMs:   42.5,
		PayloadSize: 100,
		AuthStatus:  "ok",
		ErrorCode:   "",
	}
	if err := db.Insert(entry); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	logs, err := db.QueryLogs(QueryOptions{Last: 10})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	got := logs[0]
	if got.AgentIDHash != entry.AgentIDHash {
		t.Errorf("AgentIDHash: got %q, want %q", got.AgentIDHash, entry.AgentIDHash)
	}
	if got.Method != entry.Method {
		t.Errorf("Method: got %q, want %q", got.Method, entry.Method)
	}
	if got.Direction != entry.Direction {
		t.Errorf("Direction: got %q, want %q", got.Direction, entry.Direction)
	}
	if got.Success != entry.Success {
		t.Errorf("Success: got %v, want %v", got.Success, entry.Success)
	}
	if got.LatencyMs != entry.LatencyMs {
		t.Errorf("LatencyMs: got %v, want %v", got.LatencyMs, entry.LatencyMs)
	}
	if got.PayloadSize != entry.PayloadSize {
		t.Errorf("PayloadSize: got %v, want %v", got.PayloadSize, entry.PayloadSize)
	}
	if got.AuthStatus != entry.AuthStatus {
		t.Errorf("AuthStatus: got %q, want %q", got.AuthStatus, entry.AuthStatus)
	}
}

func TestQueryFilter_Agent(t *testing.T) {
	db := openTempDB(t)

	now := time.Now().UTC()
	if err := db.Insert(ActionLog{Timestamp: now, AgentIDHash: "agent-a", Method: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Insert(ActionLog{Timestamp: now, AgentIDHash: "agent-b", Method: "m"}); err != nil {
		t.Fatal(err)
	}

	logs, err := db.QueryLogs(QueryOptions{AgentHash: "agent-a", Last: 10})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log for agent-a, got %d", len(logs))
	}
	if logs[0].AgentIDHash != "agent-a" {
		t.Errorf("got agent %q, want agent-a", logs[0].AgentIDHash)
	}
}

func TestQueryFilter_Method(t *testing.T) {
	db := openTempDB(t)

	now := time.Now().UTC()
	if err := db.Insert(ActionLog{Timestamp: now, AgentIDHash: "a", Method: "tools/call"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Insert(ActionLog{Timestamp: now, AgentIDHash: "a", Method: "resources/list"}); err != nil {
		t.Fatal(err)
	}

	logs, err := db.QueryLogs(QueryOptions{Method: "tools/call", Last: 10})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log for method tools/call, got %d", len(logs))
	}
	if logs[0].Method != "tools/call" {
		t.Errorf("got method %q, want tools/call", logs[0].Method)
	}
}

func TestQueryFilter_Since(t *testing.T) {
	db := openTempDB(t)

	old := time.Now().UTC().Add(-1 * time.Hour)
	recent := time.Now().UTC()

	if err := db.Insert(ActionLog{Timestamp: old, AgentIDHash: "a", Method: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Insert(ActionLog{Timestamp: recent, AgentIDHash: "a", Method: "recent"}); err != nil {
		t.Fatal(err)
	}

	logs, err := db.QueryLogs(QueryOptions{Since: 30 * time.Minute, Last: 10})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 recent log, got %d", len(logs))
	}
	if logs[0].Method != "recent" {
		t.Errorf("got method %q, want recent", logs[0].Method)
	}
}

func TestQueryFilter_Last(t *testing.T) {
	db := openTempDB(t)

	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		if err := db.Insert(ActionLog{Timestamp: ts, AgentIDHash: "a", Method: "m"}); err != nil {
			t.Fatal(err)
		}
	}

	logs, err := db.QueryLogs(QueryOptions{Last: 3})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs with Last=3, got %d", len(logs))
	}
}

func TestSchemaVersion(t *testing.T) {
	db := openTempDB(t)

	v, err := db.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 6 {
		t.Fatalf("expected schema version 6, got %d", v)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db := openTempDB(t)

	// Running migrate again should be a no-op.
	if err := db.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	v, err := db.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 6 {
		t.Fatalf("expected schema version 6 after re-migrate, got %d", v)
	}
}

func TestInsertWithIPAddress(t *testing.T) {
	db := openTempDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	entry := ActionLog{
		Timestamp:   now,
		AgentIDHash: "abc",
		Method:      "test",
		Direction:   "in",
		Success:     true,
		IPAddress:   "192.168.1.100",
	}
	if err := db.Insert(entry); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	logs, err := db.QueryLogs(QueryOptions{Last: 1})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].IPAddress != "192.168.1.100" {
		t.Errorf("IPAddress: got %q, want %q", logs[0].IPAddress, "192.168.1.100")
	}
}

func TestPurge(t *testing.T) {
	db := openTempDB(t)

	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := db.Insert(ActionLog{Timestamp: old, AgentIDHash: "a", Method: "old"}); err != nil {
		t.Fatal(err)
	}

	// retention=0 means cutoff is today; entries older than today are deleted.
	n, err := db.Purge(0)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row deleted, got %d", n)
	}

	logs, err := db.QueryLogs(QueryOptions{Last: 10})
	if err != nil {
		t.Fatalf("QueryLogs after purge: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 logs after purge, got %d", len(logs))
	}
}
