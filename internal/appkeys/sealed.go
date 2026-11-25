package appkeys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

// SealedReply is an answer only the holder of the request's reply key can
// read. Each field is base64url without padding.
type SealedReply struct {
	// EphemeralKey is the server's one-time P-256 public key, 65 bytes.
	EphemeralKey string `json:"epk"`
	// Nonce is the 12-byte AES-256-GCM nonce.
	Nonce string `json:"nonce"`
	// Ciphertext is the sealed plaintext with the 16-byte tag appended.
	Ciphertext string `json:"ciphertext"`
}

// Seal seals plaintext to replyKey, salted with the request's raw challenge:
// a fresh P-256 key pair (e, E); Z, the X coordinate of ECDH(e, P); the
// key KDF(Z || E || P, challenge, ReplyLabel); AES-256-GCM with a random
// nonce and no additional data.
func Seal(replyKey PublicKey, challenge, plaintext []byte) (SealedReply, error) {
	return seal(rand.Reader, replyKey, challenge, plaintext)
}

func seal(random io.Reader, replyKey PublicKey, challenge, plaintext []byte) (SealedReply, error) {
	ephemeral, err := ecdh.P256().GenerateKey(random)
	if err != nil {
		return SealedReply{}, fmt.Errorf("appkeys: sealing reply: %w", err)
	}
	nonce := make([]byte, sealNonceSize)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return SealedReply{}, fmt.Errorf("appkeys: sealing reply: %w", err)
	}
	return sealWith(ephemeral, nonce, replyKey, challenge, plaintext)
}

// sealWith seals with a given ephemeral key and nonce, so a test can
// reproduce a known reply byte for byte.
func sealWith(ephemeral *ecdh.PrivateKey, nonce []byte, replyKey PublicKey, challenge, plaintext []byte) (SealedReply, error) {
	recipient, err := ecdh.P256().NewPublicKey(replyKey.raw[:])
	if err != nil {
		return SealedReply{}, fmt.Errorf("appkeys: sealing reply: %w", err)
	}
	aead, err := replyCipher(ephemeral, recipient, ephemeral.PublicKey().Bytes(), replyKey.raw[:], challenge)
	if err != nil {
		return SealedReply{}, err
	}
	return SealedReply{
		EphemeralKey: encoding.EncodeToString(ephemeral.PublicKey().Bytes()),
		Nonce:        encoding.EncodeToString(nonce),
		Ciphertext:   encoding.EncodeToString(aead.Seal(nil, nonce, plaintext, nil)),
	}, nil
}

// replyCipher derives the sealed reply's AES-256-GCM cipher from one side's
// private key and the other's public key. e and p are the 65-byte encodings
// of the ephemeral key and the reply key, in that order on both sides.
func replyCipher(priv *ecdh.PrivateKey, peer *ecdh.PublicKey, e, p, challenge []byte) (cipher.AEAD, error) {
	z, err := priv.ECDH(peer)
	if err != nil {
		return nil, fmt.Errorf("appkeys: reply key agreement: %w", err)
	}
	input := make([]byte, 0, len(z)+len(e)+len(p))
	input = append(append(append(input, z...), e...), p...)
	key, err := hkdf.Key(sha256.New, input, challenge, ReplyLabel, sealKeySize)
	if err != nil {
		return nil, fmt.Errorf("appkeys: deriving reply key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("appkeys: reply cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// Open opens a sealed reply with the reply key's 32-byte private scalar, as
// the device does. The server never opens one; tests and tools do.
func Open(replyPrivate []byte, challenge []byte, sealed SealedReply) ([]byte, error) {
	priv, err := ecdh.P256().NewPrivateKey(replyPrivate)
	if err != nil {
		return nil, fail(CodeOpenFailed)
	}
	epk, err := decodeB64(sealed.EphemeralKey)
	if err != nil {
		return nil, fail(CodeOpenFailed)
	}
	ephemeral, err := ecdh.P256().NewPublicKey(epk)
	if err != nil || len(epk) != PublicKeySize {
		return nil, fail(CodeOpenFailed)
	}
	nonce, err := decodeB64(sealed.Nonce)
	if err != nil || len(nonce) != sealNonceSize {
		return nil, fail(CodeOpenFailed)
	}
	ciphertext, err := decodeB64(sealed.Ciphertext)
	if err != nil {
		return nil, fail(CodeOpenFailed)
	}
	aead, err := replyCipher(priv, ephemeral, epk, priv.PublicKey().Bytes(), challenge)
	if err != nil {
		return nil, fail(CodeOpenFailed)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fail(CodeOpenFailed)
	}
	return plaintext, nil
}
