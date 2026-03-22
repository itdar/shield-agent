package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"
)

// ---- helpers ----------------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 10}))
}

// writeTempYAML writes content to a temp file and returns its path.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "keys-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

// ---- FileKeyStore -----------------------------------------------------------

func TestFileKeyStoreNotFound(t *testing.T) {
	ks, err := NewFileKeyStore("/tmp/this-file-does-not-exist-rua-test.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	_, err = ks.PublicKey("any-agent")
	if err == nil {
		t.Fatal("expected error from PublicKey on empty store, got nil")
	}
}

func TestFileKeyStoreLoad(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(pub)
	yaml := fmt.Sprintf("keys:\n  - id: \"agent-1\"\n    key: %q\n", b64)
	path := writeTempYAML(t, yaml)

	ks, err := NewFileKeyStore(path)
	if err != nil {
		t.Fatalf("NewFileKeyStore: %v", err)
	}
	got, err := ks.PublicKey("agent-1")
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	if !got.Equal(pub) {
		t.Errorf("returned key does not match original")
	}
}

func TestFileKeyStoreInvalidBase64(t *testing.T) {
	yaml := "keys:\n  - id: \"bad\"\n    key: \"!!!not-base64!!!\"\n"
	path := writeTempYAML(t, yaml)

	_, err := NewFileKeyStore(path)
	if err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}
}

func TestFileKeyStoreWrongKeySize(t *testing.T) {
	// 16 bytes encoded in base64 — too short for ed25519 (needs 32)
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	yaml := fmt.Sprintf("keys:\n  - id: \"short\"\n    key: %q\n", short)
	path := writeTempYAML(t, yaml)

	_, err := NewFileKeyStore(path)
	if err == nil {
		t.Fatal("expected error for wrong key size, got nil")
	}
}

// ---- CachedKeyStore ---------------------------------------------------------

// countingStore is a KeyStore that counts calls to PublicKey.
type countingStore struct {
	key   ed25519.PublicKey
	calls int
}

func (c *countingStore) PublicKey(_ string) (ed25519.PublicKey, error) {
	c.calls++
	return c.key, nil
}

func TestCachedKeyStoreHit(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	inner := &countingStore{key: pub}
	cached := NewCachedKeyStore(inner, time.Minute)

	_, _ = cached.PublicKey("agent-1")
	_, _ = cached.PublicKey("agent-1")

	if inner.calls != 1 {
		t.Errorf("expected inner called once, got %d", inner.calls)
	}
}

func TestCachedKeyStoreExpiry(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	inner := &countingStore{key: pub}
	cached := NewCachedKeyStore(inner, -time.Second) // negative TTL → always expired

	_, _ = cached.PublicKey("agent-1")
	_, _ = cached.PublicKey("agent-1")

	if inner.calls != 2 {
		t.Errorf("expected inner called twice (expired cache), got %d", inner.calls)
	}
}

// ---- ResolveDIDKey ----------------------------------------------------------

func TestResolveDIDKeyUnsupported(t *testing.T) {
	_, err := ResolveDIDKey("did:web:example.com")
	if err == nil {
		t.Fatal("expected error for unsupported DID method, got nil")
	}
}

func TestResolveDIDKeyValid(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Encode as did:key: prepend multicodec prefix 0xed 0x01, base58btc encode.
	raw := append([]byte{0xed, 0x01}, []byte(pub)...)
	encoded := base58Encode(raw)
	did := "did:key:z" + encoded

	got, err := ResolveDIDKey(did)
	if err != nil {
		t.Fatalf("ResolveDIDKey: %v", err)
	}
	if !got.Equal(pub) {
		t.Errorf("round-trip key mismatch")
	}
}

// base58Encode encodes bytes using the Bitcoin/IPFS base58 alphabet.
func base58Encode(input []byte) string {
	alphabet := []byte(base58Alphabet)

	// Count leading zeros.
	leadingZeros := 0
	for _, b := range input {
		if b == 0 {
			leadingZeros++
		} else {
			break
		}
	}

	// Convert to big integer (big-endian input → little-endian digits).
	digits := []byte{0}
	for _, b := range input {
		carry := int(b)
		for i := 0; i < len(digits); i++ {
			carry += 256 * int(digits[i])
			digits[i] = byte(carry % 58)
			carry /= 58
		}
		for carry > 0 {
			digits = append(digits, byte(carry%58))
			carry /= 58
		}
	}

	// Build result string (digits are little-endian, so reverse).
	result := make([]byte, leadingZeros+len(digits))
	for i := range leadingZeros {
		result[i] = alphabet[0]
	}
	for i, d := range digits {
		result[len(result)-1-i] = alphabet[d]
	}
	return string(result)
}

// ---- AgentIDHash ------------------------------------------------------------

func TestAgentIDHash(t *testing.T) {
	h1 := AgentIDHash("agent-alpha")
	h2 := AgentIDHash("agent-alpha")
	h3 := AgentIDHash("agent-beta")

	if h1 != h2 {
		t.Errorf("same input produced different hashes: %q vs %q", h1, h2)
	}
	if h1 == h3 {
		t.Errorf("different inputs produced same hash: %q", h1)
	}
	// Verify it's the hex of sha256.
	sum := sha256.Sum256([]byte("agent-alpha"))
	want := hex.EncodeToString(sum[:])
	if h1 != want {
		t.Errorf("AgentIDHash = %q, want %q", h1, want)
	}
}
