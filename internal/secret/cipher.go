// Package secret provides at-rest symmetric encryption for server-owned
// credentials (arr API keys, S3 keys, sensitive server_settings, per-table
// access tokens). Callers store the resulting ciphertext directly in the column
// that previously held the plaintext credential and decrypt on read, so the
// secret never lives in the database as naked text.
//
// The one primitive everything reuses is AES-256-GCM with a random 12-byte
// nonce and a versioned envelope (enc:v1:<base64url(nonce‖sealed)>). The 32-byte
// data key is HKDF-SHA256 derived from a master key (the SECRET_KEY env value)
// with a domain-separation label, so the key that protects the data lives
// outside Postgres and encrypted secrets survive a full database dump.
//
// Each ciphertext is GCM-bound to its logical row via additional-authenticated
// data (AAD) — "table:column:<pk>" (or "server_settings:<key>") — so a
// DB-write attacker cannot transplant a credential blob into another row or
// column: decrypting with a different AAD fails authentication.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	// envelopePrefix tags a value as v1 ciphertext. It is exactly 7 ASCII
	// characters, is not valid base64url, and does not collide with any real
	// credential prefix (sa_ tokens, UUIDs, JWTs, URLs, integers) in practice.
	envelopePrefix = "enc:v1:"

	// hkdfInfo domain-separates the derived data key from any other use of the
	// master key. Bump it alongside the envelope version on a future key
	// rotation (enc:v2: with "silo/data-encryption/v2").
	hkdfInfo = "silo/data-encryption/v1"

	// MinMasterKeyLen is the minimum acceptable master-key length in bytes. The
	// bootstrap loader enforces the same floor on SECRET_KEY before New is ever
	// called.
	MinMasterKeyLen = 32

	// dataKeyLen is the AES-256 key size.
	dataKeyLen = 32
)

// ErrUnknownVersion is returned by Decrypt when the envelope carries a version
// this build does not understand. It is the forward-compatibility hook for a
// future enc:v2: key rotation: an older binary surfaces an explicit error
// instead of silently mishandling a newer ciphertext.
var ErrUnknownVersion = errors.New("secret: unknown ciphertext version")

// Cipher encrypts and decrypts short credential strings with AES-256-GCM. The
// data key is derived once in New and the AEAD is built once and reused, so the
// hot path is a single Seal/Open with no per-call key schedule. A Cipher is
// safe for concurrent use by multiple goroutines.
type Cipher struct {
	gcm cipher.AEAD
}

// New derives a Cipher from the master key (the raw SECRET_KEY value). The key
// must be at least MinMasterKeyLen bytes; New returns an error otherwise so a
// short or empty key can never silently produce a weak cipher.
func New(masterKey []byte) (*Cipher, error) {
	if len(masterKey) < MinMasterKeyLen {
		return nil, fmt.Errorf("secret: master key must be at least %d bytes, got %d", MinMasterKeyLen, len(masterKey))
	}
	dataKey, err := hkdf.Key(sha256.New, masterKey, nil /* salt */, hkdfInfo, dataKeyLen)
	if err != nil {
		return nil, fmt.Errorf("secret: derive data key: %w", err)
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, fmt.Errorf("secret: new aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: new gcm: %w", err)
	}
	return &Cipher{gcm: gcm}, nil
}

// Encrypt seals plaintext under the data key, binding the ciphertext to aad (the
// row-identity string the matching Decrypt must supply). An empty plaintext
// returns ("", nil): an empty secret is never wrapped in an envelope, so an
// absent credential stays an empty column value. The result is
// enc:v1:<base64url(nonce‖sealed)>.
func (c *Cipher) Encrypt(plaintext, aad string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secret: read nonce: %w", err)
	}
	// Seal appends the ciphertext to nonce, so the returned slice is nonce‖sealed.
	sealed := c.gcm.Seal(nonce, nonce, []byte(plaintext), []byte(aad))
	return envelopePrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decrypt opens an enc:v1: envelope produced by Encrypt with the same aad. Any
