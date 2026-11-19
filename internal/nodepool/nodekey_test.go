package nodepool

import (
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func testDataCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	cipher, err := secret.New([]byte("node-key-test-master-key-0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

// A stored key opens only on its own row, under the project's row AAD, and
// only when it names that row.
func TestNodeKeyCiphertextIsBoundToItsRow(t *testing.T) {
	cipher := testDataCipher(t)
	key, err := GenerateNodeKey(3)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := EncryptNodeKey(cipher, 3, key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, key) || !secret.IsEncrypted(ciphertext) {
		t.Fatalf("ciphertext %q is not an envelope hiding the key", ciphertext)
	}
	// Stored rows depend on this exact AAD; changing it strands every key.
	if got, err := cipher.Decrypt(ciphertext, "stream_nodes:node_key_ciphertext:3"); err != nil || got != key {
		t.Fatalf("ciphertext does not open under the row AAD: %v", err)
	}
	if got, err := DecryptNodeKey(cipher, 3, ciphertext); err != nil || got != key {
		t.Fatalf("DecryptNodeKey = %q, %v; want the issued key", got, err)
	}
	if _, err := DecryptNodeKey(cipher, 4, ciphertext); err == nil {
		t.Fatal("node 3's ciphertext decrypted as node 4's key")
	}
	if _, err := EncryptNodeKey(cipher, 4, key); !errors.Is(err, ErrInvalidNodeKey) {
		t.Fatalf("encrypting node 3's key for node 4 = %v, want ErrInvalidNodeKey", err)
	}
	if _, err := DecryptNodeKey(cipher, 3, key); err == nil {
		t.Fatal("a plaintext key in the column was accepted")
	}
	forged, err := cipher.Encrypt("snk1_9_"+strings.TrimPrefix(key, "snk1_3_"), nodeKeyAAD(3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptNodeKey(cipher, 3, forged); !errors.Is(err, ErrInvalidNodeKey) {
		t.Fatalf("a key naming node 9 stored on node 3 = %v, want ErrInvalidNodeKey", err)
	}
}
