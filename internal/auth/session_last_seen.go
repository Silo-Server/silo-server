package auth

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// SessionLastSeenUpdater persists authenticated login-session activity.
type SessionLastSeenUpdater interface {
	UpdateLastSeen(context.Context, string, time.Time) error
}

// SessionLastSeenTracker follows APIKeyLastUsedTracker's bounded, asynchronous
// activity accounting. Authentication still checks the database on every
// request; this tracker caches only when to record activity, never validity.
type SessionLastSeenTracker struct {
	updater   SessionLastSeenUpdater
	now       func() time.Time
	mu        sync.Mutex
	seen      map[string]time.Time
	nextPrune time.Time
}

func NewSessionLastSeenTracker(updater SessionLastSeenUpdater, now func() time.Time) *SessionLastSeenTracker {
	if now == nil {
		now = time.Now
	}
	return &SessionLastSeenTracker{updater: updater, now: now, seen: make(map[string]time.Time)}
}

// Touch captures the time of a successfully authenticated request, rather
// than the eventual write time. At most one write per minute per session is
// scheduled on this node; the store applies the same bound across nodes.
func (t *SessionLastSeenTracker) Touch(id string) {
	if t == nil || t.updater == nil || id == "" {
		return
	}
	now := t.now()
	t.mu.Lock()
	if !now.Before(t.nextPrune) {
		for key, at := range t.seen {
			if !at.After(now.Add(-time.Minute)) {
				delete(t.seen, key)
			}
		}
		t.nextPrune = now.Add(time.Minute)
	}
	if at, ok := t.seen[id]; ok && now.Sub(at) < time.Minute {
		t.mu.Unlock()
		return
	}
	t.seen[id] = now
	t.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := t.updater.UpdateLastSeen(ctx, id, now); err != nil {
			slog.DebugContext(ctx, "login session activity update failed", "component", "auth", "error", err)
		}
	}()
}
