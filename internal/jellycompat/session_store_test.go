package jellycompat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type sharedSessionPersistence struct {
	mu           sync.Mutex
	sessions     map[string]Session
	nativeActive bool
}

func newSharedSessionPersistence() *sharedSessionPersistence {
	return &sharedSessionPersistence{sessions: make(map[string]Session), nativeActive: true}
}

func (p *sharedSessionPersistence) Upsert(_ context.Context, session Session) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.nativeActive {
		return ErrSessionNotFound
	}
	p.sessions[session.Token] = session
	return nil
}

func (p *sharedSessionPersistence) GetByToken(_ context.Context, token string, now time.Time) (*Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session, ok := p.sessions[token]
	if !ok || !p.nativeActive || !session.ExpiresAt.After(now) {
		return nil, ErrSessionNotFound
	}
	return new(session), nil
}

func (p *sharedSessionPersistence) IsActive(_ context.Context, token string, now time.Time) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session, ok := p.sessions[token]
	return ok && p.nativeActive && session.ExpiresAt.After(now), nil
}

func (p *sharedSessionPersistence) DeleteByToken(_ context.Context, token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sessions[token]; !ok {
		return ErrSessionNotFound
	}
	delete(p.sessions, token)
	return nil
}

func (p *sharedSessionPersistence) DeleteByUserID(_ context.Context, userID int) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	deleted := 0
	for token, session := range p.sessions {
		if session.StreamAppUserID == userID {
			delete(p.sessions, token)
			deleted++
		}
	}
	p.nativeActive = false
	return deleted, nil
}

func fixedNow() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

func TestDeleteByUserID(t *testing.T) {
	store := NewSessionStore(24*time.Hour, fixedNow)

	// Insert sessions for two different users.
	_ = store.Put(Session{Token: "aaa", StreamAppUserID: 1, Username: "alice"})
	_ = store.Put(Session{Token: "bbb", StreamAppUserID: 1, Username: "alice"})
	_ = store.Put(Session{Token: "ccc", StreamAppUserID: 2, Username: "bob"})

	store.DeleteByUserID(1)

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

func TestPersistentSessionStoreRejectsRevocationFromAnotherReplica(t *testing.T) {
	repo := newSharedSessionPersistence()
	replicaA := NewPersistentSessionStore(time.Hour, fixedNow, repo)
	replicaB := NewPersistentSessionStore(time.Hour, fixedNow, repo)
	session := Session{
		Token:              "shared-token",
		StreamAppUserID:    7,
		StreamAppSessionID: "native-session",
		AuthRevision:       3,
	}
	if err := replicaA.Put(session); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, ok := replicaB.Get(session.Token); !ok {
		t.Fatal("second replica did not load the shared session")
	}

	replicaA.DeleteByUserID(session.StreamAppUserID)

	if err := replicaB.Update(session.Token, func(session *Session) error {
		session.ProfileName = "stale update"
		return nil
	}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("stale replica update error = %v, want ErrSessionNotFound", err)
	}
	if _, ok := replicaB.Get(session.Token); ok {
		t.Fatal("second replica accepted a cached session after shared revocation")
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
