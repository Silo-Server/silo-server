package plugins

import (
	"bytes"
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func instanceStateTestCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	cipher, err := secret.New(bytes.Repeat([]byte("k"), secret.MinMasterKeyLen))
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	return cipher
}

func TestInstanceStateEmptyValueAuthenticatesRow(t *testing.T) {
	store := NewInstanceStateStore(nil, instanceStateTestCipher(t))
	stored, err := store.seal(5, "api", "key", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := store.open(5, "api", "key", stored); err != nil || len(value) != 0 {
		t.Fatalf("roundtrip=%q, %v", value, err)
	}
	for _, row := range []struct {
		id         int
		scope, key string
	}{{6, "api", "key"}, {5, "node:1", "key"}, {5, "api", "other"}} {
		if _, err := store.open(row.id, row.scope, row.key, stored); err == nil {
			t.Errorf("empty value accepted under another row: %+v", row)
		}
	}
	if _, err := store.open(5, "api", "key", []byte("empty:v1")); err == nil {
		t.Error("plaintext empty marker accepted")
	}
	if _, err := store.open(5, "api", "key", bytes.TrimPrefix(stored, []byte(instanceStateEmptyMarker))); err == nil {
		t.Error("removing the empty marker changed an authenticated empty value into ordinary data")
	}
	ordinary, err := store.seal(5, "api", "key:empty", []byte("empty"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.open(5, "api", "key", append([]byte(instanceStateEmptyMarker), ordinary...)); err == nil {
		t.Error("ordinary ciphertext became an authenticated empty value")
	}
	value := []byte("empty:v1")
	stored, err = store.seal(5, "api", "key", value)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.open(5, "api", "key", stored); err != nil || !bytes.Equal(got, value) {
		t.Fatalf("marker literal=%q, %v", got, err)
	}
}
