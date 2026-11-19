package playback

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"time"
)

// relayWindowOpts is a bounded text window read from a virtual relay input: the
// shape a web player asks for on a slow provider source.
func relayWindowOpts(source string) StreamExtractOpts {
	return StreamExtractOpts{
		InputPath:             source,
		CacheIdentity:         "virtual-result-identity",
		SourceCodec:           "ass",
		TrackIndex:            0,
		SeekSeconds:           600,
		DurationSeconds:       120,
		WindowRequested:       true,
		DisableBackgroundWarm: true,
	}
}

// A text window whose extract never reaches the requested range must answer the
// retryable shape when its budget expires, instead of holding the request open
// for the two-minute window timeout. Before the budget existed this waited
// subtitleWindowExtractTimeout and the client abandoned at its own 30 s stall
// deadline without ever learning the page was costly.
func TestServeExtractTextWindowBudgetAnswersRetryable(t *testing.T) {
	c, _ := newTestCache(t)
	c.SetWindowExtractBudgetForTest(80 * time.Millisecond)
	opts := relayWindowOpts("http://relay.invalid/stream/subtitles.ass")

	calls := 0
	extract := func(ctx context.Context, _ StreamExtractOpts) error {
		calls++
		<-ctx.Done() // a relay that never reaches the window
		return ctx.Err()
	}

	rec := httptest.NewRecorder()
	start := time.Now()
	err := c.ServeExtract(rec, subtitleWindowRequest(), opts, extract)
	elapsed := time.Since(start)

	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("error = %v, want the retryable shape", err)
	}
	if !retry.WindowBudgetExceeded {
		t.Fatal("retryable answer does not report the window budget")
	}
	if retry.Reason != SubtitleWindowBudgetExceededReason {
		t.Fatalf("reason = %q, want %q", retry.Reason, SubtitleWindowBudgetExceededReason)
	}
	if retry.RetryAfter <= 0 {
		t.Fatalf("retry after = %s, want a positive delay", retry.RetryAfter)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("answered after %s, want the configured budget (the pre-budget path waited %s)",
			elapsed, subtitleWindowExtractTimeout)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want nothing: a breach must not stream a partial slice", rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1", calls)
	}

	// The breach armed the existing per-identity cooldown, so the retry the 503
	// asks for does not repeat the read.
	if _, _, cooling := c.extractionCoolingDown(opts, subtitleFormatASS); !cooling {
		t.Fatal("no extraction cooldown after a budget breach")
	}
	retryErr := c.ServeExtract(httptest.NewRecorder(), subtitleWindowRequest(), opts, extract)
	var second *SubtitleExtractionRetryError
	if !errors.As(retryErr, &second) {
		t.Fatalf("retry inside the cooldown error = %v, want the retryable shape", retryErr)
	}
	if second.WindowBudgetExceeded {
		t.Fatal("a cooled-down retry must not report a fresh breach")
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d after a retry inside the cooldown, want 1", calls)
	}
}

// A window the extract filled but never finished is held, not streamed: the
// budget kill must drop the held slice and answer the retryable shape. This is
// the case production hit on a 4K source — the extract wrote 1270 bytes and was
// killed, and because "bytes written" was read as "bytes delivered" the answer
// skipped the budget reason and the recovery warm the handler starts from it.
func TestServeExtractTextWindowBreachAfterHeldBytesAnswersRetryable(t *testing.T) {
	c, _ := newTestCache(t)
	c.SetWindowExtractBudgetForTest(80 * time.Millisecond)
	opts := relayWindowOpts("http://relay.invalid/stream/subtitles.ass")

	//nolint:misspell // "Dialogue" is the ASS cue keyword, not prose.
	const payload = "[Script Info]\nDialogue: 0,0:10:01.00,0:10:02.00,Default,,0,0,0,,hello\n"
	calls := 0
	extract := func(ctx context.Context, o StreamExtractOpts) error {
		calls++
		if _, err := io.WriteString(o.Writer, payload); err != nil {
			return err
		}
		<-ctx.Done() // the window is not finished when the budget expires
		return ctx.Err()
	}

	rec := httptest.NewRecorder()
	err := c.ServeExtract(rec, subtitleWindowRequest(), opts, extract)

	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("error = %v, want the retryable budget shape", err)
	}
	if !retry.WindowBudgetExceeded {
		t.Fatal("the breach after held bytes did not report the window budget: the handler starts no recovery warm")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want nothing: a held, unfinished window is never served", rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1", calls)
	}
}

