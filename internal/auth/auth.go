package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// KeyStore resolves Ed25519 public keys by agent ID.
type KeyStore interface {
	PublicKey(agentID string) (ed25519.PublicKey, error)
}

// keyEntry is a single entry in a keys YAML file.
type keyEntry struct {
	ID  string `yaml:"id"`
	Key string `yaml:"key"`
}

// keysFile is the top-level structure of a keys YAML file.
type keysFile struct {
	Keys []keyEntry `yaml:"keys"`
}

// FileKeyStore loads Ed25519 public keys from a YAML file.
type FileKeyStore struct {
	keys map[string]ed25519.PublicKey
}

// NewFileKeyStore loads keys from path. A missing file is treated as an empty
// store and is not an error.
func NewFileKeyStore(path string) (*FileKeyStore, error) {
	fs := &FileKeyStore{keys: make(map[string]ed25519.PublicKey)}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fs, nil
		}
		return nil, fmt.Errorf("reading key store %q: %w", path, err)
	}

	var kf keysFile
	if err := yaml.Unmarshal(data, &kf); err != nil {
		return nil, fmt.Errorf("parsing key store %q: %w", path, err)
	}

	for _, e := range kf.Keys {
		raw, err := base64.StdEncoding.DecodeString(e.Key)
		if err != nil {
			return nil, fmt.Errorf("key %q: invalid base64: %w", e.ID, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("key %q: expected %d bytes, got %d", e.ID, ed25519.PublicKeySize, len(raw))
		}
		fs.keys[e.ID] = ed25519.PublicKey(raw)
	}
	return fs, nil
}

// PublicKey returns the public key for agentID, or an error if not found.
func (fs *FileKeyStore) PublicKey(agentID string) (ed25519.PublicKey, error) {
	k, ok := fs.keys[agentID]
	if !ok {
		return nil, fmt.Errorf("agent %q not found in key store", agentID)
	}
	return k, nil
}

// cacheEntry holds a cached key with its expiry time.
type cacheEntry struct {
	key    ed25519.PublicKey
	err    error
	expiry time.Time
}

// CachedKeyStore wraps a KeyStore with a TTL cache.
type CachedKeyStore struct {
	inner KeyStore
	ttl   time.Duration
	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewCachedKeyStore wraps inner with a TTL-based cache.
func NewCachedKeyStore(inner KeyStore, ttl time.Duration) *CachedKeyStore {
	return &CachedKeyStore{
		inner: inner,
		ttl:   ttl,
		cache: make(map[string]cacheEntry),
	}
}

// PublicKey returns the public key for agentID, using the cache when valid.
func (c *CachedKeyStore) PublicKey(agentID string) (ed25519.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.cache[agentID]; ok && time.Now().Before(e.expiry) {
		return e.key, e.err
	}

	key, err := c.inner.PublicKey(agentID)
	c.cache[agentID] = cacheEntry{key: key, err: err, expiry: time.Now().Add(c.ttl)}
	return key, err
}

// Ed25519 multicodec prefix (0xed, 0x01).
var ed25519Prefix = []byte{0xed, 0x01}

// ResolveDIDKey resolves a did:key:z<base58btc> DID to an Ed25519 public key.
// Only the Ed25519 multicodec prefix (0xed01) is supported.
func ResolveDIDKey(did string) (ed25519.PublicKey, error) {
	const prefix = "did:key:z"
	if !strings.HasPrefix(did, prefix) {
		return nil, fmt.Errorf("unsupported DID method: %q", did)
	}
	encoded := did[len(prefix):]

	// base58btc decode (multibase 'z' prefix).
	raw, err := base58Decode(encoded)
	if err != nil {
		return nil, fmt.Errorf("decoding did:key: %w", err)
	}

	if len(raw) < 2 || raw[0] != ed25519Prefix[0] || raw[1] != ed25519Prefix[1] {
		return nil, fmt.Errorf("unsupported key type in did:key (expected Ed25519 multicodec 0xed01)")
	}

	keyBytes := raw[2:]
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid Ed25519 key length: %d", len(keyBytes))
	}
	return ed25519.PublicKey(keyBytes), nil
}

// base58Alphabet is the Bitcoin/IPFS base58 alphabet.
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Decode(s string) ([]byte, error) {
	alphabet := []byte(base58Alphabet)

	// Build lookup table.
	lookup := [256]int{}
	for i := range lookup {
		lookup[i] = -1
	}
	for i, b := range alphabet {
		lookup[b] = i
	}

	// Convert base58 to big integer represented as []byte (little-endian digits).
	result := []byte{0}
	for _, ch := range []byte(s) {
		digit := lookup[ch]
		if digit < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", ch)
		}
		carry := digit
		for i := 0; i < len(result); i++ {
			carry += 58 * int(result[i])
			result[i] = byte(carry % 256)
			carry /= 256
		}
		for carry > 0 {
			result = append(result, byte(carry%256))
			carry /= 256
		}
	}

	// Count leading '1's (which encode leading zero bytes).
	leadingZeros := 0
	for _, ch := range []byte(s) {
		if ch == '1' {
			leadingZeros++
		} else {
			break
		}
	}

	// Reverse result and prepend leading zeros.
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	out := make([]byte, leadingZeros+len(result))
	copy(out[leadingZeros:], result)
	return out, nil
}

// AgentIDHash returns the sha256 hex of agentID (for anonymization).
func AgentIDHash(agentID string) string {
	h := sha256.Sum256([]byte(agentID))
	return hex.EncodeToString(h[:])
}
