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
	probeTargetCacheTTL   = time.Hour
	// A failed read is retried soon, without a query on every capability read.
	probeTargetErrorTTL     = time.Minute
	probeTargetQueryTimeout = 10 * time.Second
	maxProbeTargetKbps      = 1_000_000
)

// probeTargetCache keeps the percentile for each library scope, which changes
// only when libraries do.
type probeTargetCache struct {
	mu      sync.Mutex
	entries map[string]probeTargetEntry
	reads   singleflight.Group
}

type probeTargetEntry struct {
	kbps    int
	expires time.Time
}

// bandwidthProbeTargetKbps is the rate a quick speed test has to prove for
// this viewer: the highest bitrate the server would plan for them, plus the
// 25% headroom Auto's 80% margin needs to send it untouched. That bitrate is
// the percentile of the files they can play, lowered to the administrator's
// cap for their network. It is 0 when there are no bitrates to go on.
func (h *PlaybackHandler) bandwidthProbeTargetKbps(ctx context.Context) int {
	kbps := h.sourceBitratePercentile(ctx, AccessFilterFromContext(ctx, ""))
	if kbps <= 0 {
		return 0
	}
	if capKbps := requestBitrateCapV3(ctx); capKbps > 0 && capKbps < kbps {
		kbps = capKbps
	}
	return min((kbps*5+3)/4, maxProbeTargetKbps)
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
	h.probeTargets.mu.Lock()
	cached, ok := h.probeTargets.entries[key]
	h.probeTargets.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.kbps
	}
	// Capability reads for one scope share a query, and it outlives the
	// request that started it, so a client that disconnects mid-read does
	// not leave a failure cached for everyone.
	target, _, _ := h.probeTargets.reads.Do(key, func() (any, error) {
		queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeTargetQueryTimeout)
		defer cancel()
		kbps, err := h.SourceBitrates.Percentile(queryCtx, filter, probeTargetPercentile)
		entry := probeTargetEntry{kbps: kbps, expires: time.Now().Add(probeTargetCacheTTL)}
		if err != nil {
			slog.WarnContext(ctx, "speed test target unavailable", "component", "playback", "error", err)
			// Keep the last good target while the read is retried soon.
			entry = probeTargetEntry{kbps: cached.kbps, expires: time.Now().Add(probeTargetErrorTTL)}
		}
		h.probeTargets.mu.Lock()
		if h.probeTargets.entries == nil {
			h.probeTargets.entries = map[string]probeTargetEntry{}
		}
		// Scopes come and go with profiles and their restrictions; an expired
		// one is read again on its next use, so it need not be kept.
		now := time.Now()
		for cachedKey, cachedEntry := range h.probeTargets.entries {
			if !now.Before(cachedEntry.expires) {
				delete(h.probeTargets.entries, cachedKey)
			}
		}
		h.probeTargets.entries[key] = entry
		h.probeTargets.mu.Unlock()
		return entry.kbps, nil
	})
	kbps, _ := target.(int)
	return kbps
}
