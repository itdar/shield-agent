package compliance

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/storage"
)

// AuditBundle is the JSON shape emitted by `shield-agent logs --export-audit`.
// Regulators receive this in an audit request: it carries the raw rows,
// the hash chain anchors that bracket the export window, the verification
// outcome, and a snapshot of the policy configuration that was in force.
type AuditBundle struct {
	ExportedAt      time.Time            `json:"exported_at"`
	WindowHours     int                  `json:"window_hours"`
	Logs            []storage.EgressLog  `json:"logs"`
	Anchors         []storage.EgressAnchor `json:"anchors"`
	HashChainProof  HashChainProof       `json:"hash_chain_proof"`
	PolicySnapshot  PolicySnapshot       `json:"policy_snapshot"`
	ShieldAgentVer  string               `json:"shield_agent_version"`
}

// HashChainProof summarises what Verify returned when the bundle was built.
// The three fields are reproducible by anyone who re-runs Verify against
// the same rows + anchors, which is the point — regulators can
// independently confirm the chain without trusting shield-agent's word.
type HashChainProof struct {
	OK          bool   `json:"ok"`
	RowsChecked int    `json:"rows_checked"`
	Anchors     int    `json:"anchors"`
	Tail        string `json:"tail"`
	Detail      string `json:"detail,omitempty"`
}

// PolicySnapshot captures the egress policy that was active at export time.
// It is not a historical record — if the policy has changed since the
// oldest log row, the snapshot shows the *current* policy, not the policy
// that gated that row. The row_hash chain is what proves the log itself
// hasn't been tampered with.
type PolicySnapshot struct {
	PolicyMode    string   `json:"policy_mode"`
	UpstreamAllow []string `json:"upstream_allow"`
	MITMHosts     []string `json:"mitm_hosts,omitempty"`
	RetentionDays int      `json:"retention_days"`
}

// BuildAuditBundle composes an AuditBundle covering the last N hours.
// If windowHours <= 0, all rows currently in the database are included.
func BuildAuditBundle(db *storage.DB, cfg config.EgressConfig, windowHours int, version string) (*AuditBundle, error) {
	opts := storage.EgressQueryOptions{Last: 1_000_000}
	if windowHours > 0 {
		opts.Since = time.Duration(windowHours) * time.Hour
	}
	rows, err := db.QueryEgressLogs(opts)
	if err != nil {
		return nil, fmt.Errorf("querying egress logs: %w", err)
	}
	// Chronological order reads better in an audit PDF.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}

	anchors, err := db.ListEgressAnchors()
	if err != nil {
		return nil, fmt.Errorf("listing anchors: %w", err)
	}
	// Normalise nil to empty slices so the JSON output is round-trippable.
	if rows == nil {
		rows = []storage.EgressLog{}
	}
	if anchors == nil {
		anchors = []storage.EgressAnchor{}
	}

	verifyRes, err := Verify(db)
	if err != nil {
		return nil, fmt.Errorf("verifying chain: %w", err)
	}
	tail, err := db.LastEgressRowHash()
	if err != nil {
		return nil, fmt.Errorf("reading chain tail: %w", err)
	}

	snap := PolicySnapshot{
		PolicyMode:    cfg.PolicyMode,
		UpstreamAllow: append([]string(nil), cfg.UpstreamAllow...),
		MITMHosts:     append([]string(nil), cfg.MITMHosts...),
		RetentionDays: cfg.RetentionDays,
	}

	return &AuditBundle{
		ExportedAt:     time.Now().UTC(),
		WindowHours:    windowHours,
		Logs:           rows,
		Anchors:        anchors,
		HashChainProof: HashChainProof{
			OK:          verifyRes.OK,
			RowsChecked: verifyRes.RowsChecked,
			Anchors:     verifyRes.Anchors,
			Tail:        tail,
			Detail:      verifyRes.Detail,
		},
		PolicySnapshot: snap,
		ShieldAgentVer: version,
	}, nil
}

// MarshalIndent serialises the bundle with 2-space indentation so regulator
// tooling can read the file without post-processing.
func (b *AuditBundle) MarshalIndent() ([]byte, error) {
	return json.MarshalIndent(b, "", "  ")
}
