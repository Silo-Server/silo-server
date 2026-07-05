package noderecipe

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A nil-backed store (single integrated box, no Redis) must be safe: writes
// no-op and reads miss, so callers need no nil-guarding.
func TestNilStore_PutNoopGetMiss(t *testing.T) {
	var s *Store // nil receiver
	if err := s.Put(context.Background(), "sid", playback.RecipeCard{}); err != nil {
		t.Fatalf("nil store Put returned error: %v", err)
	}
	if card, ok := s.Get(context.Background(), "sid"); ok || card != nil {
		t.Fatalf("nil store Get = (%v, %v), want (nil, false)", card, ok)
	}

	disabled := NewStore(nil, 0)
	if err := disabled.Put(context.Background(), "sid", playback.RecipeCard{}); err != nil {
		t.Fatalf("disabled store Put returned error: %v", err)
	}
	if _, ok := disabled.Get(context.Background(), "sid"); ok {
		t.Fatalf("disabled store Get returned a hit, want miss")
	}
}

// Delete on a nil/disabled store must be a safe no-op (callers in teardown
// paths need no nil-guarding), and on either store a subsequent Get still
// misses, matching the delete-then-get-not-found and delete-missing-is-no-op
// contract.
func TestNilStore_DeleteNoop(t *testing.T) {
	var s *Store // nil receiver
	if err := s.Delete(context.Background(), "sid"); err != nil {
		t.Fatalf("nil store Delete returned error: %v", err)
	}
	if card, ok := s.Get(context.Background(), "sid"); ok || card != nil {
		t.Fatalf("nil store Get after Delete = (%v, %v), want (nil, false)", card, ok)
	}

	disabled := NewStore(nil, 0)
	// Delete of a missing key is a no-op success.
	if err := disabled.Delete(context.Background(), "sid"); err != nil {
		t.Fatalf("disabled store Delete returned error: %v", err)
	}
	if _, ok := disabled.Get(context.Background(), "sid"); ok {
		t.Fatalf("disabled store Get after Delete returned a hit, want miss")
	}
}

func TestKeyNamespacing(t *testing.T) {
	if got := key("abc"); got != "silo:noderecipe:abc" {
		t.Fatalf("key(abc) = %q, want silo:noderecipe:abc", got)
	}
}

func TestDefaultTTLMatchesTokenLifetime(t *testing.T) {
	if DefaultTTL != playback.MaxTokenTTL {
		t.Fatalf("DefaultTTL = %v, want playback.MaxTokenTTL %v", DefaultTTL, playback.MaxTokenTTL)
	}
	if NewStore(nil, 0).ttl != DefaultTTL {
		t.Fatal("NewStore with ttl<=0 did not default to DefaultTTL")
	}
}
