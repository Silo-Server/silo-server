package playback

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A recovery extraction is the one read a refused window is waiting on. Under
// the production shape — the session's own background fill holds the detached
// budget and client windows hold the request-path budget — it must still be
// admitted, and the retry must be served from the artifact it produced instead
// of paying the relay read (26 s on the measured 4K release) again.
func TestPrefetchWindowsRecoveryIsAdmittedUnderRelayPressure(t *testing.T) {
	dir := t.TempDir()
	guard := newRelayDemuxGuardWithPools(2, 1, 1, 5*time.Millisecond)
	// The recovery wait is bounded but generous; keep the test fast.
	guard.recoveryWait = 50 * time.Millisecond
	swapRelayDemuxGuardForTest(t, guard)
	const remote = "http://relay-recovery.test/source/tok/stream"

	// Saturate every opportunistic budget: both request-path slots and the
	// single detached slot, which is exactly what a 4K session looks like while
	// its PGS fill runs and its windows are refused.
	releaseRequestA := holdRelaySlot(t, remote)
	releaseRequestB := holdRelaySlot(t, remote)
	releaseDetached := holdDetachedRelaySlot(t, remote)
	defer releaseRequestA()
	defer releaseRequestB()
	defer releaseDetached()

	cache := NewSubtitleCache(func() string { return dir })
	var extracts atomic.Int32
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		extracts.Add(1)
		_, err := io.WriteString(opts.Writer, prefetchWindowVTT)
		return err
	}
	opts := StreamExtractOpts{
		InputPath:         remote,
		CacheIdentity:     "relay-recovery-window",
		TrackIndex:        5,
		SourceCodec:       "subrip",
		SeekSeconds:       0,
		DurationSeconds:   120,
		WindowRequested:   true,
		WaitingRelayDemux: true,
	}
	windows := []SubtitlePrefetchWindow{{StartSeconds: 240, DurationSeconds: 120}}

	select {
	case <-cache.PrefetchWindowsInBackground(opts, windows, extract, "recovery-prefetch"):
	case <-time.After(5 * time.Second):
		t.Fatal("recovery prefetch did not settle")
	}
	if got := extracts.Load(); got != 1 {
		t.Fatalf("recovery prefetch extracts = %d, want 1 under relay pressure", got)
	}

	// The retry the client sends after its Retry-After window is served from the
	// artifact: no second relay read.
	retry := opts
	retry.WaitingRelayDemux = false
	retry.DisableBackgroundWarm = true
	retry.SeekSeconds = 240
	retry.DurationSeconds = 120
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=240&duration=120", nil)
	if _, err := cache.ServeExtractWithResult(rec, req, retry, func(context.Context, StreamExtractOpts) error {
		t.Error("the retry re-extracted instead of being served from the recovery artifact")
		return nil
	}); err != nil {
		t.Fatalf("serve the recovered window: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "prefetched") {
		t.Fatalf("recovered window body = %q", rec.Body.String())
	}
	if got := extracts.Load(); got != 1 {
		t.Fatalf("extracts after the retry = %d, want the single recovery read", got)
	}
}

// An opportunistic prefetch keeps its old behavior: under the same pressure it
// is dropped rather than adding a demux the viewers are not waiting on.
func TestPrefetchWindowsOpportunisticStillSkipsUnderRelayPressure(t *testing.T) {
	dir := t.TempDir()
	guard := newRelayDemuxGuardWithPools(2, 1, 1, 5*time.Millisecond)
	guard.recoveryWait = 50 * time.Millisecond
	swapRelayDemuxGuardForTest(t, guard)
	const remote = "http://relay-opportunistic.test/source/tok/stream"

	releaseRequest := holdRelaySlot(t, remote)
	releaseDetached := holdDetachedRelaySlot(t, remote)
	defer releaseRequest()
	defer releaseDetached()

	cache := NewSubtitleCache(func() string { return dir })
	var extracts atomic.Int32
	opts := StreamExtractOpts{
		InputPath:       remote,
		CacheIdentity:   "relay-opportunistic-window",
		TrackIndex:      5,
		SourceCodec:     "subrip",
		WindowRequested: true,
		DurationSeconds: 120,
	}
	select {
	case <-cache.PrefetchWindowsInBackground(opts, []SubtitlePrefetchWindow{{StartSeconds: 240, DurationSeconds: 120}}, func(context.Context, StreamExtractOpts) error {
		extracts.Add(1)
		return nil
	}, "opportunistic"):
	case <-time.After(5 * time.Second):
		t.Fatal("opportunistic prefetch did not settle")
	}
	if got := extracts.Load(); got != 0 {
		t.Fatalf("opportunistic prefetch extracted %d times under pressure, want 0", got)
	}
}
