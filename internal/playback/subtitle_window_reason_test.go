package playback

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A retry that lands while the covering extraction a budget refusal started is
// still running must keep the refusal's reason. Before this, the waiter timed
// out of its 5 s join and answered a generic subtitle_source_unavailable, so the
// client treated a working recovery read as a broken provider: production
// answered 503 in 5033 ms with the generic code instead of the budget reason
// that started the warm.
func TestWindowFlightTimeoutKeepsTheReasonThatStartedTheWarm(t *testing.T) {
	cache, source := newTestCache(t)
	cache.SetWindowWaitTimeoutForTest(50 * time.Millisecond)

	// The recovery warm the breach started: the breached window plus a lead,
	// admitted as the read a refused request is waiting on.
	warm := StreamExtractOpts{
		InputPath:            source,
		TrackIndex:           0,
		SourceCodec:          "subrip",
		SeekSeconds:          580,
		DurationSeconds:      240,
		WindowRequested:      true,
		WaitingRelayDemux:    true,
		WaitingRefusalReason: SubtitleWindowBudgetExceededReason,
	}
	warmKey, ok := cache.windowResultKey(warm, SubtitleFormatVTTV3)
	if !ok {
		t.Fatal("the recovery warm has no window key")
	}
	if leader, _, _ := cache.beginWindowFlight(warmKey, warm); !leader {
		t.Fatal("the recovery warm was not the leader of its own window")
	}
	// The warm is still extracting: no result has been stored.

	// The retry the 503 asks for: a later, narrower window inside the warm. It
	// joins the flight and waits out its bound.
	retry := StreamExtractOpts{
		InputPath:             source,
		TrackIndex:            0,
		SourceCodec:           "subrip",
		SeekSeconds:           590,
		DurationSeconds:       120,
		WindowRequested:       true,
		DisableBackgroundWarm: true,
	}
	_, err := cache.ServeExtractWithResult(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), retry,
		func(context.Context, StreamExtractOpts) error {
			t.Error("the retry extracted the source instead of waiting for the recovery warm")
			return nil
		})
	var retryErr *SubtitleExtractionRetryError
	if !errors.As(err, &retryErr) {
		t.Fatalf("serve error = %v, want the retryable shape", err)
	}
	if retryErr.Reason != SubtitleWindowBudgetExceededReason {
		t.Fatalf("reason = %q, want %q: a retry after a breach must keep naming the budget, not a source failure",
			retryErr.Reason, SubtitleWindowBudgetExceededReason)
	}
	if retryErr.RetryAfter <= 0 {
		t.Fatalf("retry after = %s, want a positive delay the client can honor", retryErr.RetryAfter)
	}
	if retryErr.WindowBudgetExceeded {
		t.Fatal("a waiter that timed out must not start a second recovery warm")
	}
}

// An ordinary prefetch publishes no refusal reason, so a retry that joins it and
// times out keeps the existing generic retryable answer.
func TestWindowFlightTimeoutStaysGenericWithoutARecoveryReason(t *testing.T) {
	cache, source := newTestCache(t)
	cache.SetWindowWaitTimeoutForTest(50 * time.Millisecond)

	prefetch := StreamExtractOpts{
		InputPath:       source,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     240,
		DurationSeconds: 120,
		WindowRequested: true,
	}
	key, ok := cache.windowResultKey(prefetch, SubtitleFormatVTTV3)
	if !ok {
		t.Fatal("the prefetch has no window key")
	}
	if leader, _, _ := cache.beginWindowFlight(key, prefetch); !leader {
		t.Fatal("the prefetch was not the leader of its own window")
	}
	retry := prefetch
	retry.SeekSeconds = 250
	retry.DurationSeconds = 50
	retry.DisableBackgroundWarm = true
	err := cache.ServeExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), retry,
		func(context.Context, StreamExtractOpts) error {
			t.Error("the retry extracted instead of waiting for the prefetch")
			return nil
		})
	var retryErr *SubtitleExtractionRetryError
	if !errors.As(err, &retryErr) {
		t.Fatalf("serve error = %v, want the retryable shape", err)
	}
	if retryErr.Reason != "" {
		t.Fatalf("reason = %q, want the generic answer for an unlabelled flight", retryErr.Reason)
	}
}

