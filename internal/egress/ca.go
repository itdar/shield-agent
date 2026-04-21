package egress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CA bundles the shield-agent private CA used to sign per-host leaf
// certificates during TLS MITM. It is the single source of truth for the
// signing key; all MITM leaf certs chain back to a CA.Cert parsed from
// the PEM file on disk.
//
// Phase 2 only. Phase 1 never touches CA material.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	Key     *ecdsa.PrivateKey
	KeyPEM  []byte
}

// CAOptions controls how LoadOrGenerate behaves.
type CAOptions struct {
	// CertPath / KeyPath are the on-disk locations. Both are required.
	CertPath string
	KeyPath  string
	// Generate: when true and the files are missing, create a fresh CA
	// at those paths. When false, missing files are an error — this is
	// the safe default for production where the operator pre-provisions.
	Generate bool
	// ValidityDays for a freshly generated CA. Zero means default 10y.
	ValidityDays int
}

// LoadOrGenerate reads the CA PEM pair from disk. If Generate is true
// and either file is missing, it creates a new CA (both files written
// with 0600 perms) and returns it.
func LoadOrGenerate(opts CAOptions) (*CA, error) {
	if opts.CertPath == "" || opts.KeyPath == "" {
		return nil, fmt.Errorf("ca: CertPath and KeyPath required")
	}
	if !fileExists(opts.CertPath) || !fileExists(opts.KeyPath) {
		if !opts.Generate {
			return nil, fmt.Errorf("ca: cert or key missing at %q / %q", opts.CertPath, opts.KeyPath)
		}
		return GenerateCA(opts.CertPath, opts.KeyPath, opts.ValidityDays)
	}
	certPEM, err := os.ReadFile(opts.CertPath)
	if err != nil {
		return nil, fmt.Errorf("ca: read cert: %w", err)
	}
	keyPEM, err := os.ReadFile(opts.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("ca: read key: %w", err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("ca: %s is not a PEM CERTIFICATE", opts.CertPath)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse cert: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("ca: %s is not a PEM key", opts.KeyPath)
	}
	var key *ecdsa.PrivateKey
	switch keyBlock.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(keyBlock.Bytes)
	case "PRIVATE KEY":
		parsed, perr := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if perr != nil {
			err = perr
		} else {
			ec, ok := parsed.(*ecdsa.PrivateKey)
			if !ok {
				err = fmt.Errorf("ca: PKCS8 key is not ECDSA (got %T)", parsed)
			} else {
				key = ec
			}
		}
	default:
		return nil, fmt.Errorf("ca: unsupported key PEM type %q", keyBlock.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("ca: parse key: %w", err)
	}
	return &CA{Cert: cert, CertPEM: certPEM, Key: key, KeyPEM: keyPEM}, nil
}

// GenerateCA creates a fresh ECDSA-P256 CA, writes the PEM pair to disk,
// and returns the live CA object. Files are written with 0600 perms.
func GenerateCA(certPath, keyPath string, validityDays int) (*CA, error) {
	if validityDays <= 0 {
		validityDays = 3650
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("ca: serial: %w", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "shield-agent egress CA",
			Organization: []string{"shield-agent"},
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().AddDate(0, 0, validityDays),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca: create cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: re-parse cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ca: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return nil, fmt.Errorf("ca: mkdir cert dir: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("ca: write cert: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return nil, fmt.Errorf("ca: mkdir key dir: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("ca: write key: %w", err)
	}
	return &CA{Cert: cert, CertPEM: certPEM, Key: key, KeyPEM: keyPEM}, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
