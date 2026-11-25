// Package appkeys holds the wire encoding of app keys: the public signing
// keys an app holds for one account on this server (docs/architecture/app-keys.md).
// It has no database; storage lives in internal/auth.
package appkeys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// PublicKeySize is the length of an uncompressed P-256 point: 0x04, then the
// 32-byte X and Y coordinates.
const PublicKeySize = 65

// keyIDSize is how many bytes of the public key's SHA-256 make its key ID.
const keyIDSize = 16

// ErrInvalidPublicKey is returned for a value that is not the base64url form
// of an uncompressed P-256 point on the curve.
var ErrInvalidPublicKey = errors.New("appkeys: invalid public key")

// encoding is base64url without padding. Strict refuses an encoding whose
// unused trailing bits are set, so each key has exactly one spelling.
var encoding = base64.RawURLEncoding.Strict()

// PublicKey is a validated P-256 public key in its 65-byte uncompressed form.
type PublicKey struct {
	raw [PublicKeySize]byte
}

// ParsePublicKey decodes the base64url (no padding) form of a public key.
func ParsePublicKey(encoded string) (PublicKey, error) {
	if encoding.DecodedLen(len(encoded)) != PublicKeySize {
		return PublicKey{}, ErrInvalidPublicKey
	}
	raw, err := encoding.DecodeString(encoded)
	if err != nil {
		return PublicKey{}, ErrInvalidPublicKey
	}
	return PublicKeyFromBytes(raw)
}

// PublicKeyFromBytes validates the 65-byte uncompressed form: the length,
// the 0x04 prefix, and that the point lies on P-256.
func PublicKeyFromBytes(raw []byte) (PublicKey, error) {
	if len(raw) != PublicKeySize {
		return PublicKey{}, ErrInvalidPublicKey
	}
	if _, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw); err != nil {
		return PublicKey{}, ErrInvalidPublicKey
	}
	var k PublicKey
	copy(k.raw[:], raw)
	return k, nil
}

// Bytes returns a copy of the 65-byte uncompressed point.
func (k PublicKey) Bytes() []byte {
	out := make([]byte, PublicKeySize)
	copy(out, k.raw[:])
	return out
}

// String is the key's wire form: base64url without padding.
func (k PublicKey) String() string {
	return encoding.EncodeToString(k.raw[:])
}

// KeyID is the key's identifier: base64url without padding of the first 16
// bytes of SHA-256 of the 65-byte point.
func (k PublicKey) KeyID() string {
	sum := sha256.Sum256(k.raw[:])
	return encoding.EncodeToString(sum[:keyIDSize])
}

// TokenHash is how a 32-byte token is named where the token itself must not
// be kept: base64url of SHA-256 over the bytes the base64url token decodes
// to.
func TokenHash(token string) (string, error) {
	raw, err := encoding.DecodeString(token)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return encoding.EncodeToString(sum[:]), nil
}