// A retry that lands inside the backoff a budget breach armed keeps the budget
// reason too: the generic answer on that path told the client the provider had
// failed when the server had only decided this window costs more than one
// request may spend.
func TestWindowCooldownAfterABreachKeepsTheBudgetReason(t *testing.T) {
	cache, source := newTestCache(t)
	opts := StreamExtractOpts{
		InputPath:       source,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     1680,
		DurationSeconds: 120,
		WindowRequested: true,
		CacheIdentity:   "virtual-text-breach",
	}
	cache.SetWindowExtractBudgetForTest(time.Millisecond)
	// Slow enough that the request-path budget kills it.
	extract := func(ctx context.Context, opts StreamExtractOpts) error {
		<-ctx.Done()
		return ctx.Err()
	}
	_, err := cache.ServeExtractWithResult(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), opts, extract)
	var breach *SubtitleExtractionRetryError
	if !errors.As(err, &breach) {
		t.Fatalf("breached window error = %v, want the retryable shape", err)
	}
	if breach.Reason != SubtitleWindowBudgetExceededReason {
		t.Fatalf("breach reason = %q, want %q", breach.Reason, SubtitleWindowBudgetExceededReason)
	}

	// The client's immediate retry: inside the cooldown the breach armed, so it
	// must answer the same reason.
	retry := opts
	retry.SeekSeconds = 1710
	_, err = cache.ServeExtractWithResult(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), retry,
		func(context.Context, StreamExtractOpts) error {
			t.Error("the retry read the source inside the cooldown")
			return nil
		})
	var cooling *SubtitleExtractionRetryError
	if !errors.As(err, &cooling) {
		t.Fatalf("retry error = %v, want the retryable shape", err)
	}
	if cooling.Reason != SubtitleWindowBudgetExceededReason {
		t.Fatalf("retry reason = %q, want %q: the cooldown must remember what armed it",
			cooling.Reason, SubtitleWindowBudgetExceededReason)
	}
}

// The covering-window invariant on its own: a finished artifact serves a request
// whose window it covers, whatever the playhead has done since. The cache never
// consults the playhead, and this pins that — the production failure was a
// window the artifact no longer reached, not an advanced playhead.
func TestCoveringWindowServesAfterThePlayheadHasMovedOn(t *testing.T) {
	cache, source := newTestCache(t)
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		_, err := io.WriteString(opts.Writer, "WEBVTT\n\n00:29:00.000 --> 00:29:01.000\ncovering\n")
		return err
	}
	warm := StreamExtractOpts{
		InputPath:             source,
		TrackIndex:            0,
		SourceCodec:           "subrip",
		SeekSeconds:           1680,
		DurationSeconds:       240,
		WindowRequested:       true,
		DisableBackgroundWarm: true,
	}
	if _, err := cache.ServeExtractWithResult(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), warm, extract); err != nil {
		t.Fatalf("store the covering window: %v", err)
	}

	// The client's retry window is inside the warm's range; the live playhead is
	// far past it by now and nothing about that may reject the slice.
	for _, request := range []struct{ seek, duration float64 }{
		{seek: 1740, duration: 120},
		{seek: 1800, duration: 120},
	} {
		retry := warm
		retry.SeekSeconds = request.seek
		retry.DurationSeconds = request.duration
		rec := httptest.NewRecorder()
		_, err := cache.ServeExtractWithResult(rec, httptest.NewRequest(http.MethodGet, "/", nil), retry,
			func(context.Context, StreamExtractOpts) error {
				t.Errorf("window %.0f+%.0f was re-extracted although the warm covers it", request.seek, request.duration)
				return nil
			})
		if err != nil {
			t.Fatalf("serve window %.0f+%.0f: %v", request.seek, request.duration, err)
		}
		if !strings.Contains(rec.Body.String(), "covering") {
			t.Fatalf("serve window %.0f+%.0f body = %q", request.seek, request.duration, rec.Body.String())
		}
	}
}
