package auth

import (
	"errors"
	"testing"
)

// A key Postgres cannot take as text is answered as not found before any
// query: the query would fail with an encoding error that callers read as a
// store outage (a retryable 503) for a key that can never be valid.
func TestGetByKeyRefusesKeysPostgresCannotTake(t *testing.T) {
	repo := &APIKeyRepository{} // no pool: a query would panic
	for _, key := range []string{"sa_\xff", "sa_\x00", "sa_ab\xc3"} {
		if _, err := repo.GetByKey(t.Context(), key); !errors.Is(err, ErrAPIKeyNotFound) {
			t.Fatalf("GetByKey(%q) = %v, want ErrAPIKeyNotFound", key, err)
		}
	}
	if !ValidTextKey("sa_0123abcd") {
		t.Fatal("ValidTextKey refused a well-formed key")
	}
}
