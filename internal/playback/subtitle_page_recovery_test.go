package playback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A page refused for its read cost asks the handler for the one recovery fill it
// is allowed. The retry is then served from that fill's artifact, and every later
// refusal for the same page says the fill already owns it — so a request storm
// cannot start a second warm.
func TestRefusedBitmapPageStartsOneRecoveryWarmAndRetryIsServed(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 600, 120)
	opts.DisableBackgroundWarm = true // the handler owns the fill for a virtual source
	// An 18 GB container over two hours reads ~2.5 MB/s, so this page's 120 s
	// window is ~300 MB; the identity has been measured at 1 MB/s, which is
	// 120 MB per page extract deadline. The window cannot fit, so the page is
	// refused before its source read.
	opts.SourceDurationSeconds = 7200
	c.notePageReadRate(opts.CacheIdentity, 1_000_000)

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a window that cannot fit its extract deadline")
		return nil
	}

	var retry *SubtitleExtractionRetryError
	first := serveSUPPage(t, c, opts, extract)
	if !errors.As(first, &retry) || retry.Reason != SubtitlePageTooCostlyReason {
		t.Fatalf("first refusal = %v, want the page-too-costly retry shape", first)
	}
	if !retry.PageRecoveryDue {
		t.Fatal("the first refusal did not ask for the page's one recovery fill")
	}

	second := serveSUPPage(t, c, opts, extract)
	retry = nil
	if !errors.As(second, &retry) {
		t.Fatalf("second refusal = %v, want the retryable shape", second)
	}
	if retry.PageRecoveryDue {
		t.Fatal("the second refusal asked for a second recovery fill")
	}

	// The recovery fill parsed the page. The retry is served from the artifact
	// with the display sets that show inside the page.
	page := supSets(pgsDisplaySetAt(610), pgsDisplaySetAt(700))
	fill := startProgressiveFill(t, c, opts, page)
	defer fill.Discard()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=600&duration=120", nil)
	if err := c.ServeSUPExtract(rec, req, opts, extract); err != nil {
		t.Fatalf("page inside the artifact's watermark: %v", err)
	}
	if !bytes.Contains(rec.Body.Bytes(), pgsDisplaySetAt(700)) {
		t.Fatalf("served page is missing its display sets (%d bytes)", rec.Body.Len())
	}
}

// serveSUPPage issues one bounded .sup page request and returns the serve error.
func serveSUPPage(t *testing.T, c *SubtitleCache, opts StreamExtractOpts, extract SUPExtractFunc) error {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=600&duration=120", nil)
	return c.ServeSUPExtract(httptest.NewRecorder(), req, opts, extract)
}

// A page whose artifact never reaches it must not be retried forever: the wait
// is bounded, and once it is spent the answer is the final unavailable shape. A
// page that does advance stays retryable inside the same budget.
func TestUnreachableBitmapPageAnswersOnceBounded(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 600, 120)
	c.SetPageRecoveryBudgetForTest(30 * time.Second)
	now := time.Unix(1_700_000_000, 0)
	c.pageWaitNow = func() time.Time { return now }

	// A running fill has parsed only the first ten seconds of a page at 600 s.
	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10))
	defer fill.Discard()
	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page ahead of the watermark")
		return nil
	}

	first := serveSUPPage(t, c, opts, extract)
	var retry *SubtitleExtractionRetryError
	if !errors.As(first, &retry) || retry.Reason != SubtitlePageNotReadyReason {
		t.Fatalf("first answer = %v, want the page-not-ready retry shape", first)
	}
	if !retry.PageRecoveryDue {
		t.Fatal("the first refusal did not mark the page's recovery")
	}
	if retry.CoveredUntil != 10 {
		t.Fatalf("covered_until = %v, want 10", retry.CoveredUntil)
	}

	// Inside the budget, with the watermark advancing, the page stays retryable
	// and no second recovery is announced.
	now = now.Add(10 * time.Second)
	_, _ = fill.Tee(io.Discard).Write(pgsDisplaySetAt(200))
	second := serveSUPPage(t, c, opts, extract)
	if !errors.As(second, &retry) || retry.Reason != SubtitlePageNotReadyReason {
		t.Fatalf("second answer = %v, want the retryable shape inside the budget", second)
	}
	if retry.PageRecoveryDue {
		t.Fatal("the retry announced another recovery fill")
	}

	// Past the budget the same page is answered finally.
	now = now.Add(25 * time.Second)
	third := serveSUPPage(t, c, opts, extract)
	if !IsSubtitlePageUnavailable(third) {
		t.Fatalf("third answer = %v, want the final page-unavailable shape", third)
	}
	var unavailable *SubtitleExtractionUnavailableError
	if !errors.As(third, &unavailable) {
		t.Fatalf("final answer is not *SubtitleExtractionUnavailableError: %T", third)
	}
	if unavailable.Reason != SubtitlePageUnavailableReason {
		t.Fatalf("reason = %q, want %q", unavailable.Reason, SubtitlePageUnavailableReason)
	}

	// And it stays final: the client's next attempt cannot restart the wait.
	if next := serveSUPPage(t, c, opts, extract); !IsSubtitlePageUnavailable(next) {
		t.Fatalf("answer after the final refused = %v, want the final shape again", next)
	}
}

