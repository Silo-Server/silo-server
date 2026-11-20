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

// A second client waiting on the same track keeps the fill: another playback
// session still needs the artifact, so answering one page must not stop the read
// it is waiting on.
func TestFinalPageAnswerKeepsAFillAnotherPageNeeds(t *testing.T) {
	cache, opts, advance, canceled, release := newPageAnswerFixture(t, "two-page-track")
	defer release()

	deep := opts
	deep.PageWaitScope = "session-deep"
	deep.SeekSeconds = 1080
	deep.DurationSeconds = 120
	other := opts
	other.PageWaitScope = "session-other"
	other.SeekSeconds = 1320
	other.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(deep, 1080, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	// A second session's page of the same track is waiting on the same fill.
	if err := cache.pageRecoveryRefusal(other, 1320, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("the second page's refusal returned no answer")
	}
	advance(10 * time.Second)
	if err := cache.pageRecoveryRefusal(deep, 1080, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !IsSubtitlePageUnavailable(err) {
		t.Fatalf("page answer = %v, want the final page-unavailable reason", err)
	}
	if *canceled {
		t.Fatal("the fill was canceled while another session's page was still waiting")
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

// A new playback session starts its own wait for the page it asks for. The
// previous session's spent budget is not evidence about this one: production
// carried waited=9m54 into a new session and answered its first page in 1 ms
// with covered=0, which made every later page of the track terminal on arrival.
func TestNewPlaybackSessionStartsANewPageWait(t *testing.T) {
	cache, opts, advance, _, release := newPageAnswerFixture(t, "new-session-track")
	defer release()
	cache.SetPageRecoveryBudgetForTest(5 * time.Second)

	first := opts
	first.PageWaitScope = "session-one"
	first.SeekSeconds = 1080
	first.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(first, 1080, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	advance(6 * time.Second)
	if err := cache.pageRecoveryRefusal(first, 1080, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !IsSubtitlePageUnavailable(err) {
		t.Fatalf("page answer in the first session = %v, want the final reason", err)
	}

	// The next session asks for the same page on the same track. It must be a
	// fresh, measured wait, not the old session's spent one.
	next := first
	next.PageWaitScope = "session-two"
	advance(time.Second)
	err := cache.pageRecoveryRefusal(next, 1080, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) || !retry.PageRecoveryDue {
		t.Fatalf("new session's first answer = %v, want the retryable shape that starts its own fill", err)
	}

	// It still ends bounded, by its own measurement.
	advance(6 * time.Second)
	var unavailable *SubtitleExtractionUnavailableError
	if err := cache.pageRecoveryRefusal(next, 1080, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !errors.As(err, &unavailable) {
		t.Fatalf("new session's page answer = %v, want its own bounded final answer", err)
	}
	if unavailable.Waited > 2*5*time.Second {
		t.Fatalf("waited = %s, want the new session's own ~5 s wait, not the previous session's total", unavailable.Waited)
	}
}

// A seek starts a new wait for the page it lands on. The page before the seek is
// not being retried any more, so its spent budget must not answer the new one.
func TestSeekStartsANewPageWait(t *testing.T) {
	cache, opts, advance, _, release := newPageAnswerFixture(t, "seek-page-track")
	defer release()
	cache.SetPageRecoveryBudgetForTest(5 * time.Second)

	watch := opts
	watch.PageWaitScope = "session-seek"
	watch.SeekSeconds = 1080
	watch.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(watch, 1080, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	advance(6 * time.Second)
	if err := cache.pageRecoveryRefusal(watch, 1080, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !IsSubtitlePageUnavailable(err) {
		t.Fatalf("page answer before the seek = %v, want the final reason", err)
	}

	// The viewer seeks back to 600 s. That page begins its own wait.
	seeked := watch
	seeked.SeekSeconds = 600
	advance(time.Second)
	err := cache.pageRecoveryRefusal(seeked, 600, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) || !retry.PageRecoveryDue {
		t.Fatalf("page after the seek = %v, want a fresh retryable wait", err)
	}
}

// A page the fill is measurably not going to reach before the client's ladder
// ends must answer finally at the retry, not after the ladder. The measurement is
// the fill's average rate since this page's first refusal; the last interval
// alone is not the rate, and production's 1080p page stayed retryable for 75 s
// because a watermark jump between two refusals looked fast.
func TestPageProjectedPastTheClientLadderAnswersAtTheRetry(t *testing.T) {
	cache, opts, advance, _, release := newPageAnswerFixture(t, "projected-page-track")
	defer release()

	page := opts
	page.PageWaitScope = "session-projected"
	page.SeekSeconds = 1200
	page.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(page, 1200, 0, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	// Forty seconds in, the fill has parsed 500 media seconds (~12.5x). The
	// 700 s left is ~56 s at that rate, inside the 35 s the client still waits
	// with the margin, so the page stays retryable.
	advance(40 * time.Second)
	var retry *SubtitleExtractionRetryError
	if err := cache.pageRecoveryRefusal(page, 1200, 500, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !errors.As(err, &retry) {
		t.Fatalf("page the fill may still reach = %v, want to keep waiting", err)
	}
	// The watermark has not moved since the last refusal, but the average rate
	// since the first refusal is ~6.6 media seconds per wall second: the 700 s
	// left needs ~105 s, past everything the client will wait.
	advance(35_400 * time.Millisecond)
	var unavailable *SubtitleExtractionUnavailableError
	if err := cache.pageRecoveryRefusal(page, 1200, 500, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !errors.As(err, &unavailable) {
		t.Fatalf("page projected past the ladder = %v, want the bounded final answer", err)
	}
	if unavailable.CoveredUntil != 500 {
		t.Fatalf("covered_until = %v, want the measured 500 s watermark", unavailable.CoveredUntil)
	}
	if unavailable.Waited < 75*time.Second {
		t.Fatalf("waited = %s, want the ladder the client actually held", unavailable.Waited)
	}
}

// The other side of the same measurement: a page the fill is about to cross
// stays retryable even when the watermark has not moved since the last refusal.
func TestPageTheFillIsAboutToCrossStaysRetryable(t *testing.T) {
	cache, opts, advance, _, release := newPageAnswerFixture(t, "about-to-cross-track")
	defer release()

	page := opts
	page.PageWaitScope = "session-near"
	page.SeekSeconds = 1200
	page.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(page, 1200, 0, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	advance(40 * time.Second)
	if err := cache.pageRecoveryRefusal(page, 1200, 1190, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("near-page refusal returned no answer")
	}
	// The watermark stalls at 1197 for the rest of the ladder, but that is 3 s
	// of media away at the average rate: the fill is about to cross it.
	advance(35_400 * time.Millisecond)
	err := cache.pageRecoveryRefusal(page, 1200, 1197, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("a page the fill is about to cross answered %v, want the retryable shape", err)
	}
}

// Answering a page finally removes it from the waiting set immediately, along
// with the earlier positions the same session asked for before it. Otherwise an
// abandoned position keeps the fill alive for a page nobody will ask for again
// (production ran a 1080 page's fill to 00:52:29 for a stale 1200 entry).
func TestAnsweredPageLeavesTheWaitingSetAndStopsTheFill(t *testing.T) {
	cache, opts, advance, canceled, release := newPageAnswerFixture(t, "answered-waiting-track")
	defer release()

	first := opts
	first.PageWaitScope = "session-answered"
	first.SeekSeconds = 1080
	first.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(first, 1080, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	// The client re-anchors a few seconds forward on the next attempt.
	reanchored := first
	reanchored.SeekSeconds = 1085
	advance(5 * time.Second)
	if err := cache.pageRecoveryRefusal(reanchored, 1085, 300, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("re-anchored refusal returned no answer")
	}
	// Ten wall seconds in, the fill has parsed ten media seconds: far too slow.
	advance(5 * time.Second)
	if err := cache.pageRecoveryRefusal(reanchored, 1085, 310, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !IsSubtitlePageUnavailable(err) {
		t.Fatalf("page answer = %v, want the final reason", err)
	}
	if !*canceled {
		t.Fatal("the final answer did not cancel the fill")
	}
	key, ok := cache.pageRecoveryKey(reanchored, 1085)
	if !ok {
		t.Fatal("page recovery key unavailable")
	}
	cache.pageWaitMu.Lock()
	defer cache.pageWaitMu.Unlock()
	if entry, seen := cache.pageWaits[key]; seen && len(entry.waiting) != 0 {
		t.Fatalf("waiting set = %v, want every answered/superseded position removed", entry.waiting)
	}
}

// A page projected past the client's ladder answers at the retry, not after it.
// The 7.9x fill measured in production needs ~145 s to reach a 1200 s page from
// zero, so the page answers on the third request (15 s in) instead of keeping
// `page_not_ready` alive for the whole 75 s ladder with nothing to show.
func TestPageProjectedPastTheClientLadderAnswersByTheThirdRetry(t *testing.T) {
	cache, opts, advance, _, release := newPageAnswerFixture(t, "third-retry-page-track")
	defer release()

	page := opts
	page.PageWaitScope = "session-third-retry"
	page.SeekSeconds = 1200
	page.DurationSeconds = 120
	if err := cache.pageRecoveryRefusal(page, 1200, 0, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal returned no answer")
	}
	// Five seconds in, ~11x: the 1145 s left is ~104 s, inside the 70 s the
	// client still waits once the margin is applied, so keep waiting.
	advance(5 * time.Second)
	if err := cache.pageRecoveryRefusal(page, 1200, 55, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("second refusal returned no answer")
	}
	// Fifteen seconds in, ~7.9x average: the 1082 s left needs ~137 s, past
	// everything the client will wait.
	advance(10 * time.Second)
	if err := cache.pageRecoveryRefusal(page, 1200, 118, subtitleFillBusyRetryAfter, SubtitlePageNotReadyReason); !IsSubtitlePageUnavailable(err) {
		t.Fatalf("third refusal of a page projected past the ladder = %v, want the bounded final answer", err)
	}
}
