package compliance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/itdar/shield-agent/internal/storage"
)

// HashChain maintains the running prev_hash for the egress_logs chain and
// computes row_hash = SHA256(prev_hash || canonical(row_data)). It recovers
// the previous row_hash from storage at construction so the chain survives
// restarts and SIGHUP chain swaps.
//
// Thread-safety: Next must be called sequentially for a single logical
// append-order. The egress pipeline ensures this by chaining through a
// single LogWriter goroutine-free caller (Enqueue is the serialization
// point because the writer consumes them in order).
// Callers must use the embedded mutex when they compute+enqueue from
// multiple goroutines; see ComputeRow.
type HashChain struct {
	mu       sync.Mutex
	prevHash string
}

// NewHashChain initialises a chain, seeding prev_hash from the most
// recent egress_logs row (or the newest anchor if the log has been purged).
func NewHashChain(db *storage.DB) (*HashChain, error) {
	last, err := db.LastEgressRowHash()
	if err != nil {
		return nil, fmt.Errorf("loading last row hash: %w", err)
	}
	return &HashChain{prevHash: last}, nil
}

// ComputeRow fills in PrevHash and RowHash on the given log row, using
// the current chain tail, then advances the tail. It returns the updated
// row so the caller can pass it straight to LogWriter.Enqueue.
//
// ComputeRow holds the chain mutex for the duration of the call, so
// concurrent callers get a deterministic ordering that matches what the
// writer goroutine will see.
func (h *HashChain) ComputeRow(row storage.EgressLog) storage.EgressLog {
	h.mu.Lock()
	defer h.mu.Unlock()
	row.PrevHash = h.prevHash
	row.RowHash = canonicalRowHash(h.prevHash, row)
	h.prevHash = row.RowHash
	return row
}

// Tail returns the current chain tail hash (for metrics, Prometheus
// `shield_agent_egress_hashchain_length` derivative, and tests).
func (h *HashChain) Tail() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.prevHash
}

// canonicalRowHash serialises the fields that participate in the chain
// hash. The canonical form is a tab-separated string of the fields in
// a fixed order. Any new column added to egress_logs must be appended
// here AND to the verifier, or the chain check will break.
func canonicalRowHash(prevHash string, r storage.EgressLog) string {
	// Timestamp is serialised with nanosecond precision so two rows in
	// the same millisecond produce distinct hashes.
	parts := []string{
		prevHash,
		r.Timestamp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		r.CorrelationID,
		r.Provider,
		r.Model,
		r.Method,
		r.Protocol,
		r.Destination,
		strconv.Itoa(r.StatusCode),
		strconv.FormatInt(r.RequestSize, 10),
		strconv.FormatInt(r.ResponseSize, 10),
		strconv.FormatFloat(r.LatencyMs, 'f', -1, 64),
		r.ContentClass,
		r.PromptHash,
		boolStr(r.PIIDetected),
		boolStr(r.PIIScrubbed),
		r.PolicyAction,
		r.PolicyRule,
		boolStr(r.AIGeneratedTag),
		r.ErrorDetail,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\t")))
	return hex.EncodeToString(sum[:])
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// VerifyResult captures chain verification outcome.
type VerifyResult struct {
	OK          bool
	RowsChecked int
	Anchors     int
	BadRowID    int64
	Detail      string
}

// Verify walks every row in egress_logs in id order via a DB cursor,
// reconciling against anchor boundaries, and confirms each row's
// prev_hash chains from the previous row/anchor and each row_hash
// matches a recomputation. The cursor-based walk means a 10M-row log
// is checked with bounded memory.
//
// Returns (result, nil) when verification completed (check result.OK for
// pass/fail). Returns a non-nil error only for DB-level failures.
func Verify(db *storage.DB) (VerifyResult, error) {
	anchors, err := db.ListEgressAnchors()
	if err != nil {
		return VerifyResult{}, fmt.Errorf("loading anchors: %w", err)
	}

	expected := ""
	anchorIdx := 0
	rowsChecked := 0
	var badRowID int64
	var badDetail string

	walkErr := db.ScanEgressLogsAsc(0, func(row storage.EgressLog) error {
		// Advance across any anchors whose next_row_id matches this row —
		// this means the chain segment immediately before us was purged,
		// so our prev_hash should chain from the anchor's chain_hash.
		for anchorIdx < len(anchors) && anchors[anchorIdx].NextRowID == row.ID {
			expected = anchors[anchorIdx].ChainHash
			anchorIdx++
		}

		if row.PrevHash != expected {
			badRowID = row.ID
			badDetail = fmt.Sprintf("prev_hash mismatch at row %d: expected %q, got %q", row.ID, expected, row.PrevHash)
			return errVerifyBad
		}

		recomputed := canonicalRowHash(row.PrevHash, row)
		if row.RowHash != recomputed {
			badRowID = row.ID
			badDetail = fmt.Sprintf("row_hash mismatch at row %d: expected %q, got %q", row.ID, recomputed, row.RowHash)
			return errVerifyBad
		}

		expected = row.RowHash
		rowsChecked++
		return nil
	})

	if walkErr != nil && walkErr != errVerifyBad {
		return VerifyResult{}, fmt.Errorf("scanning egress logs: %w", walkErr)
	}
	if walkErr == errVerifyBad {
		return VerifyResult{
			OK:          false,
			RowsChecked: rowsChecked + 1, // include the bad row
			Anchors:     len(anchors),
			BadRowID:    badRowID,
			Detail:      badDetail,
		}, nil
	}

	return VerifyResult{
		OK:          true,
		RowsChecked: rowsChecked,
		Anchors:     len(anchors),
		Detail:      fmt.Sprintf("OK (%d entries verified, %d anchors)", rowsChecked, len(anchors)),
	}, nil
}

// errVerifyBad is a sentinel used to break out of ScanEgressLogsAsc early
// without treating the break as a DB error.
var errVerifyBad = fmt.Errorf("verify sentinel: mismatch")
