package compliance

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/itdar/shield-agent/internal/storage"
)

func openDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "shield.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func makeRow(ts time.Time, dest string) storage.EgressLog {
	return storage.EgressLog{
		Timestamp:    ts,
		Provider:     "openai",
		Method:       "CONNECT " + dest + ":443",
		Protocol:     "https",
		Destination:  dest,
		PolicyAction: "allow",
	}
}

func TestHashChainLinksRows(t *testing.T) {
	db := openDB(t)
	chain, err := NewHashChain(db)
	if err != nil {
		t.Fatalf("NewHashChain: %v", err)
	}

	var prevTail string
	for i := 0; i < 3; i++ {
		row := makeRow(time.Now().UTC().Add(time.Duration(i)*time.Millisecond), "api.openai.com")
		row = chain.ComputeRow(row)
		if row.PrevHash != prevTail {
			t.Fatalf("row %d prev = %q, want %q", i, row.PrevHash, prevTail)
		}
		if row.RowHash == "" {
			t.Fatalf("row %d row_hash is empty", i)
		}
		if _, err := db.InsertEgressLog(row); err != nil {
			t.Fatalf("InsertEgressLog row %d: %v", i, err)
		}
		prevTail = row.RowHash
	}
	if chain.Tail() != prevTail {
		t.Errorf("chain tail = %q, want %q", chain.Tail(), prevTail)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	db := openDB(t)
	chain, err := NewHashChain(db)
	if err != nil {
		t.Fatalf("NewHashChain: %v", err)
	}
	for i := 0; i < 5; i++ {
		row := chain.ComputeRow(makeRow(time.Now().UTC().Add(time.Duration(i)*time.Millisecond), "api.openai.com"))
		if _, err := db.InsertEgressLog(row); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Untampered chain should verify OK.
	res, err := Verify(db)
	if err != nil {
		t.Fatalf("Verify clean: %v", err)
	}
	if !res.OK {
		t.Fatalf("clean chain reported tampered: %s", res.Detail)
	}

	// Tamper: flip the destination on row 3 after the fact.
	if _, err := db.Conn().Exec(`UPDATE egress_logs SET destination = 'evil' WHERE id = 3`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	res, err = Verify(db)
	if err != nil {
		t.Fatalf("Verify tampered: %v", err)
	}
	if res.OK {
		t.Fatal("tampered chain reported OK")
	}
	if res.BadRowID != 3 {
		t.Errorf("BadRowID = %d, want 3", res.BadRowID)
	}
}

func TestHashChainResumesFromDB(t *testing.T) {
	db := openDB(t)
	c1, _ := NewHashChain(db)
	row := c1.ComputeRow(makeRow(time.Now().UTC(), "api.openai.com"))
	if _, err := db.InsertEgressLog(row); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Fresh chain must read the tail from the DB.
	c2, err := NewHashChain(db)
	if err != nil {
		t.Fatalf("second NewHashChain: %v", err)
	}
	if c2.Tail() != row.RowHash {
		t.Errorf("resumed tail = %q, want %q", c2.Tail(), row.RowHash)
	}

	next := c2.ComputeRow(makeRow(time.Now().UTC().Add(10*time.Millisecond), "api.openai.com"))
	if next.PrevHash != row.RowHash {
		t.Errorf("new row prev = %q, want %q", next.PrevHash, row.RowHash)
	}
}

func TestVerifyAcrossPurgeAnchor(t *testing.T) {
	db := openDB(t)
	chain, _ := NewHashChain(db)

	// Insert 5 rows with timestamps far in the past, then 3 fresh rows.
	old := time.Now().UTC().AddDate(0, 0, -10)
	for i := 0; i < 5; i++ {
		row := chain.ComputeRow(makeRow(old.Add(time.Duration(i)*time.Millisecond), "api.openai.com"))
		if _, err := db.InsertEgressLog(row); err != nil {
			t.Fatalf("insert old: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		row := chain.ComputeRow(makeRow(time.Now().UTC().Add(time.Duration(i)*time.Millisecond), "api.openai.com"))
		if _, err := db.InsertEgressLog(row); err != nil {
			t.Fatalf("insert fresh: %v", err)
		}
	}

	// Purge rows older than 3 days — should keep the 3 fresh rows and drop 5 old rows,
	// creating an anchor.
	n, err := db.PurgeEgress(3)
	if err != nil {
		t.Fatalf("PurgeEgress: %v", err)
	}
	if n != 5 {
		t.Fatalf("purged %d rows, want 5", n)
	}

	res, err := Verify(db)
	if err != nil {
		t.Fatalf("Verify post-purge: %v", err)
	}
	if !res.OK {
		t.Fatalf("post-purge verify failed: %s", res.Detail)
	}
	if res.Anchors == 0 {
		t.Error("expected at least 1 anchor")
	}
}
