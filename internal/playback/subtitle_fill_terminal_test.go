package playback

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureSubtitleLogs installs a text slog handler for one test and returns the
// buffer. slog's default logger is process-wide, so these tests must not run in
// parallel with one another.
func captureSubtitleLogs(t *testing.T, level slog.Level) *warmLogBuffer {
	t.Helper()
	logs := &warmLogBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

// warmLogBuffer is a mutex-guarded writer the tests read while the fill writes.
type warmLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *warmLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *warmLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *warmLogBuffer) count(sub string) int {
	return strings.Count(b.String(), sub)
}

// A fill something is waiting on must end when the wait does, and it must say
// why. Production left a PGS fill running for 114 s (2.18 MB of a track whose
// page the viewer was waiting on), ended only when the session closed, and
// logged at DEBUG — so the page path had no terminal state to act on and kept
// answering `page_not_ready` until the client gave up.
func TestPageDrivenFillEndsWithinItsBudgetAndSaysWhy(t *testing.T) {
	logs := captureSubtitleLogs(t, slog.LevelDebug)
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 960, 120)
	opts.CacheIdentity = "virtual-page-budget-fill"

	extract := func(ctx context.Context, _ StreamExtractOpts) error {
		<-ctx.Done()
		return ctx.Err()
	}

	start := time.Now()
	select {
	case <-c.WarmTrackInBackgroundWithin(opts, extract, "", 120*time.Millisecond):
	case <-time.After(5 * time.Second):
		t.Fatal("page-driven fill did not end inside its budget")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fill ended after %s, want its own 120 ms budget", elapsed)
	}

	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if !ok {
		t.Fatal("fill identity did not key")
	}
	if reason := c.fillEndedSince(key, start); reason != subtitleFillEndPageBudgetSpent {
		t.Fatalf("recorded fill end = %q, want %q\n%s", reason, subtitleFillEndPageBudgetSpent, logs.String())
	}
	if logs.count("subtitle cache warm abandoned") != 1 {
		t.Fatalf("fill left no terminal abandonment line:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "reason=page_wait_budget_spent") {
		t.Fatalf("abandonment line does not name the reason:\n%s", logs.String())
	}
	// The budget ending is a deliberate stop, not a source failure: it must not
	// gate the next fill behind a failure cooldown.
	if !c.warmAdmitted(key, time.Now()) {
		t.Fatal("a page-budget fill end armed the failure cooldown")
	}
}

// A fill canceled by its caller (a seek, a session end) is a terminal state too,
// and must not be recorded as a failure.
func TestCanceledFillLogsItsEndAndDoesNotArmTheCooldown(t *testing.T) {
	logs := captureSubtitleLogs(t, slog.LevelDebug)
	c, source := newTestCache(t)
	opts := supExtractOpts(source, 0)
	opts.CacheIdentity = "virtual-canceled-fill"

	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	extract := func(context.Context, StreamExtractOpts) error {
		once.Do(func() { close(started) })
		<-release
		return ErrSubtitleWarmCanceled
	}
	done := c.WarmTrackInBackground(opts, extract, "")
	<-started
	close(release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled fill did not finish")
	}
	if logs.count("subtitle cache warm abandoned") == 0 {
		t.Fatalf("canceled fill left no terminal line:\n%s", logs.String())
	}
	key, _ := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if !c.warmAdmitted(key, time.Now()) {
		t.Fatal("a canceled fill armed the failure cooldown")
	}
}

// pageRefusalOpts is a refused bitmap page at a fixed source position.
func pageRefusalOpts(source string) StreamExtractOpts {
	opts := windowedSupOpts(source, 0, 960, 120)
	opts.CacheIdentity = "virtual-page-recovery"
	return opts
}

// The page path acts on the fill's terminal state: once the fill that was
// supposed to reach this page ended because it spent the page's own budget,
// repeating the same read cannot reach the page sooner, so the page gets the
// final answer now instead of another retryable refusal.
func TestPageRefusalIsFinalOnceItsFillSpentThePageBudget(t *testing.T) {
	c, source := newTestCache(t)
	opts := pageRefusalOpts(source)
	now := time.Now()
	c.pageWaitNow = func() time.Time { return now }

	retryAfter := 5 * time.Second
	first := c.pageRecoveryRefusal(opts, 960, 0, retryAfter, SubtitlePageNotReadyReason)
	var retry *SubtitleExtractionRetryError
	if !errors.As(first, &retry) || !retry.PageRecoveryDue {
		t.Fatalf("first refusal = %v, want the retryable shape that starts the fill", first)
	}

	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if !ok {
		t.Fatal("page identity did not key")
	}
	now = now.Add(80 * time.Second) // inside the ledger budget, before any stall window
	c.noteFillEnded(key, subtitleFillEndPageBudgetSpent, now)

	second := c.pageRecoveryRefusal(opts, 960, 0, retryAfter, SubtitlePageNotReadyReason)
	var unavailable *SubtitleExtractionUnavailableError
	if !errors.As(second, &unavailable) {
		t.Fatalf("refusal after the fill ended = %v, want the final page answer", second)
	}
	if unavailable.Reason != SubtitlePageUnavailableReason {
		t.Fatalf("reason = %q, want %q", unavailable.Reason, SubtitlePageUnavailableReason)
	}
}

// A transient fill end is not a verdict: the page keeps its retryable answer
// inside its own budget, so a provider failure does not become a final
// subtitles-unavailable verdict (the 7j rule).
func TestPageRefusalStaysRetryableAfterATransientFillFailure(t *testing.T) {
	c, source := newTestCache(t)
	opts := pageRefusalOpts(source)
	now := time.Now()
	c.pageWaitNow = func() time.Time { return now }

	retryAfter := 5 * time.Second
	if err := c.pageRecoveryRefusal(opts, 960, 0, retryAfter, SubtitlePageNotReadyReason); err == nil {
		t.Fatal("first refusal must not be nil")
	}
	key, _ := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	now = now.Add(40 * time.Second)
	c.noteFillEnded(key, "extract_error", now)

	err := c.pageRecoveryRefusal(opts, 960, 0, retryAfter, SubtitlePageNotReadyReason)
	var unavailable *SubtitleExtractionUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatalf("transient fill failure became a final verdict: %v", err)
	}
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("refusal = %v, want the retryable shape", err)
	}
}
