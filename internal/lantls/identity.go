// Package lantls lets a signed-in client use the API server's local-network
// address without a public certificate and without trusting that address on
// its word. The API listener answers TLS and plain HTTP on the same port; the
// TLS side presents a self-signed certificate whose key is derived from
// SECRET_KEY, and the authenticated connections document publishes that key's
// pin. A client learns the pin over the address it already trusts and accepts
// a LAN address only when TLS there presents the same key
// (docs/architecture/server-identity.md, "Local network TLS").
//
// The key is derived, not stored: every API process and replica of a
// deployment shares SECRET_KEY and so presents the same key, nothing new lives
// in the database, and a SECRET_KEY rotation replaces the key, which clients
// pick up from the connections document the next time they read it.
package lantls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
)

const (
	// hkdfInfo domain-separates the TLS key from every other use of the
	// master key (the data-encryption key uses "silo/data-encryption/v1").
	hkdfInfo = "silo/lan-tls/v1"

	// maxDeriveAttempts bounds the search for a P-256 scalar. A 32-byte HKDF
	// output is out of range with probability below 2^-32, so a second
	// attempt is already vanishingly rare.
	maxDeriveAttempts = 16

	// certificateLifetime only has to outlast any process: clients pin the
	// key and never evaluate the certificate's dates or names.
	certificateLifetime = 10 * 365 * 24 * time.Hour

	// alpnHTTP1 is the only application protocol the listener offers.
	alpnHTTP1 = "http/1.1"
)

// Identity is the deployment's local-network TLS identity: the derived key,
// a self-signed certificate for it, and the pin clients compare against.
type Identity struct {
	certificate tls.Certificate
	pin         string
}

// New derives the identity from the master key (the raw SECRET_KEY value).
// The same master key always yields the same key and pin; the certificate
// around it is minted per process and carries no meaning of its own.
func New(masterKey []byte) (*Identity, error) {
	if len(masterKey) < secret.MinMasterKeyLen {
		return nil, fmt.Errorf("lantls: master key must be at least %d bytes, got %d", secret.MinMasterKeyLen, len(masterKey))
	}
	key, err := deriveKey(masterKey)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("lantls: certificate serial: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Silo local network"},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(certificateLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("lantls: create certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("lantls: parse certificate: %w", err)
	}
	return &Identity{
		certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		pin:         Pin(leaf),
	}, nil
}

// Pin is the value clients compare: the base64 SHA-256 of the certificate's
// SubjectPublicKeyInfo, the same form HTTP public key pinning used.
func Pin(certificate *x509.Certificate) string {
	sum := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// Pin returns the identity's pin.
func (i *Identity) Pin() string { return i.pin }

// Config is the server TLS configuration for the API listener. HTTP/1.1 only:
// the listener hands connections to an http.Server through Serve, which does
// not set up HTTP/2 for connections it did not accept through ServeTLS.
func (i *Identity) Config() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{i.certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{alpnHTTP1},
	}
}

// deriveKey turns HKDF output into a P-256 private key, moving to the next
// counter in the rare case the output is not a valid scalar.
func deriveKey(masterKey []byte) (*ecdsa.PrivateKey, error) {
	var lastErr error
	for attempt := range maxDeriveAttempts {
		info := hkdfInfo
		if attempt > 0 {
			info = fmt.Sprintf("%s#%d", hkdfInfo, attempt)
		}
		scalar, err := hkdf.Key(sha256.New, masterKey, nil /* salt */, info, 32)
		if err != nil {
			return nil, fmt.Errorf("lantls: derive key: %w", err)
		}
		key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar)
		if err == nil {
			return key, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("lantls: derive key: no valid scalar after %d attempts: %w", maxDeriveAttempts, lastErr)
}
