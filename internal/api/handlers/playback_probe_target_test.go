package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

type countingPercentiles struct {
	mu    sync.Mutex
	kbps  int
	err   error
	calls int
	last  catalog.AccessFilter
	// panics makes a read panic instead of answering.
	panics bool
	// release, when set, holds every read until it is closed.
	release chan struct{}
}

func (c *countingPercentiles) Percentile(ctx context.Context, filter catalog.AccessFilter, fraction float64) (int, error) {
	if c.release != nil {
		<-c.release
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.last = filter
	if c.panics {
		panic("percentile read failed")
	}
	if fraction != probeTargetPercentile {
		return 0, errors.New("unexpected percentile")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return c.kbps, c.err
}

func (c *countingPercentiles) set(kbps int, err error, panics bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kbps, c.err, c.panics = kbps, err, panics
}

func (c *countingPercentiles) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// advanceFakeClock moves a synctest bubble's clock forward without waiting.
func advanceFakeClock(d time.Duration) {
	time.Sleep(d) //nolint:forbidigo // Inside a synctest bubble this only advances the fake clock.
}

// settledProbeTarget is the target a capability read sends once the
// background read its first read started has finished. Call it inside a
// synctest bubble.
func settledProbeTarget(h *PlaybackHandler, ctx context.Context) int {
	h.bandwidthProbeTargetKbps(ctx)
	synctest.Wait()
	return h.bandwidthProbeTargetKbps(ctx)
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
			synctest.Test(t, func(t *testing.T) {
				h := &PlaybackHandler{SourceBitrates: &countingPercentiles{kbps: tc.percentile}}
				ctx := remote
				if tc.scope != nil {
					ctx = access.SetScope(ctx, *tc.scope)
				}
				if got := settledProbeTarget(h, ctx); got != tc.want {
					t.Fatalf("target = %d, want %d", got, tc.want)
				}
			})
		})
	}
}

// A capability read never waits for the percentile: on a large catalog the
// read takes longer than clients wait for capabilities. Reads that miss the
// cache send no target and share one background read, which outlives the
// request that started it; later reads send its target.
func TestBandwidthProbeTargetNeverWaitsForTheRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := &countingPercentiles{kbps: 40000, release: make(chan struct{})}
		h := &PlaybackHandler{SourceBitrates: source}
		ctx := newAuthorizedPlaybackContext()
		first, cancel := context.WithCancel(ctx)
		for _, read := range []context.Context{first, ctx, ctx} {
			if got := h.bandwidthProbeTargetKbps(read); got != 0 {
				t.Fatalf("target before the read finished = %d, want none", got)
			}
		}
		cancel()
		close(source.release)
		synctest.Wait()
		if got := h.bandwidthProbeTargetKbps(ctx); got != 50000 || source.count() != 1 {
			t.Fatalf("target = %d after %d reads, want 50000 after 1", got, source.count())
		}
	})
}

// The percentile is read once per library scope and cached; another scope
// reads its own. A failed read sends no target, and a read that found no
// bitrates yet is retried after the error lifetime, not the cache lifetime.
func TestBandwidthProbeTargetCachesPerLibraryScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := &countingPercentiles{kbps: 40000}
		h := &PlaybackHandler{SourceBitrates: source}
		all := newAuthorizedPlaybackContext()
		kids := access.SetScope(all, access.Scope{AllowedLibraryIDs: []int{3}, LibrariesRestricted: true})
		for range 3 {
			if got := settledProbeTarget(h, all); got != 50000 {
				t.Fatalf("target = %d", got)
			}
		}
		if source.count() != 1 {
			t.Fatalf("read the percentile %d times for one scope", source.count())
		}
		settledProbeTarget(h, kids)
		if source.count() != 2 || len(source.last.AllowedLibraryIDs) != 1 || source.last.AllowedLibraryIDs[0] != 3 {
			t.Fatalf("restricted scope: calls=%d filter=%+v", source.count(), source.last.AllowedLibraryIDs)
		}

		failing := &PlaybackHandler{SourceBitrates: &countingPercentiles{err: errors.New("down")}}
		if got := settledProbeTarget(failing, all); got != 0 {
			t.Fatalf("failed read sent target %d", got)
		}

		scanning := &countingPercentiles{}
		empty := &PlaybackHandler{SourceBitrates: scanning}
		if got := settledProbeTarget(empty, all); got != 0 {
			t.Fatalf("no bitrates sent target %d", got)
		}
		scanning.set(40000, nil, false)
		advanceFakeClock(probeTargetErrorTTL + time.Second)
		if got := settledProbeTarget(empty, all); got != 50000 {
			t.Fatalf("after the error lifetime: target = %d, want 50000", got)
		}
	})
}

// A stale target is still sent while it is read again. A read that fails,
// or panics, keeps the last good target, including after another scope's
// store swept the cache, and is retried after the error lifetime.
func TestBandwidthProbeTargetKeepsLastGoodWhileRefreshing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := &countingPercentiles{kbps: 40000}
		h := &PlaybackHandler{SourceBitrates: source}
		all := newAuthorizedPlaybackContext()
		kids := access.SetScope(all, access.Scope{AllowedLibraryIDs: []int{3}, LibrariesRestricted: true})
		settledProbeTarget(h, all)

		advanceFakeClock(probeTargetCacheTTL + time.Second)
		settledProbeTarget(h, kids)
		source.set(0, errors.New("down"), false)
		if got := h.bandwidthProbeTargetKbps(all); got != 50000 {
			t.Fatalf("stale target = %d, want the last good 50000 while it is read again", got)
		}
		synctest.Wait()
		if got := h.bandwidthProbeTargetKbps(all); got != 50000 || source.count() != 3 {
			t.Fatalf("failed refresh: target = %d after %d reads, want the last good 50000 after 3", got, source.count())
		}

		source.set(64000, nil, true)
		advanceFakeClock(probeTargetErrorTTL + time.Second)
		if got := settledProbeTarget(h, all); got != 50000 || source.count() != 4 {
			t.Fatalf("panicking refresh: target = %d after %d reads, want the last good 50000 after 4", got, source.count())
		}

		source.set(64000, nil, false)
		advanceFakeClock(probeTargetErrorTTL + time.Second)
		if got := settledProbeTarget(h, all); got != 80000 {
			t.Fatalf("retry after a failure: target = %d, want 80000", got)
		}
	})
}

// A scope expired for longer than the cache lifetime is dropped when another
// scope is stored, so profiles that come and go do not leave their targets
// behind.
func TestBandwidthProbeTargetDropsExpiredScopes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &PlaybackHandler{SourceBitrates: &countingPercentiles{kbps: 40000}}
		all := newAuthorizedPlaybackContext()
		kids := access.SetScope(all, access.Scope{AllowedLibraryIDs: []int{3}, LibrariesRestricted: true})
		settledProbeTarget(h, all)
		advanceFakeClock(2*probeTargetCacheTTL + time.Second)
		settledProbeTarget(h, kids)
		h.probeTargets.mu.Lock()
		defer h.probeTargets.mu.Unlock()
		if len(h.probeTargets.entries) != 1 {
			t.Fatalf("kept %d scopes, want only the one just read", len(h.probeTargets.entries))
		}
	})
}
