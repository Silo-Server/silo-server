package handlers

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

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
	probeTargetErrorTTL = time.Minute
	maxProbeTargetKbps  = 1_000_000
)

// probeTargetCache keeps the percentile for each library scope, which changes
// only when libraries do.
type probeTargetCache struct {
	mu      sync.Mutex
	entries map[string]probeTargetEntry
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
	var key strings.Builder
	filter.WriteAccessScopeCacheKey(&key)
	key.WriteString("|quality=")
	key.WriteString(filter.MaxPlaybackQuality)
	now := time.Now()
	h.probeTargets.mu.Lock()
	entry, ok := h.probeTargets.entries[key.String()]
	h.probeTargets.mu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry.kbps
	}
	kbps, err := h.SourceBitrates.Percentile(ctx, filter, probeTargetPercentile)
	entry = probeTargetEntry{kbps: kbps, expires: now.Add(probeTargetCacheTTL)}
	if err != nil {
		slog.WarnContext(ctx, "speed test target unavailable", "component", "playback", "error", err)
		entry = probeTargetEntry{expires: now.Add(probeTargetErrorTTL)}
	}
	h.probeTargets.mu.Lock()
	if h.probeTargets.entries == nil {
		h.probeTargets.entries = map[string]probeTargetEntry{}
	}
	h.probeTargets.entries[key.String()] = entry
	h.probeTargets.mu.Unlock()
	return entry.kbps
}
