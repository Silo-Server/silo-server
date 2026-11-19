package playback

import (
	"errors"
	"testing"
	"time"
)

// deepPageOpts is one bounded bitmap page on a remote identity.
func deepPageOpts(start, duration float64) StreamExtractOpts {
	return StreamExtractOpts{
		InputPath:             "http://relay-measured-page.test/source/tok/stream",
		CacheIdentity:         "deep-page-identity",
		TrackIndex:            1,
		SourceCodec:           "hdmv_pgs_subtitle",
		SeekSeconds:           start,
		DurationSeconds:       duration,
		WindowRequested:       true,
		AllowWindow:           true,
		DisableBackgroundWarm: true,
	}
}

// A deep page whose fill is measurably too slow to reach it inside the wait must
// end with the bounded final answer as soon as that is provable, not after the
// whole budget. Production spent 95 s on a 4K fill that was parsing at about
// real time toward a page twenty minutes ahead: no retry the client can make
// would have been answered, so the spinner ran to the bound for nothing.
func TestMeasuredDeepPageEndsAsSoonAsTheFillCannotReachIt(t *testing.T) {
	dir := t.TempDir()
	cache := NewSubtitleCache(func() string { return dir })
	cache.SetPageRecoveryBudgetForTest(6 * time.Second)
	now := time.Now()
	cache.pageWaitNow = func() time.Time { return now }

	opts := deepPageOpts(1200, 120)
	fill := startProgressiveFill(t, cache, opts, pgsDisplaySetAt(10), pgsDisplaySetAt(30))
	defer fill.Discard()

	// First refusal: nothing parsed yet, so the wait opens and asks for the fill.
	err := cache.pageRecoveryRefusal(opts, 1200, 0, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("first deep page answer = %v, want the retryable shape", err)
	}
	if !retry.PageRecoveryDue {
		t.Fatal("the first refusal did not ask for the recovery fill")
	}
	if retry.CoveredUntil != 0 {
		t.Fatalf("covered_until = %v, want the artifact's 0 watermark", retry.CoveredUntil)
	}

	// Two seconds later the artifact has parsed two seconds of media: ~1x real
	// time, so 1200 s of distance cannot be covered in the 4 s of budget left.
	now = now.Add(2 * time.Second)
	err = cache.pageRecoveryRefusal(opts, 1200, 2, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	var unavailable *SubtitleExtractionUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("second deep page answer = %v, want the bounded final reason", err)
	}
	if unavailable.Reason != SubtitlePageUnavailableReason {
		t.Fatalf("final reason = %q, want %q", unavailable.Reason, SubtitlePageUnavailableReason)
	}
	if unavailable.CoveredUntil != 2 {
		t.Fatalf("covered_until = %v, want the measured watermark (2)", unavailable.CoveredUntil)
	}
	if unavailable.Waited != 2*time.Second {
		t.Fatalf("waited = %s, want the 2 s the measurement took, not the whole budget", unavailable.Waited)
	}
}

// The other side of the same measurement: a page the fill is closing on stays
// retryable, so the retry the 503 asks for is answered once the artifact reaches
// it. The reach check must never cut off a fill that is actually winning.
func TestMeasuredPageTheFillIsReachingStaysRetryable(t *testing.T) {
	dir := t.TempDir()
	cache := NewSubtitleCache(func() string { return dir })
	cache.SetPageRecoveryBudgetForTest(6 * time.Second)
	now := time.Now()
	cache.pageWaitNow = func() time.Time { return now }

	opts := deepPageOpts(150, 120)
	fill := startProgressiveFill(t, cache, opts, pgsDisplaySetAt(10), pgsDisplaySetAt(30))
	defer fill.Discard()

	if err := cache.pageRecoveryRefusal(opts, 150, 0, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("the first refusal returned no answer")
	}
	// One wall second later the artifact has parsed 100 s of media: the 50 s of
	// distance left is 0.5 s at that rate, far inside the remaining budget.
	now = now.Add(time.Second)
	err := cache.pageRecoveryRefusal(opts, 150, 100, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("reachable page answer = %v, want to keep waiting", err)
	}
	if retry.Reason != SubtitlePageNotReadyReason {
		t.Fatalf("reason = %q, want %q while the fill is reaching the page", retry.Reason, SubtitlePageNotReadyReason)
	}
	if retry.CoveredUntil != 100 {
		t.Fatalf("covered_until = %v, want the 100 s watermark", retry.CoveredUntil)
	}
}
