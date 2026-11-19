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

// A fill that is still advancing but cannot cross the page inside the wait the
// client itself will hold is answered finally at that point. Production's 1080p
// page (1074011 at 1080 s) spent the client's whole retry ladder on
// `page_not_ready` because the reach check compared the projection against the
// server's 90 s window: the fill was moving, so every refusal stayed retryable
// long after the viewer's client had stopped asking.
func TestBitmapPageTooSlowForTheClientWaitAnswersFinal(t *testing.T) {
	dir := t.TempDir()
	cache := NewSubtitleCache(func() string { return dir })
	now := time.Now()
	cache.pageWaitNow = func() time.Time { return now }
	const remote = "http://relay-slow-page.test/source/tok/stream"
	opts := StreamExtractOpts{
		InputPath:             remote,
		CacheIdentity:         "slow-page-track",
		TrackIndex:            3,
		SourceCodec:           "hdmv_pgs_subtitle",
		WindowRequested:       true,
		AllowWindow:           true,
		DisableBackgroundWarm: true,
	}
	format := subtitleCacheFormat(opts.SourceCodec, opts.TargetFormat)
	fill, _ := cache.reserveFill(remote, opts.CacheIdentity, opts.TrackIndex, format, true)
	if fill == nil {
		t.Fatal("reserve the fill the page is waiting on")
	}
	defer fill.Discard()
	cache.mu.Lock()
	fill.background = true
	cache.mu.Unlock()

	// First refusal opens the wait; the fill is in flight and covers 300 s.
	page := opts
	page.SeekSeconds = 1080
	page.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(page, 1080, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	// Ten seconds later it has parsed ten more media seconds: reaching the page
	// needs ~780 s of fill, far past the client's remaining ladder.
	now = now.Add(10 * time.Second)
	err := cache.pageRecoveryRefusal(page, 1080, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	if !IsSubtitlePageUnavailable(err) {
		t.Fatalf("an advancing but far-too-slow fill answered %v, want the final page-unavailable reason", err)
	}
}

// A fill that is about to cross the page keeps it retryable, even once the
// client's ladder is spent: the page lands a moment later and the retry (or a
// viewer pressing Retry) must be served rather than told it cannot be.
func TestBitmapPageNearTheFillStaysRetryable(t *testing.T) {
	dir := t.TempDir()
	cache := NewSubtitleCache(func() string { return dir })
	now := time.Now()
	cache.pageWaitNow = func() time.Time { return now }
	const remote = "http://relay-near-page.test/source/tok/stream"
	opts := StreamExtractOpts{
		InputPath:             remote,
		CacheIdentity:         "near-page-track",
		TrackIndex:            3,
		SourceCodec:           "hdmv_pgs_subtitle",
		WindowRequested:       true,
		AllowWindow:           true,
		DisableBackgroundWarm: true,
	}
	format := subtitleCacheFormat(opts.SourceCodec, opts.TargetFormat)
	fill, _ := cache.reserveFill(remote, opts.CacheIdentity, opts.TrackIndex, format, true)
	if fill == nil {
		t.Fatal("reserve the fill the page is waiting on")
	}
	defer fill.Discard()
	cache.mu.Lock()
	fill.background = true
	cache.mu.Unlock()

	page := opts
	page.SeekSeconds = 1080
	page.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(page, 1080, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	// Past the client's ladder, but the fill is parsing at 30x realtime and has
	// only 2 s of media left to reach the page.
	now = now.Add(subtitlePageClientWaitBudget + 10*time.Second)
	err := cache.pageRecoveryRefusal(page, 1080, 1078, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("a page the fill is about to reach answered %v, want the retryable shape", err)
	}
	if retry.Reason != SubtitlePageNotReadyReason {
		t.Fatalf("retry reason = %q, want %q", retry.Reason, SubtitlePageNotReadyReason)
	}
}

// A page's final answer stops the fill it was waiting on: the remaining read
// cannot serve the answered page, and production's answered page left it
// reading to 95 s and 3.1 MB for nobody.
func TestFinalPageAnswerCancelsTheFill(t *testing.T) {
	cache, opts, advance, canceled, release := newPageAnswerFixture(t, "answered-page-track")
	defer release()

	page := opts
	page.SeekSeconds = 1080
	page.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(page, 1080, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	// The fill advanced ten media seconds in ten wall seconds: reaching the
	// page needs far more than the client still waits, so this refusal is final.
	advance(10 * time.Second)
	if err := cache.pageRecoveryRefusal(page, 1080, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !IsSubtitlePageUnavailable(err) {
		t.Fatalf("page answer = %v, want the final page-unavailable reason", err)
	}
	if !*canceled {
		t.Fatal("the final page answer did not cancel the fill it was waiting on")
	}
}

// A second page waiting on the same track keeps its fill: another client (or a
// seek target and the playhead page) still needs the artifact.
func TestFinalPageAnswerKeepsAFillAnotherPageNeeds(t *testing.T) {
	cache, opts, advance, canceled, release := newPageAnswerFixture(t, "two-page-track")
	defer release()

	deep := opts
	deep.SeekSeconds = 1080
	deep.DurationSeconds = 120
	other := opts
	other.SeekSeconds = 1320
	other.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(deep, 1080, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	// A second page of the same track is waiting on the same fill.
	if err := cache.pageRecoveryRefusal(other, 1320, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("the second page's refusal returned no answer")
	}
	advance(10 * time.Second)
	if err := cache.pageRecoveryRefusal(deep, 1080, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !IsSubtitlePageUnavailable(err) {
		t.Fatalf("page answer = %v, want the final page-unavailable reason", err)
	}
	if *canceled {
		t.Fatal("the fill was canceled while another page of the track was still waiting")
	}
}

// newPageAnswerFixture builds a cache whose page-wait clock the test drives,
// plus the bitmap options of one track's in-flight fill and the flag its cancel
// cause sets. release unregisters the fill and discards it.
func newPageAnswerFixture(t *testing.T, identity string) (cache *SubtitleCache, opts StreamExtractOpts, advance func(time.Duration), canceled *bool, release func()) {
	t.Helper()
	dir := t.TempDir()
	cache = NewSubtitleCache(func() string { return dir })
	now := time.Now()
	cache.pageWaitNow = func() time.Time { return now }
	opts = StreamExtractOpts{
		InputPath:             "http://relay-" + identity + ".test/source/tok/stream",
		CacheIdentity:         identity,
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
	format := subtitleCacheFormat(opts.SourceCodec, opts.TargetFormat)
	fill, _ := cache.reserveFill(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format, true)
	if fill == nil {
		t.Fatal("reserve the fill the page is waiting on")
	}
	cache.mu.Lock()
	fill.background = true
	cache.mu.Unlock()
	canceled = new(bool)
	run := cache.registerWarmRun(key, func(cause error) {
		*canceled = errors.Is(cause, errSubtitleFillPageAnswered)
	})
	if run == nil {
		t.Fatal("register the fill handle the page cancels")
	}
	return cache, opts,
		func(d time.Duration) { now = now.Add(d) },
		canceled,
		func() {
			cache.unregisterWarmRun(key, run)
			fill.Discard()
		}
}
