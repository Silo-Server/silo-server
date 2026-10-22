package handlers

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// SourceBitratePercentiler reads a bitrate percentile of the video files a
// viewer can play; *catalog.SourceBitrates in production.
type SourceBitratePercentiler interface {
	Percentile(context.Context, catalog.AccessFilter, float64) (int, error)
}

const (
	// probeTargetPercentile sets the speed test's target from the files a
	// viewer can play: a percentile, so a few files with nonsense bitrates
	// cannot set it.
	probeTargetPercentile = 0.999
	// The percentile changes only when libraries do, and on a large catalog
	// reading it scans every file for many seconds, so it is read rarely and
	// never while a capability read waits.
	probeTargetCacheTTL = 6 * time.Hour
	// A failed read is retried after a while, not on every capability read.
	probeTargetErrorTTL     = 10 * time.Minute
	probeTargetQueryTimeout = 2 * time.Minute
	// A probe result is 100 to 1,000,000 kbps, so a target must be too: a
	// quick test that reached a lower one would report a result the server
	// refuses.
	minProbeTargetKbps = 100
	maxProbeTargetKbps = 1_000_000
)

// probeTargetCache keeps the percentile for each library scope, which changes
// only when libraries do.
type probeTargetCache struct {
	mu      sync.Mutex
	entries map[string]probeTargetEntry
	reads   singleflight.Group
	// query lets one scope's read scan the catalog at a time, so the scopes
	// a restart leaves empty do not each hold a database connection at once.
	query sync.Mutex
}

type probeTargetEntry struct {
	kbps    int
	expires time.Time
}

// lookup returns the entry stored for key and whether it is still fresh.
func (c *probeTargetCache) lookup(key string) (probeTargetEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return entry, ok && time.Now().Before(entry.expires)
}

// bandwidthProbeTargetKbps is the rate a quick speed test has to prove for
// this viewer: the highest bitrate the server would plan for them, plus the
// 25% headroom Auto's 80% margin needs to send it untouched. That bitrate is
// the percentile of the files they can play, lowered to the administrator's
// cap for their network, and never below the floor of a probe result. It is 0
// when there are no bitrates to go on, or none read yet.
func (h *PlaybackHandler) bandwidthProbeTargetKbps(ctx context.Context) int {
	kbps := h.sourceBitratePercentile(ctx, AccessFilterFromContext(ctx, ""))
	if kbps <= 0 {
		return 0
	}
	if capKbps := requestBitrateCapV3(ctx); capKbps > 0 && capKbps < kbps {
		kbps = capKbps
	}
	return min(max((kbps*5+3)/4, minProbeTargetKbps), maxProbeTargetKbps)
}

func (h *PlaybackHandler) sourceBitratePercentile(ctx context.Context, filter catalog.AccessFilter) int {
	if h.SourceBitrates == nil {
		return 0
	}
	var builder strings.Builder
	filter.WriteAccessScopeCacheKey(&builder)
	builder.WriteString("|quality=")
	builder.WriteString(filter.MaxPlaybackQuality)
	key := builder.String()
	cached, fresh := h.probeTargets.lookup(key)
	if !fresh {
		// The capability read sends the last target, or none, and the scope
		// is read again in the background: one shared read per scope that
		// outlives the request that started it. A client without a target
		// treats any current reading as proof of its link.
		readCtx := context.WithoutCancel(ctx)
		h.probeTargets.reads.DoChan(key, func() (any, error) {
			h.readProbeTarget(readCtx, filter, key)
			return nil, nil
		})
	}
	return cached.kbps
}

// readProbeTarget reads a scope's percentile for the cache, as the one shared
// read for that scope.
func (h *PlaybackHandler) readProbeTarget(ctx context.Context, filter catalog.AccessFilter, key string) {
	h.probeTargets.query.Lock()
	defer h.probeTargets.query.Unlock()
	// Look again: a read that missed the cache just before another read
	// stored the target leaves it, and a failure below falls back on the
	// latest one.
	cached, fresh := h.probeTargets.lookup(key)
	if fresh {
		return
	}
	queryCtx, cancel := context.WithTimeout(ctx, probeTargetQueryTimeout)
	defer cancel()
	kbps, err := h.SourceBitrates.Percentile(queryCtx, filter, probeTargetPercentile)
	entry := probeTargetEntry{kbps: kbps, expires: time.Now().Add(probeTargetCacheTTL)}
	if err != nil {
		slog.WarnContext(ctx, "speed test target unavailable", "component", "playback", "error", err)
		// Keep the last good target until the retry.
		entry = probeTargetEntry{kbps: cached.kbps, expires: time.Now().Add(probeTargetErrorTTL)}
	}
	h.probeTargets.mu.Lock()
	defer h.probeTargets.mu.Unlock()
	if h.probeTargets.entries == nil {
		h.probeTargets.entries = map[string]probeTargetEntry{}
	}
	// Scopes come and go with profiles and their restrictions. An expired
	// one stays a cache lifetime longer as the last good target for its
	// reads, then goes.
	now := time.Now()
	for cachedKey, cachedEntry := range h.probeTargets.entries {
		if now.Sub(cachedEntry.expires) > probeTargetCacheTTL {
			delete(h.probeTargets.entries, cachedKey)
		}
	}
	h.probeTargets.entries[key] = entry
}
