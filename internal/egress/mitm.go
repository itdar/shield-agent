package egress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// MITMMinter produces TLS server certificates on the fly, signed by the
// shield-agent CA, so the proxy can terminate TLS from the client side
// while re-originating a second TLS connection to the real upstream.
//
// Phase 2 only. Leaves are cached per host (key = lowercased SNI) with
// a configurable TTL. Cache entries are time-based — on expiry the next
// request synchronously re-mints.
type MITMMinter struct {
	ca       *CA
	validity time.Duration
	cacheTTL time.Duration

	mu    sync.RWMutex
	cache map[string]mintedLeaf
}

type mintedLeaf struct {
	cert    *tls.Certificate
	expires time.Time
}

// NewMITMMinter builds a minter. leafValidity defaults to 30 days
// if zero; cacheTTL defaults to 60 minutes.
func NewMITMMinter(ca *CA, leafValidity, cacheTTL time.Duration) *MITMMinter {
	if leafValidity <= 0 {
		leafValidity = 30 * 24 * time.Hour
	}
	if cacheTTL <= 0 {
		cacheTTL = 60 * time.Minute
	}
	return &MITMMinter{
		ca:       ca,
		validity: leafValidity,
		cacheTTL: cacheTTL,
		cache:    make(map[string]mintedLeaf),
	}
}

// CertificateFor returns a TLS certificate valid for the given host.
// Concurrent callers for the same host may race to mint; the result is
// harmless because both certs are functionally interchangeable. The last
// one wins in the cache.
func (m *MITMMinter) CertificateFor(host string) (*tls.Certificate, error) {
	key := strings.ToLower(host)

	m.mu.RLock()
	entry, ok := m.cache[key]
	m.mu.RUnlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.cert, nil
	}

	cert, err := m.mint(key)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.cache[key] = mintedLeaf{cert: cert, expires: time.Now().Add(m.cacheTTL)}
	m.mu.Unlock()
	return cert, nil
}

// GetCertificate is a drop-in tls.Config.GetCertificate hook.
func (m *MITMMinter) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName
	if host == "" {
		// Fallback: the client didn't send SNI. Use the LocalAddr the
		// conn was accepted on; not perfect but still better than
		// failing the handshake.
		if hello.Conn != nil {
			if a, ok := hello.Conn.LocalAddr().(*net.TCPAddr); ok {
				host = a.IP.String()
			}
		}
	}
	if host == "" {
		return nil, fmt.Errorf("mitm: empty SNI, cannot mint certificate")
	}
	return m.CertificateFor(host)
}

func (m *MITMMinter) mint(host string) (*tls.Certificate, error) {
	// Reject plainly invalid hostnames so we don't sign junk.
	if !validMintableHost(host) {
		return nil, fmt.Errorf("mitm: invalid host %q", host)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("mitm: generate leaf key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("mitm: serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(m.validity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, m.ca.Cert, &leafKey.PublicKey, m.ca.Key)
	if err != nil {
		return nil, fmt.Errorf("mitm: create leaf: %w", err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{der, m.ca.Cert.Raw},
		PrivateKey:  leafKey,
		Leaf:        nil, // parsed lazily by crypto/tls
	}, nil
}

// validMintableHost restricts mintable hostnames to ASCII-safe DNS labels
// or IPs. Keeps an attacker from coaxing us into signing a giant or
// control-character-laden name.
func validMintableHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, r := range host {
		if r == '.' || r == '-' || r == '_' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		return false
	}
	return true
}
