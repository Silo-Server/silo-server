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
	// An 18 GB container: this page's discarded prefix is priced far past the
	// page budget before any read starts.
	c.notePageReadRate(opts.CacheIdentity, 3_000_000)

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page whose prefix is priced beyond the budget")
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

// A fill that stops advancing ends the wait immediately instead of accepting
// retries until the budget lapses: nothing is ever going to cross the page.
func TestStalledBitmapArtifactEndsThePageWait(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 600, 120)
	c.SetPageRecoveryBudgetForTest(time.Hour)
	now := time.Unix(1_700_000_000, 0)
	c.pageWaitNow = func() time.Time { return now }

	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10))
	defer fill.Discard()
	extract := func(context.Context, StreamExtractOpts) error { return nil }

	if err := serveSUPPage(t, c, opts, extract); !errors.As(err, new(*SubtitleExtractionRetryError)) {
		t.Fatalf("first answer = %v, want the retryable shape", err)
	}
	now = now.Add(subtitlePageRecoveryStallWindow + time.Second)
	if err := serveSUPPage(t, c, opts, extract); !IsSubtitlePageUnavailable(err) {
		t.Fatalf("stalled artifact answer = %v, want the final shape", err)
	}
}
