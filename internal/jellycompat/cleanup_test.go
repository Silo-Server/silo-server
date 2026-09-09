package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type fakePlaybackSessionExpirer struct {
	called bool
	count  int64
	err    error
}

func (f *fakePlaybackSessionExpirer) DeleteExpired(context.Context) (int64, error) {
	f.called = true
	return f.count, f.err
}

func TestCleanupExpiredCompatStateIncludesPlaybackSessions(t *testing.T) {
	expirer := &fakePlaybackSessionExpirer{count: 3}

	authDeleted, playbackDeleted, err := cleanupExpiredCompatState(
		context.Background(),
		nil,
		expirer,
		time.Unix(100, 0),
	)
	if err != nil {
		t.Fatalf("cleanupExpiredCompatState returned error: %v", err)
	}
	if authDeleted != 0 {
		t.Fatalf("authDeleted = %d, want 0 with nil repo", authDeleted)
	}
	if playbackDeleted != 3 {
		t.Fatalf("playbackDeleted = %d, want 3", playbackDeleted)
	}
	if !expirer.called {
		t.Fatal("playback session expirer was not called")
	}
}

func TestCleanupExpiredDeviceProfilesDrainsBacklog(t *testing.T) {
	now := time.Now()
	store := NewDeviceProfileStore(time.Hour, func() time.Time { return now })
	for i := range 2501 {
		store.Put(fmt.Sprintf("expired-%d", i), DeviceProfile{})
	}
	now = now.Add(2 * time.Hour)
	store.Put("active", DeviceProfile{Name: "active"})
	deleted, err := cleanupExpiredDeviceProfiles(t.Context(), store)
	if err != nil || deleted != 2501 || len(store.profiles) != 1 {
		t.Fatalf("deleted %d remaining %d err %v", deleted, len(store.profiles), err)
	}
	if _, ok := store.Get("active"); !ok {
		t.Fatal("removed active registration")
	}
}

func TestCleanupExpiredDeviceProfilesStopsOnCancellationAndError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store := &fakePlaybackSessionExpirer{count: 1000}
	if _, err := cleanupExpiredDeviceProfiles(ctx, store); !errors.Is(err, context.Canceled) || store.called {
		t.Fatalf("err %v called %v", err, store.called)
	}
	want := errors.New("database unavailable")
	store.err = want
	deleted, err := cleanupExpiredDeviceProfiles(t.Context(), store)
	if deleted != 1000 || !errors.Is(err, want) {
		t.Fatalf("deleted %d err %v", deleted, err)
	}
}
