package recommendations

import (
	"context"
	"testing"
	"time"
)

func newDebounceTestWorker() *Worker {
	return &Worker{
		engine:                &Engine{},
		profileRefreshCh:      make(chan profileRefreshRequest, 4),
		profileRefreshPending: make(map[string]struct{}),
		profileRefreshDone:    make(map[string]time.Time),
	}
}

func TestRequestProfileRefreshDebouncesAfterSuccessfulRefresh(t *testing.T) {
	ctx := context.Background()
	w := newDebounceTestWorker()
	key := profileRefreshKey(1, "profile-a")

	w.RequestProfileRefresh(ctx, 1, "profile-a")
	if got := len(w.profileRefreshCh); got != 1 {
		t.Fatalf("queued requests = %d, want 1", got)
	}

	// While the request is pending, duplicates are dropped.
	w.RequestProfileRefresh(ctx, 1, "profile-a")
	if got := len(w.profileRefreshCh); got != 1 {
		t.Fatalf("queued requests with pending entry = %d, want 1", got)
	}

	// Simulate the refresh loop completing the request successfully.
	<-w.profileRefreshCh
	w.mu.Lock()
	w.profileRefreshDone[key] = time.Now()
	delete(w.profileRefreshPending, key)
	w.mu.Unlock()

	// Within the cooldown the request is debounced.
	w.RequestProfileRefresh(ctx, 1, "profile-a")
	if got := len(w.profileRefreshCh); got != 0 {
		t.Fatalf("queued requests within cooldown = %d, want 0", got)
	}

	// A different profile is not affected by the cooldown.
	w.RequestProfileRefresh(ctx, 1, "profile-b")
	if got := len(w.profileRefreshCh); got != 1 {
		t.Fatalf("queued requests for other profile = %d, want 1", got)
	}

	// Once the cooldown has passed the profile can refresh again.
	w.mu.Lock()
	w.profileRefreshDone[key] = time.Now().Add(-profileRefreshMinInterval)
	w.mu.Unlock()
	w.RequestProfileRefresh(ctx, 1, "profile-a")
	if got := len(w.profileRefreshCh); got != 2 {
		t.Fatalf("queued requests after cooldown = %d, want 2", got)
	}
}

func TestRequestProfileRefreshAllowsRetryAfterFailedRefresh(t *testing.T) {
	ctx := context.Background()
	w := newDebounceTestWorker()
	key := profileRefreshKey(1, "profile-a")

	w.RequestProfileRefresh(ctx, 1, "profile-a")
	<-w.profileRefreshCh

	// Simulate the refresh loop finishing with an error: pending is cleared but
	// no completion time is recorded, so the next request is not debounced.
	w.mu.Lock()
	delete(w.profileRefreshPending, key)
	w.mu.Unlock()

	w.RequestProfileRefresh(ctx, 1, "profile-a")
	if got := len(w.profileRefreshCh); got != 1 {
		t.Fatalf("queued requests after failed refresh = %d, want 1", got)
	}
}
