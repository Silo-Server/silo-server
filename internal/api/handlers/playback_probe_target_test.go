package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

type countingPercentiles struct {
	kbps  int
	err   error
	calls int
	last  catalog.AccessFilter
}

func (c *countingPercentiles) Percentile(ctx context.Context, filter catalog.AccessFilter, fraction float64) (int, error) {
	c.calls++
	c.last = filter
	if fraction != probeTargetPercentile {
		return 0, errors.New("unexpected percentile")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return c.kbps, c.err
}

func TestBandwidthProbeTarget(t *testing.T) {
	remote := clientip.SetContext(newAuthorizedPlaybackContext(), "203.0.113.7")
	for _, tc := range []struct {
		name       string
		percentile int
		scope      *access.Scope
		want       int
	}{
		{"percentile plus headroom", 91000, nil, 113750},
		{"rounds up", 91001, nil, 113752},
		{"remote cap lowers it", 91000, &access.Scope{MaxRemoteStreamBitrateKbps: 20000}, 25000},
		{"a higher cap leaves it", 91000, &access.Scope{MaxRemoteStreamBitrateKbps: 200000}, 113750},
		{"never above the estimate bound", 11_700_000, nil, 1_000_000},
		// A remote cap of 50 kbps targets 63 by the arithmetic, which no
		// probe result may report.
		{"never below the probe floor", 91000, &access.Scope{MaxRemoteStreamBitrateKbps: 50}, 100},
		{"no bitrates, no target", 0, &access.Scope{MaxRemoteStreamBitrateKbps: 20000}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &PlaybackHandler{SourceBitrates: &countingPercentiles{kbps: tc.percentile}}
			ctx := remote
			if tc.scope != nil {
				ctx = access.SetScope(ctx, *tc.scope)
			}
			if got := h.bandwidthProbeTargetKbps(ctx); got != tc.want {
				t.Fatalf("target = %d, want %d", got, tc.want)
			}
		})
	}
}

// The percentile is read once per library scope and cached; another scope
// reads its own, and a failed read sends no target.
func TestBandwidthProbeTargetCachesPerLibraryScope(t *testing.T) {
	source := &countingPercentiles{kbps: 40000}
	h := &PlaybackHandler{SourceBitrates: source}
	all := newAuthorizedPlaybackContext()
	kids := access.SetScope(all, access.Scope{AllowedLibraryIDs: []int{3}, LibrariesRestricted: true})
	for range 3 {
		if got := h.bandwidthProbeTargetKbps(all); got != 50000 {
			t.Fatalf("target = %d", got)
		}
	}
	if source.calls != 1 {
		t.Fatalf("read the percentile %d times for one scope", source.calls)
	}
	h.bandwidthProbeTargetKbps(kids)
	if source.calls != 2 || len(source.last.AllowedLibraryIDs) != 1 || source.last.AllowedLibraryIDs[0] != 3 {
		t.Fatalf("restricted scope: calls=%d filter=%+v", source.calls, source.last.AllowedLibraryIDs)
	}

	failing := &PlaybackHandler{SourceBitrates: &countingPercentiles{err: errors.New("down")}}
	if got := failing.bandwidthProbeTargetKbps(all); got != 0 {
		t.Fatalf("failed read sent target %d", got)
	}
}

// A capability read whose client went away still caches the target it read,
// and a read that fails after a good one keeps that target until the retry.
func TestBandwidthProbeTargetSurvivesCanceledAndFailedReads(t *testing.T) {
	source := &countingPercentiles{kbps: 40000}
	h := &PlaybackHandler{SourceBitrates: source}
	canceled, cancel := context.WithCancel(newAuthorizedPlaybackContext())
	cancel()
	if got := h.bandwidthProbeTargetKbps(canceled); got != 50000 {
		t.Fatalf("canceled request: target = %d, want 50000", got)
	}

	all := newAuthorizedPlaybackContext()
	expireProbeTargets(h, time.Second)
	source.err = errors.New("down")
	if got := h.bandwidthProbeTargetKbps(all); got != 50000 {
		t.Fatalf("failed refresh: target = %d, want the last good 50000", got)
	}
	if source.calls != 2 {
		t.Fatalf("read the percentile %d times, want 2", source.calls)
	}
}

// A scope expired for longer than the cache lifetime is dropped when another
// scope is stored, so profiles that come and go do not leave their targets
// behind.
func TestBandwidthProbeTargetDropsExpiredScopes(t *testing.T) {
	h := &PlaybackHandler{SourceBitrates: &countingPercentiles{kbps: 40000}}
	all := newAuthorizedPlaybackContext()
	kids := access.SetScope(all, access.Scope{AllowedLibraryIDs: []int{3}, LibrariesRestricted: true})
	h.bandwidthProbeTargetKbps(all)
	expireProbeTargets(h, probeTargetCacheTTL+time.Second)
	h.bandwidthProbeTargetKbps(kids)
	h.probeTargets.mu.Lock()
	defer h.probeTargets.mu.Unlock()
	if len(h.probeTargets.entries) != 1 {
		t.Fatalf("kept %d scopes, want only the one just read", len(h.probeTargets.entries))
	}
}

// A failed read keeps its scope's last good target even after another
// scope's store has swept the cache.
func TestBandwidthProbeTargetKeepsLastGoodAcrossScopes(t *testing.T) {
	source := &countingPercentiles{kbps: 40000}
	h := &PlaybackHandler{SourceBitrates: source}
	all := newAuthorizedPlaybackContext()
	kids := access.SetScope(all, access.Scope{AllowedLibraryIDs: []int{3}, LibrariesRestricted: true})
	h.bandwidthProbeTargetKbps(all)
	expireProbeTargets(h, time.Second)
	h.bandwidthProbeTargetKbps(kids)
	source.err = errors.New("down")
	if got := h.bandwidthProbeTargetKbps(all); got != 50000 {
		t.Fatalf("failed refresh after another scope's store: target = %d, want the last good 50000", got)
	}
}

// Of two capability reads that miss the cache together, the one whose shared
// read starts after the other's has stored the target uses that target: it
// neither queries again nor, when that query would fail, replaces the target
// with a short-lived fallback.
func TestBandwidthProbeTargetLateReadUsesTheStoredTarget(t *testing.T) {
	source := &countingPercentiles{kbps: 40000}
	h := &PlaybackHandler{SourceBitrates: source}
	ctx := newAuthorizedPlaybackContext()
	h.bandwidthProbeTargetKbps(ctx)
	var key string
	for cached := range h.probeTargets.entries {
		key = cached
	}
	source.err = errors.New("down")
	if got := h.readProbeTarget(ctx, AccessFilterFromContext(ctx, ""), key); got != 40000 || source.calls != 1 {
		t.Fatalf("late read = %d after %d percentile reads, want the stored 40000 after 1", got, source.calls)
	}
	if entry, fresh := h.probeTargets.lookup(key); !fresh || entry.kbps != 40000 || time.Until(entry.expires) <= probeTargetErrorTTL {
		t.Fatalf("late read replaced the stored target with %+v", entry)
	}
}

// expireProbeTargets moves every cached target's expiry age into the past.
func expireProbeTargets(h *PlaybackHandler, age time.Duration) {
	h.probeTargets.mu.Lock()
	defer h.probeTargets.mu.Unlock()
	for key, entry := range h.probeTargets.entries {
		entry.expires = time.Now().Add(-age)
		h.probeTargets.entries[key] = entry
	}
}
