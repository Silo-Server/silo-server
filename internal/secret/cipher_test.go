package secret

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// testKey returns a deterministic-length master key suitable for New.
func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 48)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("read key: %v", err)
	}
	return key
}

func newTestCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := New(testKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := newTestCipher(t)
	const (
		plaintext = "sk-radarr-0123456789abcdef"
		aad       = "request_integrations:api_key_ref:abc123"
	)
	ct, err := c.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(ct, "enc:v1:") {
		t.Fatalf("ciphertext missing enc:v1: prefix: %q", ct)
	}
	if !IsEncrypted(ct) {
		t.Fatalf("IsEncrypted(%q) = false, want true", ct)
	}
	got, err := c.Decrypt(ct, aad)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plaintext {
		t.Fatalf("round-trip = %q, want %q", got, plaintext)
	}
}

func TestEncryptEmptyReturnsEmpty(t *testing.T) {
	c := newTestCipher(t)
	ct, err := c.Encrypt("", "any:aad")
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	if ct != "" {
		t.Fatalf("Encrypt(\"\") = %q, want empty (no envelope for an empty secret)", ct)
	}
}

func TestEncryptNonDeterministic(t *testing.T) {
	c := newTestCipher(t)
	const plaintext, aad = "same-secret", "t:c:1"
	a, err := c.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt a: %v", err)
	}
	b, err := c.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt b: %v", err)
	}
	if a == b {
		t.Fatalf("two encryptions of the same plaintext+aad were identical; random nonce not applied")
	}
	// Both must still decrypt back to the same plaintext.
	for _, ct := range []string{a, b} {
		got, err := c.Decrypt(ct, aad)
		if err != nil || got != plaintext {
			t.Fatalf("Decrypt(%q) = (%q, %v), want (%q, nil)", ct, got, err, plaintext)
		}
	}
}

// Decrypt refuses every value it cannot authenticate. A non-prefixed legacy
// value is the caller's pass-through responsibility, but Decrypt itself must
// reject it rather than echo it back.
func TestDecryptRejects(t *testing.T) {
	c := newTestCipher(t)
	const aad = "t:c:1"
	ct, err := c.Encrypt("secret", aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Decode the body, flip a byte, re-encode, reattach the envelope.
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ct, "enc:v1:"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[len(raw)-1] ^= 0xFF
	for _, tc := range []struct {
		name    string
		cipher  *Cipher
		ct, aad string
		want    error
	}{
		{"tampered ciphertext", c, "enc:v1:" + base64.RawURLEncoding.EncodeToString(raw), aad, nil},
		{"mismatched AAD (row binding broken)", c, ct, "t:c:2", nil},
		{"the wrong key", newTestCipher(t), ct, aad, nil},
		{"enc:v99:", c, "enc:v99:" + base64.RawURLEncoding.EncodeToString([]byte("whatever")), aad, ErrUnknownVersion},
		{"a non-envelope value", c, "plaintext-not-an-envelope", aad, nil},
		// Valid base64url but fewer bytes than the GCM nonce.
		{"a too-short ciphertext", c, "enc:v1:" + base64.RawURLEncoding.EncodeToString([]byte("ab")), aad, nil},
	} {
		_, err := tc.cipher.Decrypt(tc.ct, tc.aad)
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("Decrypt with %s returned %v, want error %v", tc.name, err, tc.want)
		}
	}
}

func TestNewRejectsShortKey(t *testing.T) {
	if _, err := New(make([]byte, MinMasterKeyLen-1)); err == nil {
		t.Fatalf("New with a %d-byte key succeeded, want error", MinMasterKeyLen-1)
	}
	if _, err := New(nil); err == nil {
		t.Fatalf("New(nil) succeeded, want error")
	}
	if _, err := New(make([]byte, MinMasterKeyLen)); err != nil {
		t.Fatalf("New with a %d-byte key failed: %v", MinMasterKeyLen, err)
	}
}

func TestIsEncrypted(t *testing.T) {
	cases := map[string]bool{
		"enc:v1:x":   true,
		"sa_abc":     false,
		"":           false,
		"ENC:V1:x":   false,
		"enc:v1":     false,
		"plain text": false,
	}
	for in, want := range cases {
		if got := IsEncrypted(in); got != want {
			t.Errorf("IsEncrypted(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDecryptIfEncrypted(t *testing.T) {
	c := newTestCipher(t)
	const aad = "server_settings:tmdb.api_key"

	// Empty passes through.
	if got, err := c.DecryptIfEncrypted("", aad); err != nil || got != "" {
		t.Fatalf("DecryptIfEncrypted(\"\") = (%q, %v), want (\"\", nil)", got, err)
	}
	// Legacy plaintext passes through unchanged.
	if got, err := c.DecryptIfEncrypted("legacy-plaintext-key", aad); err != nil || got != "legacy-plaintext-key" {
		t.Fatalf("DecryptIfEncrypted(plaintext) = (%q, %v), want pass-through", got, err)
	}
	// A real enc:v1: value is decrypted.
	ct, err := c.Encrypt("real-secret", aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if got, err := c.DecryptIfEncrypted(ct, aad); err != nil || got != "real-secret" {
		t.Fatalf("DecryptIfEncrypted(ct) = (%q, %v), want (\"real-secret\", nil)", got, err)
	}
	// A corrupt enc:v1: value errors — never falls back to the ciphertext.
	if _, err := c.DecryptIfEncrypted(ct, "server_settings:wrong.key"); err == nil {
		t.Fatalf("DecryptIfEncrypted with wrong AAD succeeded, want error")
	}
}

func TestAADHelpers(t *testing.T) {
	if got := SettingsAAD("auth.jwt_secret"); got != "server_settings:auth.jwt_secret" {
		t.Errorf("SettingsAAD = %q", got)
	}
	if got := RowAAD("request_integrations", "api_key_ref", "abc"); got != "request_integrations:api_key_ref:abc" {
		t.Errorf("RowAAD = %q", got)
	}
}

func TestEncryptIfPlaintextIdempotent(t *testing.T) {
	c := newTestCipher(t)
	const aad = "t:c:1"

	// First call encrypts.
	ct, changed, err := c.EncryptIfPlaintext("secret", aad)
	if err != nil {
		t.Fatalf("EncryptIfPlaintext: %v", err)
	}
	if !changed {
		t.Fatalf("first EncryptIfPlaintext reported changed=false, want true")
	}
	if !IsEncrypted(ct) {
		t.Fatalf("EncryptIfPlaintext did not produce an envelope: %q", ct)
	}

	// Second call is a no-op on the already-encrypted value.
	ct2, changed2, err := c.EncryptIfPlaintext(ct, aad)
	if err != nil {
		t.Fatalf("EncryptIfPlaintext (2nd): %v", err)
	}
	if changed2 {
		t.Fatalf("second EncryptIfPlaintext reported changed=true, want false (double-encrypt)")
	}
	if ct2 != ct {
		t.Fatalf("second EncryptIfPlaintext mutated the value: %q -> %q", ct, ct2)
	}

	// Empty is a no-op too.
	got, changed3, err := c.EncryptIfPlaintext("", aad)
	if err != nil || changed3 || got != "" {
		t.Fatalf("EncryptIfPlaintext(\"\") = (%q, %v, %v), want (\"\", false, nil)", got, changed3, err)
	}
}