// A stalled watermark only ends the page wait once nothing is filling the track
// any more: a running fill is still the thing that could cross the page, and the
// recovery budget bounds it. Coverage that has not moved with no fill left to
// move it is the dead end, and that answer is final.
func TestStalledBitmapArtifactEndsThePageWaitOnlyWithNoFillLeft(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 600, 120)
	c.SetPageRecoveryBudgetForTest(time.Hour)
	// The page's window is priced beyond the budget at this observed read rate,
	// so a request with nothing left to fill it is refused before it reads the
	// source.
	opts.SourceDurationSeconds = 7200
	c.notePageReadRate(opts.CacheIdentity, 1_000_000)
	now := time.Unix(1_700_000_000, 0)
	c.pageWaitNow = func() time.Time { return now }

	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10))
	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page nothing is filling")
		return nil
	}

	if err := serveSUPPage(t, c, opts, extract); !errors.As(err, new(*SubtitleExtractionRetryError)) {
		t.Fatalf("first answer = %v, want the retryable shape", err)
	}

	// The client's own backoff is longer than the old window. A page that is
	// still being filled stays retryable however long it has waited: the fill is
	// slow, not dead.
	now = now.Add(subtitlePageRecoveryStallWindow + time.Second)
	var retry *SubtitleExtractionRetryError
	if err := serveSUPPage(t, c, opts, extract); !errors.As(err, &retry) {
		t.Fatalf("answer with a fill in flight = %v, want the retryable shape", err)
	}

	// With the fill gone and the watermark still short of the page, nothing can
	// advance it. That wait ends, once.
	opts.DisableBackgroundWarm = true
	fill.Discard()
	now = now.Add(subtitlePageRecoveryStallWindow + time.Second)
	if err := serveSUPPage(t, c, opts, extract); !IsSubtitlePageUnavailable(err) {
		t.Fatalf("stalled artifact answer = %v, want the final shape", err)
	}
}

// The stall window is measured from the client's last refusal, so it must be
// longer than the client's own page retry ladder (5, 10, 20, 40 s). A window
// shorter than the longest gap finalized a page whose fill was healthy whenever
// the client legitimately backed off.
func TestPageRecoveryStallWindowOutlastsTheClientRetryLadder(t *testing.T) {
	const clientLongestBackoff = 40 * time.Second
	if subtitlePageRecoveryStallWindow <= clientLongestBackoff {
		t.Fatalf("stall window = %s, want more than the client's %s backoff", subtitlePageRecoveryStallWindow, clientLongestBackoff)
	}
	if subtitlePageRecoveryStallWindow >= subtitlePageRecoveryBudget {
		t.Fatalf("stall window = %s, want it inside the %s recovery budget", subtitlePageRecoveryStallWindow, subtitlePageRecoveryBudget)
	}
}
