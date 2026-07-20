package auth

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	apiKeyLastUsedInterval = time.Minute
	apiKeyLastUsedTimeout  = 5 * time.Second
)

// APIKeyLastUsedUpdater persists an API key's last-used timestamp.
type APIKeyLastUsedUpdater interface {
	UpdateLastUsed(ctx context.Context, id int64) error
}

// APIKeyLastUsedTracker coalesces last-used writes per key without blocking
// authentication requests. Stale entries are pruned inline once per interval,
// avoiding both unbounded historical-key retention and another goroutine.
type APIKeyLastUsedTracker struct {
	updater APIKeyLastUsedUpdater
	now     func() time.Time

	mu         sync.Mutex
	lastUsedAt map[int64]time.Time
	nextPrune  time.Time
}

// NewAPIKeyLastUsedTracker creates a tracker. A nil clock uses time.Now.
func NewAPIKeyLastUsedTracker(updater APIKeyLastUsedUpdater, now func() time.Time) *APIKeyLastUsedTracker {
	if now == nil {
		now = time.Now
	}
	return &APIKeyLastUsedTracker{
		updater:    updater,
		now:        now,
		lastUsedAt: make(map[int64]time.Time),
	}
}

// Touch records key usage asynchronously, at most once per interval per key.
func (t *APIKeyLastUsedTracker) Touch(id int64) {
	if t == nil || t.updater == nil || !t.shouldUpdate(id) {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), apiKeyLastUsedTimeout)
		defer cancel()
		if err := t.updater.UpdateLastUsed(ctx, id); err != nil {
			slog.DebugContext(ctx, "api key last-used update failed", "component", "auth", "id", id, "error", err)
		}
	}()
}

func (t *APIKeyLastUsedTracker) shouldUpdate(id int64) bool {
	now := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.nextPrune.IsZero() || !now.Before(t.nextPrune) {
		cutoff := now.Add(-apiKeyLastUsedInterval)
		for keyID, lastUsed := range t.lastUsedAt {
			if !lastUsed.After(cutoff) {
				delete(t.lastUsedAt, keyID)
			}
		}
		t.nextPrune = now.Add(apiKeyLastUsedInterval)
	}

	if lastUsed, ok := t.lastUsedAt[id]; ok && now.Sub(lastUsed) < apiKeyLastUsedInterval {
		return false
	}
	t.lastUsedAt[id] = now
	return true
}