// The opening window must still attempt a real extract: a page starting at
// source zero is not refused or cut short by the budget, and a fast one is
// served untouched.
func TestServeExtractTextWindowBudgetKeepsOpeningPage(t *testing.T) {
	c, _ := newTestCache(t)
	c.SetWindowExtractBudgetForTest(80 * time.Millisecond)
	opts := relayWindowOpts("http://relay.invalid/stream/subtitles.ass")
	opts.SeekSeconds = 0
	opts.DurationSeconds = 120

	//nolint:misspell // "Dialogue" is the ASS cue keyword, not prose.
	const payload = "[Script Info]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,hello\n"
	extract := func(_ context.Context, o StreamExtractOpts) error {
		_, err := io.WriteString(o.Writer, payload)
		return err
	}

	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, subtitleWindowRequest(), opts, extract); err != nil {
		t.Fatalf("opening window error = %v, want a served extract", err)
	}
	if rec.Body.String() != payload {
		t.Fatalf("opening window body = %q, want %q", rec.Body.String(), payload)
	}
}

// SubRip and WebVTT windows are covered by the same bound, but they are not
// what it is for: a fast text window must be served exactly as before.
func TestServeExtractSRTWindowUnaffectedByBudget(t *testing.T) {
	c, _ := newTestCache(t)
	c.SetWindowExtractBudgetForTest(80 * time.Millisecond)
	opts := relayWindowOpts("http://relay.invalid/stream/subtitles.srt")
	opts.SourceCodec = "subrip"

	const payload = "1\n00:10:01,000 --> 00:10:03,000\nhello\n"
	extract := func(_ context.Context, o StreamExtractOpts) error {
		_, err := io.WriteString(o.Writer, payload)
		return err
	}

	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, subtitleWindowRequest(), opts, extract); err != nil {
		t.Fatalf("SRT window error = %v, want a served extract", err)
	}
	if rec.Body.String() != payload {
		t.Fatalf("SRT window body = %q, want %q", rec.Body.String(), payload)
	}
}

// A budget breach arms the identity's extraction backoff for client retries,
// but the detached prefetch that fills the window cache must still run: blocking
// it inside the backoff it just armed would leave the page unservable.
func TestServeExtractDetachedPrefetchIgnoresTheBreachCooldown(t *testing.T) {
	c, _ := newTestCache(t)
	c.SetWindowExtractBudgetForTest(80 * time.Millisecond)
	opts := relayWindowOpts("http://relay.invalid/stream/subtitles.ass")

	slow := func(ctx context.Context, _ StreamExtractOpts) error {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := c.ServeExtract(httptest.NewRecorder(), subtitleWindowRequest(), opts, slow); err == nil {
		t.Fatal("the request-path window was not bounded")
	}
	if _, _, cooling := c.extractionCoolingDown(opts, subtitleFormatASS); !cooling {
		t.Fatal("the breach did not arm the extraction cooldown")
	}

	//nolint:misspell // "Dialogue" is the ASS cue keyword, not prose.
	const payload = "[Script Info]\nDialogue: 0,0:10:01.00,0:10:02.00,Default,,0,0,0,,hi\n"
	prefetch := opts
	prefetch.DetachedRelayDemux = true
	extract := func(_ context.Context, o StreamExtractOpts) error {
		_, err := io.WriteString(o.Writer, payload)
		return err
	}

	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, subtitleWindowRequest(), prefetch, extract); err != nil {
		t.Fatalf("detached prefetch inside the breach cooldown error = %v, want a served slice", err)
	}
	if rec.Body.String() != payload {
		t.Fatalf("detached prefetch body = %q, want %q", rec.Body.String(), payload)
	}
}

