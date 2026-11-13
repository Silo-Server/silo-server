package streamtoken

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// A JWT's claims are readable by anyone who holds the token, and stream and
// download tokens travel in URLs clients hold. The media path is sealed with
// AES-256-GCM under a key derived from the signing secret, which every server
// and node that verifies tokens already shares.
const mediaPathKeyInfo = "silo/stream-token-media-path/v1"

func mediaPathAEAD(secret string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, []byte(secret), nil, mediaPathKeyInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealMediaPath encrypts path as base64url(nonce || ciphertext).
func sealMediaPath(path, secret string) (string, error) {
	aead, err := mediaPathAEAD(secret)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(path), nil)), nil
}

var errSealedMediaPath = errors.New("sealed media path does not open")

func openMediaPath(sealed, secret string) (string, error) {
	aead, err := mediaPathAEAD(secret)
	if err != nil {
		return "", err
	}
	data, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(data) < aead.NonceSize() {
		return "", errSealedMediaPath
	}
	path, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
	if err != nil {
		return "", errSealedMediaPath
	}
	return string(path), nil
}
