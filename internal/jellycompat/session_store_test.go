package jellycompat

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// rowSessionRepo keeps stored sessions in a map, standing in for the
// jellycompat_sessions table.
type rowSessionRepo struct {
	rows    map[string]Session
	deletes int
}

func (r *rowSessionRepo) Upsert(_ context.Context, session Session) error {
	r.rows[session.Token] = session
	return nil
}

func (r *rowSessionRepo) GetByToken(_ context.Context, token string, _ time.Time) (*Session, error) {
	session, ok := r.rows[token]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return &session, nil
}

func (r *rowSessionRepo) DeleteByToken(_ context.Context, token string) error {
	r.deletes++
	delete(r.rows, token)
	return nil
}

func TestEvictUser(t *testing.T) {
	store := NewSessionStore(24*time.Hour, fixedNow)

	// Insert sessions for two different users.
	_ = store.Put(Session{Token: "aaa", StreamAppUserID: 1, Username: "alice"})
	_ = store.Put(Session{Token: "bbb", StreamAppUserID: 1, Username: "alice"})
	_ = store.Put(Session{Token: "ccc", StreamAppUserID: 2, Username: "bob"})

	store.EvictUser(1)

	if _, ok := store.Get("aaa"); ok {
		t.Error("expected session aaa to be deleted")
	}
	if _, ok := store.Get("bbb"); ok {
		t.Error("expected session bbb to be deleted")
	}
	if _, ok := store.Get("ccc"); !ok {
		t.Error("expected session ccc to still exist")
	}
}

// Revocation deletes the stored rows in its own transaction; EvictUser must
// then drop the copies this replica cached, or a cached session keeps serving
// after its row is gone.
func TestEvictUserAfterRevokedRowsEndsCachedSessions(t *testing.T) {
	repo := &rowSessionRepo{rows: map[string]Session{}}
	store := NewPersistentSessionStore(24*time.Hour, fixedNow, repo)
	for _, s := range []Session{{Token: "aaa", StreamAppUserID: 1}, {Token: "bbb", StreamAppUserID: 2}} {
		if err := store.Put(s); err != nil {
			t.Fatal(err)
		}
	}

	delete(repo.rows, "aaa") // the revoking transaction
	if _, err := store.Lookup(t.Context(), "aaa"); err != nil {
		t.Fatalf("before eviction the cached copy still serves: %v", err)
	}
	store.EvictUser(1)

	if _, err := store.Lookup(t.Context(), "aaa"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("revoked session after eviction = %v, want ErrSessionNotFound", err)
	}
	if _, err := store.Lookup(t.Context(), "bbb"); err != nil {
		t.Fatalf("another account's session = %v", err)
	}
	if repo.deletes != 0 {
		t.Fatalf("EvictUser wrote to the store %d times; the revoking transaction owns the rows", repo.deletes)
	}
}

func TestGetSlidingWindow_ExtendsWhenBelowHalfTTL(t *testing.T) {
	ttl := 30 * 24 * time.Hour // 30 days
	now := fixedNow()
	clock := func() time.Time { return now }
	store := NewSessionStore(ttl, clock)

	_ = store.Put(Session{Token: "tok1", StreamAppUserID: 1})

	// Advance time to 20 days (past the halfway point of 15 days).
	now = now.Add(20 * 24 * time.Hour)

	session, ok := store.Get("tok1")
	if !ok {
		t.Fatal("expected session to exist")
	}

	// ExpiresAt should be extended to now + ttl.
	expected := now.Add(ttl)
	if !session.ExpiresAt.Equal(expected) {
		t.Errorf("expected ExpiresAt = %v, got %v", expected, session.ExpiresAt)
	}
}

func TestGetSlidingWindow_NoExtensionAboveHalfTTL(t *testing.T) {
	ttl := 30 * 24 * time.Hour
	now := fixedNow()
	clock := func() time.Time { return now }
	store := NewSessionStore(ttl, clock)

	_ = store.Put(Session{Token: "tok2", StreamAppUserID: 1})
	originalExpiry := now.Add(ttl)

	// Advance time to 10 days (before the halfway point of 15 days).
	now = now.Add(10 * 24 * time.Hour)

	session, ok := store.Get("tok2")
	if !ok {
		t.Fatal("expected session to exist")
	}

	// ExpiresAt should NOT have changed.
	if !session.ExpiresAt.Equal(originalExpiry) {
		t.Errorf("expected ExpiresAt = %v, got %v", originalExpiry, session.ExpiresAt)
	}
}

func TestGet_ExpiredSession_ReturnsNotFound(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	store := NewSessionStore(1*time.Hour, clock)

	_ = store.Put(Session{Token: "short-lived", StreamAppUserID: 1})

	// Advance past TTL.
	now = now.Add(2 * time.Hour)

	if _, ok := store.Get("short-lived"); ok {
		t.Error("expected expired session to not be returned")
	}
}
