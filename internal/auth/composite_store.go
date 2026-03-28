package auth

import "crypto/ed25519"

// CompositeKeyStore tries multiple KeyStores in order, returning the first
// successful result. This allows combining FileKeyStore and DBKeyStore so
// that keys registered via either mechanism are found.
type CompositeKeyStore struct {
	stores []KeyStore
}

// NewCompositeKeyStore creates a store that queries each inner store in order.
func NewCompositeKeyStore(stores ...KeyStore) *CompositeKeyStore {
	return &CompositeKeyStore{stores: stores}
}

// PublicKey returns the public key from the first store that has it.
func (c *CompositeKeyStore) PublicKey(agentID string) (ed25519.PublicKey, error) {
	var lastErr error
	for _, s := range c.stores {
		key, err := s.PublicKey(agentID)
		if err == nil {
			return key, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
