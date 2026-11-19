package jellycompat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type unavailableSessionContent struct{ ContentService }

func (unavailableSessionContent) GetItemDetail(context.Context, *Session, string, *int) (*upstreamItemDetail, error) {
	return nil, errors.New("catalog unavailable")
}

type pingTestStore interface {
	CompatPlaybackStore
	TouchActiveForToken(context.Context, string, string) error
}

func assertPingPreservesAbsoluteLifetime(t *testing.T, store pingTestStore, now *time.Time) {
	t.Helper()
	id := uuid.NewString()
	store.Put(PlaybackSession{ID: id, CompatToken: id, UpstreamSessionID: "native"})
	t.Cleanup(func() { store.Delete(id) })
	before, ok := store.Get(id)
	if !ok {
		t.Fatal("session not stored")
	}
	*now = now.Add(55 * time.Minute)
	if err := store.TouchActiveForToken(t.Context(), id, id); err != nil {
		t.Fatal(err)
	}
	after, ok := store.Get(id)
	if !ok || !after.UpdatedAt.Equal(*now) || !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("ping must refresh activity without extending grant: %+v", after)
	}
	*now = before.ExpiresAt
	if err := store.TouchActiveForToken(t.Context(), id, id); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("ping revived expired grant: %v", err)
	}
	if _, ok := store.Get(id); ok {
		t.Fatal("expired grant remains usable")
	}
}

func TestSessionPingPreservesAbsoluteLifetime(t *testing.T) {
	now := time.Now().UTC()
	assertPingPreservesAbsoluteLifetime(t, NewPlaybackSessionStore(time.Hour, func() time.Time { return now }), &now)
}
