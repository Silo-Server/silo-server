package playback

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A page extract that passes the read cap must be killed at the first sample,
// not left to run to the extraction timeout.
func TestSubtitleReadBudgetCancelsPastItsCap(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	budget := newSubtitleReadBudget(1, 500)
	budget.interval = time.Millisecond
	reads := []int64{100, 400, 501}
	sampled := 0
	budget.read = func(int) (int64, bool) {
		sampled++
		if sampled > len(reads) {
			return reads[len(reads)-1], true
		}
		return reads[sampled-1], true
	}

	budget.watch(ctx, cancel)

	if got := budget.exceeded(); got != 501 {
		t.Fatalf("breach = %d, want the read count that passed the cap (501)", got)
	}
	if ctx.Err() == nil {
		t.Fatal("the budget did not cancel the extract")
	}
}

// Without readable I/O counters there is nothing to enforce the cap with, and
// guessing one would abandon pages that are fine. The watcher must simply stop.
func TestSubtitleReadBudgetWithoutCountersPasses(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	budget := newSubtitleReadBudget(1, 1)
	budget.interval = time.Millisecond
	budget.read = func(int) (int64, bool) { return 0, false }

	budget.watch(ctx, cancel)

	if got := budget.exceeded(); got != 0 {
		t.Fatalf("breach = %d, want 0 when the counters cannot be read", got)
	}
}

// The cap exists for the relay prefix read a windowed bitmap page pays. A
// whole-track read must read everything, an artifact-backed window reads a
// local file, and a local source's prefix read is a disk read: none are capped.
func TestPageReadBudgetAppliesOnlyToRemoteBitmapWindows(t *testing.T) {
	remote := StreamExtractOpts{
		InputPath:       "relay",
		CacheIdentity:   "virtual-identity",
		SourceCodec:     "hdmv_pgs_subtitle",
		AllowWindow:     true,
		WindowRequested: true,
		SeekSeconds:     600,
		DurationSeconds: 120,
	}
	if got := remote.pageReadBudget(); got != subtitlePageReadBudgetBytes {
		t.Fatalf("remote bitmap page budget = %d, want %d", got, subtitlePageReadBudgetBytes)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*StreamExtractOpts)
	}{
		{name: "local source", mutate: func(o *StreamExtractOpts) { o.CacheIdentity = "" }},
		{name: "whole track", mutate: func(o *StreamExtractOpts) {
			o.WindowRequested = false
			o.SeekSeconds = 0
			o.DurationSeconds = 0
			o.AllowWindow = false
		}},
		{name: "text codec", mutate: func(o *StreamExtractOpts) { o.SourceCodec = "subrip" }},
		{name: "cached .sup input", mutate: func(o *StreamExtractOpts) { o.InputIsExtractedSup = true }},
		{name: "cached text input", mutate: func(o *StreamExtractOpts) { o.InputIsExtractedText = "vtt" }},
		{name: "packet input", mutate: func(o *StreamExtractOpts) { o.Packets = &SubtitlePacketInput{} }},
	} {
		opts := remote
		tc.mutate(&opts)
		if got := opts.pageReadBudget(); got != 0 {
			t.Errorf("%s: budget = %d, want no cap", tc.name, got)
		}
	}
}

// A page killed for its read cost must answer the retryable shape with its own
// additive reason, and must arm the existing extraction backoff so a client
// retry does not pay the same read again.
func TestServeSUPExtractPageReadBudgetMapsToRetryableReason(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 600, 120)
	opts.DisableBackgroundWarm = true
	calls := 0
	extract := func(context.Context, StreamExtractOpts) error {
		calls++
		return fmt.Errorf("%w: read 525000000 bytes", ErrSubtitlePageTooCostly)
	}

	first := httptest.NewRecorder()
	err := c.ServeSUPExtract(first, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil), opts, extract)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("serve error = %v, want the retryable shape", err)
	}
	if retry.Reason != SubtitlePageTooCostlyReason {
		t.Fatalf("reason = %q, want %q", retry.Reason, SubtitlePageTooCostlyReason)
	}
	if retry.RetryAfter <= 0 {
		t.Fatalf("retry after = %s, want a positive delay", retry.RetryAfter)
	}
	if first.Body.Len() != 0 {
		t.Fatalf("body = %d bytes, want none: the page was never produced", first.Body.Len())
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1", calls)
	}

	second := httptest.NewRecorder()
	err = c.ServeSUPExtract(second, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil), opts, extract)
	if !errors.As(err, &retry) {
		t.Fatalf("second serve error = %v, want the retryable shape", err)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d after a retry inside the backoff, want 1: the cost must not be paid twice", calls)
	}
}