// failure — unknown version, malformed base64, truncation, tampering, the wrong
// key, or an aad mismatch — returns an error. Decrypt never falls back to
// returning the input ciphertext or an empty string; passing through
// non-prefixed legacy plaintext is the caller's responsibility (the read-path
// contract), not Decrypt's.
func (c *Cipher) Decrypt(ciphertext, aad string) (string, error) {
	// Parse "enc:<version>:<body>" and dispatch on the version so a future
	// enc:v2: can be added without touching existing call sites.
	parts := strings.SplitN(ciphertext, ":", 3)
	if len(parts) != 3 || parts[0] != "enc" {
		return "", fmt.Errorf("secret: not an enc envelope")
	}
	switch parts[1] {
	case "v1":
		return c.openV1(parts[2], aad)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownVersion, parts[1])
	}
}

// openV1 decodes and authenticates a v1 body (the part after "enc:v1:").
func (c *Cipher) openV1(encoded, aad string) (string, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("secret: decode ciphertext: %w", err)
	}
	nonceSize := c.gcm.NonceSize()
	if len(sealed) < nonceSize {
		return "", fmt.Errorf("secret: ciphertext too short")
	}
	nonce, body := sealed[:nonceSize], sealed[nonceSize:]
	plaintext, err := c.gcm.Open(nil, nonce, body, []byte(aad))
	if err != nil {
		return "", fmt.Errorf("secret: authenticate ciphertext: %w", err)
	}
	return string(plaintext), nil
}

// IsEncrypted reports whether s carries the v1 envelope prefix. It is a cheap
// prefix check, not a validity check: a value that begins with enc:v1: but
// holds a corrupt body still reports true here and then fails Decrypt — it never
// silently degrades to being used as a plaintext credential.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, envelopePrefix)
}

// DecryptIfEncrypted is the canonical read-path primitive shared by every
// decrypt site (the settings decorator and the per-table repos). It implements
// the read-path contract:
//
//  1. an empty value returns "" unchanged;
//  2. a non-enveloped (legacy plaintext) value is returned unchanged — during
//     the backfill window a not-yet-encrypted credential is no worse than today;
//  3. an enc:v1: value is decrypted, and any failure (wrong key, tamper,
//     truncation) is returned, never swallowed and never returned as the
//     ciphertext string.
//
// This reconciles best-effort backfill (a skipped/failed row stays readable as
// plaintext) with the hard guarantee that a real enc:v1: value never silently
// degrades into using ciphertext as a credential.
func (c *Cipher) DecryptIfEncrypted(value, aad string) (string, error) {
	if value == "" || !IsEncrypted(value) {
		return value, nil
	}
	return c.Decrypt(value, aad)
}

// SettingsAAD returns the additional-authenticated-data string binding a
// server_settings ciphertext to its key. Read and write sites must both use it
// so the AAD matches; centralizing it here keeps the convention typo-proof
// across the settings decorator, the config watcher, and other settings
// readers.
func SettingsAAD(key string) string {
	return "server_settings:" + key
}

// RowAAD returns the additional-authenticated-data string binding a ciphertext
// to one logical row: table:column:<pk>. Binding to row identity stops a
// DB-write attacker from transplanting a credential blob into another row or
// column — decrypting with a different (table, column, pk) fails authentication.
func RowAAD(table, column, pk string) string {
	return table + ":" + column + ":" + pk
}

// EncryptIfPlaintext encrypts s only when it is non-empty, non-encrypted
// plaintext, returning (ciphertext, true, nil). An already-encrypted or empty
// value is returned unchanged with changed=false. This is the idempotent
// primitive the startup backfill uses to sweep a column without ever
// double-encrypting an already-wrapped value.
func (c *Cipher) EncryptIfPlaintext(s, aad string) (string, bool, error) {
	if s == "" || IsEncrypted(s) {
		return s, false, nil
	}
	ct, err := c.Encrypt(s, aad)
	if err != nil {
		return s, false, err
	}
	return ct, true, nil
}
