package playback

import (
	"errors"
	"testing"
	"time"
)

// A deep bitmap page whose artifact cannot reach it must answer the final,
// non-retrying reason — including for the page the client re-anchors to on its
// next retry. Keying the wait ledger by page position gave every re-anchor a
// fresh budget, which is how the production 4K page looped on "loading" with no
// verdict.
func TestDeepBitmapPageKeepsOneTrackWaitAcrossReanchors(t *testing.T) {
	dir := t.TempDir()
	cache := NewSubtitleCache(func() string { return dir })
	cache.SetPageRecoveryBudgetForTest(50 * time.Millisecond)
	now := time.Now()
	cache.pageWaitNow = func() time.Time { return now }
	const remote = "http://relay-deep-page.test/source/tok/stream"

	// One fill for this track ended having spent the page budget the wait gave
	// it: repeating the same prefix cannot reach the page sooner.
	opts := StreamExtractOpts{
		InputPath:             remote,
		CacheIdentity:         "deep-page-track",
		TrackIndex:            3,
		SourceCodec:           "hdmv_pgs_subtitle",
		WindowRequested:       true,
		AllowWindow:           true,
		DisableBackgroundWarm: true,
	}
	key, ok := cache.pageRecoveryFillKey(opts)
	if !ok {
		t.Fatal("page recovery key unavailable for a bounded bitmap window")
	}
	cache.noteFillEnded(key, subtitleFillEndPageBudgetSpent, now)

	// First refusal at 20:00 opens the track's wait: retryable, with the
	// recovery fill due once.
	first := opts
	first.SeekSeconds = 1200
	first.DurationSeconds = 120
	err := cache.pageRecoveryRefusal(first, 1200, 0, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("first deep page answer = %v, want the retryable shape", err)
	}
	if !retry.PageRecoveryDue {
		t.Fatal("first deep page refusal did not ask for its recovery fill")
	}

	// The client re-anchors a few seconds forward on each retry. Every one of
	// those is the same track and must not restart the wait.
	now = now.Add(120 * time.Millisecond)
	for _, start := range []float64{1203, 1208, 1214} {
		page := opts
		page.SeekSeconds = start
		page.DurationSeconds = 120
		err := cache.pageRecoveryRefusal(page, start, 0, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
		if !IsSubtitlePageUnavailable(err) {
			t.Fatalf("page at %.0fs answered %v, want the final page-unavailable reason", start, err)
		}
		var unavailable *SubtitleExtractionUnavailableError
		if errors.As(err, &unavailable) && unavailable.Reason != SubtitlePageUnavailableReason {
			t.Fatalf("final page reason = %q, want %q", unavailable.Reason, SubtitlePageUnavailableReason)
		}
	}
}

// A page the artifact did reach clears the track's wait, so a later visit pays
// its own bounded wait instead of inheriting a spent budget.
func TestBitmapPageServeClearsTheTrackWait(t *testing.T) {
	dir := t.TempDir()
	cache := NewSubtitleCache(func() string { return dir })
	cache.SetPageRecoveryBudgetForTest(50 * time.Millisecond)
	now := time.Now()
	cache.pageWaitNow = func() time.Time { return now }
	const remote = "http://relay-cleared-page.test/source/tok/stream"
	opts := StreamExtractOpts{
		InputPath:       remote,
		CacheIdentity:   "cleared-page-track",
		TrackIndex:      3,
		SourceCodec:     "hdmv_pgs_subtitle",
		WindowRequested: true,
		AllowWindow:     true,
	}
	opts.SeekSeconds = 1200
	opts.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(opts, 1200, 0, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	cache.clearPageRecovery(opts, 1200)
	now = now.Add(200 * time.Millisecond)
	err := cache.pageRecoveryRefusal(opts, 1200, 0, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	if IsSubtitlePageUnavailable(err) {
		t.Fatalf("a page answered after the wait was cleared still reported the final reason: %v", err)
	}
}
