package jellycompat

import (
	"context"
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
	// afterGet and afterUpdate, when set, run after GetByToken has read a row
	// or UpdateByToken has written one, as a revocation committing then.
	afterGet    func()
	afterUpdate func()
}

func (r *rowSessionRepo) Upsert(_ context.Context, session Session) error {
	r.rows[session.Token] = session
	return nil
}

func (r *rowSessionRepo) UpdateByToken(_ context.Context, session Session) error {
	if _, ok := r.rows[session.Token]; !ok {
		return ErrSessionNotFound
	}
	r.rows[session.Token] = session
	if r.afterUpdate != nil {
		r.afterUpdate()
	}
	return nil
}

func (r *rowSessionRepo) GetByToken(_ context.Context, token string, _ time.Time) (*Session, error) {
	session, ok := r.rows[token]
	if !ok {
		return nil, ErrSessionNotFound
	}
	if r.afterGet != nil {
		r.afterGet()
	}
	return &session, nil
}

func (r *rowSessionRepo) DeleteByToken(_ context.Context, token string) error {
	r.deletes++
	delete(r.rows, token)
	return nil
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
