package nodepool

import (
	"encoding/base64"
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

func TestGeneratedNodeKeyParsesToItsNode(t *testing.T) {
	key, err := GenerateNodeKey(7)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "snk1_7_") {
		t.Fatalf("key %q lacks the snk1_<id>_ prefix", key)
	}
	random, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "snk1_7_"))
	if err != nil || len(random) != 32 {
		t.Fatalf("key material = %d bytes, %v; want 32 bytes of base64url", len(random), err)
	}
	if id, err := ParseNodeKey(key); err != nil || id != 7 {
		t.Fatalf("ParseNodeKey = %d, %v; want 7", id, err)
	}
	other, err := GenerateNodeKey(7)
	if err != nil || other == key {
		t.Fatalf("a second key for the same node repeated the first: %v", err)
	}
	if _, err := GenerateNodeKey(0); !errors.Is(err, ErrInvalidNodeKey) {
		t.Fatalf("GenerateNodeKey(0) = %v, want ErrInvalidNodeKey", err)
	}
}

func TestParseNodeKeyRejectsMalformedKeys(t *testing.T) {
	valid, err := GenerateNodeKey(12)
	if err != nil {
		t.Fatal(err)
	}
	material := strings.TrimPrefix(valid, "snk1_12_")
	short := base64.RawURLEncoding.EncodeToString(make([]byte, 31))
	for name, key := range map[string]string{
		"empty":             "",
		"wrong prefix":      "snk2_12_" + material,
		"no id":             "snk1__" + material,
		"zero id":           "snk1_0_" + material,
		"negative id":       "snk1_-12_" + material,
		"leading zero":      "snk1_012_" + material,
		"signed id":         "snk1_+12_" + material,
		"no material":       "snk1_12_",
		"no separator":      "snk1_12",
		"short material":    "snk1_12_" + short,
		"padded material":   valid + "=",
		"standard alphabet": "snk1_12_" + strings.NewReplacer("-", "+", "_", "/").Replace(material) + "+/",
		"embedded newline":  "snk1_12_" + material[:20] + "\n" + material[20:],
		"trailing space":    valid + " ",
		"leading space":     " " + valid,
	} {
		if _, err := ParseNodeKey(key); !errors.Is(err, ErrInvalidNodeKey) {
			t.Errorf("%s: ParseNodeKey(%q) = %v, want ErrInvalidNodeKey", name, key, err)
		}
	}
}

func TestNodeKeyEncryptionRoundTripsAndBindsTheRow(t *testing.T) {
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
	if got, err := DecryptNodeKey(cipher, 3, ciphertext); err != nil || got != key {
		t.Fatalf("DecryptNodeKey = %q, %v; want the issued key", got, err)
	}
	// Row A's ciphertext copied onto row B must not decrypt there.
	if _, err := DecryptNodeKey(cipher, 4, ciphertext); err == nil {
		t.Fatal("node 3's ciphertext decrypted as node 4's key")
	}
	if _, err := EncryptNodeKey(cipher, 4, key); !errors.Is(err, ErrInvalidNodeKey) {
		t.Fatalf("encrypting node 3's key for node 4 = %v, want ErrInvalidNodeKey", err)
	}
	// A plaintext key in the column is not an encrypted one.
	if _, err := DecryptNodeKey(cipher, 3, key); err == nil {
		t.Fatal("a plaintext key in the column was accepted")
	}
	// A correctly bound ciphertext must still hold a key naming its row.
	forged, err := cipher.Encrypt("snk1_9_"+strings.TrimPrefix(key, "snk1_3_"), nodeKeyAAD(3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptNodeKey(cipher, 3, forged); !errors.Is(err, ErrInvalidNodeKey) {
		t.Fatalf("a key naming node 9 stored on node 3 = %v, want ErrInvalidNodeKey", err)
	}
	other, err := secret.New([]byte("another-master-key-for-node-key-tests-0123"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptNodeKey(other, 3, ciphertext); err == nil {
		t.Fatal("a ciphertext decrypted under another SECRET_KEY")
	}
}
