package requests

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// TestEncryptAPIKeyRoundTripAndAAD guards the security-critical invariants of the
// at-rest credential cipher adopted from #95: encryptAPIKey must produce
// ciphertext that DecryptIfEncrypted inverts under the SAME id-bound AAD that
// secret's backfill writes, a blank key must encrypt to "" (the keep-existing
// update sentinel), and the AAD must be row-bound. A drift in apiKeyAAD or the
// envelope would silently corrupt or leak every stored connection api key with no
// other test catching it (there is no DB harness for scanIntegration).
func TestEncryptAPIKeyRoundTripAndAAD(t *testing.T) {
	cipher, err := secret.New([]byte("unit-test-master-key-with-at-least-32-bytes"))
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	r := &Repository{cipher: cipher}

	const id = "integ-1"
	const key = "00178d9a3893480cbf122d29f7aca1a0"

	enc, err := r.encryptAPIKey(id, key)
	if err != nil {
		t.Fatalf("encryptAPIKey: %v", err)
	}
	if enc == "" || enc == key {
		t.Fatalf("encryptAPIKey did not encrypt: %q", enc)
	}

	// The AAD must equal what secret.BackfillReferencedColumns wrote, or rows
	// encrypted by the startup backfill fail to decrypt on read.
	if got, want := apiKeyAAD(id), secret.RowAAD("request_integrations", "api_key_ref", id); got != want {
		t.Fatalf("apiKeyAAD = %q, want %q (must match #95 backfill)", got, want)
	}

	dec, err := cipher.DecryptIfEncrypted(enc, apiKeyAAD(id))
	if err != nil {
		t.Fatalf("DecryptIfEncrypted: %v", err)
	}
	if dec != key {
		t.Fatalf("round-trip = %q, want %q", dec, key)
	}

	// A blank/whitespace key encrypts to "" so updateIntegration's
	// CASE WHEN $5 = '' sentinel preserves the stored ciphertext.
	if blank, err := r.encryptAPIKey(id, "   "); err != nil || blank != "" {
		t.Fatalf("encryptAPIKey(blank) = %q, %v; want \"\", nil (keep-existing sentinel)", blank, err)
	}

	// AAD is row-bound: ciphertext from one row must not authenticate under
	// another row's id.
	if _, err := cipher.DecryptIfEncrypted(enc, apiKeyAAD("integ-2")); err == nil {
		t.Fatal("ciphertext decrypted under a different id's AAD — AAD is not row-bound")
	}
}
