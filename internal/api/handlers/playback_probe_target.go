package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/logredact"
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
	// reading marks the scopes a background read is refreshing, so a scope
	// has at most one.
	reading map[string]bool
	// query lets one scope's read scan the catalog at a time, so the scopes
	// a restart leaves empty do not each hold a database connection at once.
	query sync.Mutex
}

type probeTargetEntry struct {
	kbps    int
	expires time.Time
}

// claim returns the target stored for key, and whether the caller is to read
// it again: it is missing or stale and no read of it is running. A claimed
// read runs until finish.
func (c *probeTargetCache) claim(key string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if ok && time.Now().Before(entry.expires) || c.reading[key] {
		return entry.kbps, false
	}
	if c.reading == nil {
		c.reading = map[string]bool{}
	}
	c.reading[key] = true
	return entry.kbps, true
}

// finish stores what the claimed read of key found and ends it. A failed read
// keeps the last good target until its retry.
func (c *probeTargetCache) finish(key string, kbps int, failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.reading, key)
	now := time.Now()
	entry := probeTargetEntry{kbps: kbps, expires: now.Add(probeTargetCacheTTL)}
	switch {
	case failed:
		entry = probeTargetEntry{kbps: c.entries[key].kbps, expires: now.Add(probeTargetErrorTTL)}
	case kbps <= 0:
		// No bitrates yet, as while a first scan runs: read again as soon
		// as after a failure.
		entry.expires = now.Add(probeTargetErrorTTL)
	}
	if c.entries == nil {
		c.entries = map[string]probeTargetEntry{}
	}
	// Scopes come and go with profiles and their restrictions. An expired
	// one stays a cache lifetime longer as the last good target for its
	// reads, then goes.
	for cachedKey, cachedEntry := range c.entries {
		if now.Sub(cachedEntry.expires) > probeTargetCacheTTL {
			delete(c.entries, cachedKey)
		}
	}
	c.entries[key] = entry
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
	// The capability read sends the last target, or none, and a stale scope
	// is read again in the background, by one read that outlives the request
	// that started it. A client without a target treats any current reading
	// as proof of its link.
	kbps, read := h.probeTargets.claim(key)
	if read {
		go h.readProbeTarget(context.WithoutCancel(ctx), filter, key)
	}
	return kbps
}

// readProbeTarget is the claimed read of a scope's percentile. It runs on its
// own goroutine, out of reach of the request's panic recovery, so a panic is
// logged and counts as a failed read.
func (h *PlaybackHandler) readProbeTarget(ctx context.Context, filter catalog.AccessFilter, key string) {
	kbps, failed := 0, true
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(ctx, "speed test target read panicked", "component", "playback",
				"panic", logredact.SanitizeText(fmt.Sprint(recovered)), "stack", string(debug.Stack()))
		}
		h.probeTargets.finish(key, kbps, failed)
	}()
	h.probeTargets.query.Lock()
	defer h.probeTargets.query.Unlock()
	queryCtx, cancel := context.WithTimeout(ctx, probeTargetQueryTimeout)
	defer cancel()
	kbps, err := h.SourceBitrates.Percentile(queryCtx, filter, probeTargetPercentile)
	if err != nil {
		slog.WarnContext(ctx, "speed test target unavailable", "component", "playback", "error", err)
		return
	}
	failed = false
}
