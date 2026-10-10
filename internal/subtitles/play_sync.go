package subtitles

import (
	"context"
	"sync"
)

// PlaySyncer starts the automatic sync of a subtitle a player was just
// served, when that subtitle was never synced. It returns at once: subtitle
// delivery never waits on it.
type PlaySyncer interface {
	SubtitlePlayed(ctx context.Context, target SyncTarget)
}

// PlaySyncHook hands played subtitles to the sync service, which is set up
// with the API routes; the Jellyfin routes share the same hook. Played
// subtitles are ignored until a service is set.
type PlaySyncHook struct {
	mu     sync.RWMutex
	syncer PlaySyncer
}

// Set connects the sync service.
func (h *PlaySyncHook) Set(syncer PlaySyncer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.syncer = syncer
}

// SubtitlePlayed passes target to the sync service, if one is set.
func (h *PlaySyncHook) SubtitlePlayed(ctx context.Context, target SyncTarget) {
	if h == nil {
		return
	}
	h.mu.RLock()
	syncer := h.syncer
	h.mu.RUnlock()
	if syncer != nil {
		syncer.SubtitlePlayed(ctx, target)
	}
}