// The budget covers exactly the request-path text window a client waits on.
func TestSubtitleWindowBudgetScope(t *testing.T) {
	base := relayWindowOpts("http://relay.invalid/stream/subtitles.ass")
	cases := []struct {
		name   string
		mutate func(*StreamExtractOpts)
		want   bool
	}{
		{name: "seeked ASS window from a relay", mutate: func(*StreamExtractOpts) {}, want: true},
		{name: "opening ASS window from a relay", mutate: func(o *StreamExtractOpts) { o.SeekSeconds = 0 }, want: true},
		{name: "converted WebVTT window from a relay", mutate: func(o *StreamExtractOpts) {
			o.SourceCodec = "subrip"
			o.TargetFormat = "vtt"
		}, want: true},
		{name: "bitmap page keeps its own read budget", mutate: func(o *StreamExtractOpts) {
			o.SourceCodec = "hdmv_pgs_subtitle"
		}, want: false},
		{name: "committed text artifact is a local read", mutate: func(o *StreamExtractOpts) {
			o.InputIsExtractedText = subtitleFormatASS
		}, want: false},
		{name: "packets read through the index are a local read", mutate: func(o *StreamExtractOpts) {
			o.Packets = &SubtitlePacketInput{}
		}, want: false},
		{name: "local source has no relay read to bound", mutate: func(o *StreamExtractOpts) {
			o.CacheIdentity = ""
		}, want: false},
		{name: "whole-track fetch must read everything", mutate: func(o *StreamExtractOpts) {
			o.WindowRequested = false
			o.SeekSeconds = 0
			o.DurationSeconds = 0
		}, want: false},
		{name: "detached prefetch is allowed the window timeout", mutate: func(o *StreamExtractOpts) {
			o.DetachedRelayDemux = true
		}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := base
			tc.mutate(&opts)
			c := NewSubtitleCache(func() string { return t.TempDir() })
			budget := c.textWindowExtractBudget(opts)
			if got := budget > 0; got != tc.want {
				t.Fatalf("budgeted = %t (budget %s), want %t", got, budget, tc.want)
			}
			if tc.want && budget != subtitleTextWindowElapsedBudget {
				t.Fatalf("budget = %s, want %s", budget, subtitleTextWindowElapsedBudget)
			}
		})
	}
}

// The production budget must stay inside the client's own stall deadline, or
// the server would answer after the client had already given up.
func TestSubtitleWindowBudgetLeavesRoomUnderClientStall(t *testing.T) {
	const clientStallTimeout = 30 * time.Second
	if subtitleTextWindowElapsedBudget >= clientStallTimeout {
		t.Fatalf("window budget %s must be under the client stall deadline %s",
			subtitleTextWindowElapsedBudget, clientStallTimeout)
	}
}

// A window extract canceled by the request or the window timeout is not a
// budget breach: it must keep the error path it had.
func TestWindowExtractBoundedOnlyReportsItsOwnCause(t *testing.T) {
	plain, cancelPlain := context.WithTimeout(context.Background(), time.Minute)
	defer cancelPlain()
	if windowExtractBounded(plain) {
		t.Fatal("an unfired context reported a budget breach")
	}
	bounded, cancelBounded := context.WithTimeoutCause(context.Background(), time.Millisecond, errSubtitleWindowBudget)
	defer cancelBounded()
	<-bounded.Done()
	if !windowExtractBounded(bounded) {
		t.Fatal("a budget-expired context did not report a breach")
	}
	request, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if windowExtractBounded(request) {
		t.Fatal("a client cancellation reported a budget breach")
	}
}
