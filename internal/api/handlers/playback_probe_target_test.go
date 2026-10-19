package handlers

import (
	"context"
	"errors"
	"testing"

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

func (c *countingPercentiles) Percentile(_ context.Context, filter catalog.AccessFilter, fraction float64) (int, error) {
	c.calls++
	c.last = filter
	if fraction != probeTargetPercentile {
		return 0, errors.New("unexpected percentile")
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
