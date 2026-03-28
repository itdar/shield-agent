package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"

	"github.com/itdar/shield-agent/internal/storage"
)

// DBKeyStore resolves Ed25519 public keys from the SQLite agent_keys table.
type DBKeyStore struct {
	db *storage.DB
}

// NewDBKeyStore creates a DBKeyStore backed by db.
func NewDBKeyStore(db *storage.DB) *DBKeyStore {
	return &DBKeyStore{db: db}
}

// PublicKey returns the Ed25519 public key for agentID from the database.
func (s *DBKeyStore) PublicKey(agentID string) (ed25519.PublicKey, error) {
	ak, err := s.db.GetAgentKey(agentID)
	if err != nil || ak == nil {
		return nil, fmt.Errorf("agent %q not found in DB key store", agentID)
	}
	raw, err := base64.StdEncoding.DecodeString(ak.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("agent %q: invalid base64 key in DB: %w", agentID, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("agent %q: expected %d bytes, got %d", agentID, ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}
