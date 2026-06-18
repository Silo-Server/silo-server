package jellycompat

import (
	"context"
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
