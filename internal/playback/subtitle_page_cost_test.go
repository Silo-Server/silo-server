package playback

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A page's discarded prefix can never hold more than the whole container. A seek
// past the end of a known-size source, or a size and bitrate that disagree, must
// not price the page above the source and refuse a page it can serve.
func TestBitmapPagePrefixIsClampedToTheKnownSourceSize(t *testing.T) {
	cases := []struct {
		name        string
		size        int64
		duration    float64
		bitrateKbps int
		seek        float64
	}{
		{
			name:     "seek past the end of the source",
			size:     200 << 20,
			duration: 3600,
			seek:     7200,
		},
		{
			name:        "bitrate that disagrees with the declared size",
			size:        8 << 20,
			bitrateKbps: 28000,
			seek:        1200,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, source := newTestCache(t)
			opts := progressiveSUPOpts(source, tc.seek, 120)
			opts.SourceSizeBytes = tc.size
			opts.SourceDurationSeconds = tc.duration
			opts.SourceBitrateKbps = tc.bitrateKbps

			prefixBytes, _, priced := opts.pagePrefixEstimate(0)
			if !priced {
				t.Fatal("the page was not priced")
			}
			if prefixBytes != tc.size {
				t.Fatalf("prefix = %d bytes, want the container's %d", prefixBytes, tc.size)
			}
			// Unclamped, each case prices above the budget and refuses a page
			// the whole of this source can serve.
			if refusal := c.pagePrefixRefusal(opts); refusal != nil {
				t.Fatalf("page refused: %v", refusal)
			}
		})
	}
}

// A page whose discarded container prefix is priced beyond the page budget must
// be refused before the extract, not after it: the production pathology is the
// wall clock of reading that prefix (88 s for a page at 3600 s), and a refusal
// after the read has already paid it.
func TestExpensiveBitmapPageIsRefusedBeforeItsExtract(t *testing.T) {
	c, source := newTestCache(t)
	// 18 GB over two hours is ~2.5 MB/s, so the page at 3600 s would discard
	// about 9 GB of container before reaching its own window.
	opts := progressiveSUPOpts(source, 3600, 120)
	opts.SourceDurationSeconds = 7200
	// A remote identity is warmed by its handler, not by the cache: no fill is
	// running here, so the refusal is the pre-extract price and nothing else.
	opts.DisableBackgroundWarm = true

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page priced beyond its prefix budget")
		return nil
	}
	rec := httptest.NewRecorder()
	err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=3600&duration=120", nil), opts, extract)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("serve error = %v, want the retryable shape", err)
	}
	if retry.Reason != SubtitlePageTooCostlyReason {
		t.Fatalf("reason = %q, want %q", retry.Reason, SubtitlePageTooCostlyReason)
	}
	if retry.RetryAfter <= 0 {
		t.Fatalf("retry after = %s, want a positive delay the client can honor", retry.RetryAfter)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %d bytes, want none: the page was never produced", rec.Body.Len())
	}
	if got := c.inflightCount(); got != 0 {
		t.Fatalf("in-flight fills = %d, want none: the refusal must not start work on its own", got)
	}

	// With the fill already running, the same page advertises how far the track
	// is parsed, so the client can tell "not ready yet" from "unavailable".
	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10), pgsDisplaySetAt(200))
	defer fill.Discard()
	err = c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=3600&duration=120", nil), opts, extract)
	if !errors.As(err, &retry) {
		t.Fatalf("serve with a fill error = %v, want the retryable shape", err)
	}
	if retry.CoveredUntil != 200 {
		t.Fatalf("covered_until = %v, want the artifact's watermark (200)", retry.CoveredUntil)
	}
}

// The prefix budget must not refuse what it exists to spare: an opening page has
// no discarded prefix, so it extracts exactly as before.
func TestOpeningBitmapPageStillExtracts(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 0, 120)
	opts.SourceDurationSeconds = 7200
	opts.DisableBackgroundWarm = true

	extracted := false
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		extracted = true
		_, err := extractOpts.Writer.Write(pgsDisplaySetAt(10))
		return err
	}
	rec := httptest.NewRecorder()
	if err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=0&duration=120", nil), opts, extract); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !extracted {
		t.Fatal("the opening page was refused instead of extracted")
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the opening page produced no bytes")
	}
}

// A prefix under the byte cap can still be too slow: the estimate must use the
// rate this identity has been observed to read at, so a page that would spend
// most of its client fetch discarding the prefix is refused too.
func TestSlowBitmapPageIsRefusedOnTheObservedReadRate(t *testing.T) {
	c, source := newTestCache(t)
	// 1 GB over two hours is ~139 kB/s, so the page at 1200 s discards ~166 MB:
	// under the byte budget, and ~166 s of reading at the observed 1 MiB/s.
	opts := progressiveSUPOpts(source, 1200, 120)
	opts.SourceSizeBytes = 1 << 30
	opts.SourceDurationSeconds = 7200
	opts.DisableBackgroundWarm = true
	c.notePageReadRate(opts.CacheIdentity, 1<<20)

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page priced beyond its time budget")
		return nil
	}
	err := c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1200&duration=120", nil), opts, extract)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) || retry.Reason != SubtitlePageTooCostlyReason {
		t.Fatalf("serve error = %v, want the page-cost retryable shape", err)
	}

	// The same page at a rate the link actually sustains is served: the gate
	// prices the link, not the page's offset.
	c.notePageReadRate(opts.CacheIdentity, 64<<20)
	extracted := false
	extract = func(_ context.Context, extractOpts StreamExtractOpts) error {
		extracted = true
		_, writeErr := extractOpts.Writer.Write(pgsDisplaySetAt(1200))
		return writeErr
	}
	if serveErr := c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1200&duration=120", nil), opts, extract); serveErr != nil {
		t.Fatalf("serve at a fast rate = %v, want the page", serveErr)
	}
	if !extracted {
		t.Fatal("the page was refused although its prefix reads in seconds at the observed rate")
	}
}

// An extraction that times out without emitting a byte is a slow source, not a
// broken extraction: it must answer the retryable 503 shape so the client backs
// off instead of spending its terminal budget on a 500.
func TestTimedOutBitmapPageAnswersRetryableNotFailure(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 600, 120)
	opts.CacheIdentity = "virtual-pgs-timeout"
	opts.DisableBackgroundWarm = true

	extract := func(context.Context, StreamExtractOpts) error {
		return context.DeadlineExceeded
	}
	err := c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=600&duration=120", nil), opts, extract)
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
}

// A text window killed by its own extract deadline must not be a 200 carrying
// half a cue: the client cannot tell that from a complete window and has nothing
// to retry. It answers the retryable 503 shape with nothing on the wire.
func TestTimedOutTextWindowAnswersRetryableWithNoBody(t *testing.T) {
	c, source := newTestCache(t)
	rec := httptest.NewRecorder()
	err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.vtt?position=3600&duration=120", nil), textWindowOpts(source, "virtual-text-timeout", 3600),
		func(_ context.Context, extractOpts StreamExtractOpts) error {
			// Half a cue reaches the writer before the deadline kills ffmpeg.
			if _, writeErr := io.WriteString(extractOpts.Writer, "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nwin"); writeErr != nil {
				return writeErr
			}
			return context.DeadlineExceeded
		})
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("serve error = %v, want the retryable shape", err)
	}
	if retry.RetryAfter <= 0 {
		t.Fatalf("retry after = %s, want a positive delay", retry.RetryAfter)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want none: a truncated slice must not be served as a window", rec.Body.String())
	}
	if rec.Flushed {
		t.Fatal("the response was committed before the extract finished")
	}
}

// A canceled text window is the same class: the request went away, so nothing may
// be committed. The held slice is dropped and the caller sees the cancellation.
func TestCanceledTextWindowServesNoPartialBody(t *testing.T) {
	c, source := newTestCache(t)
	rec := httptest.NewRecorder()
	err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.vtt?position=120&duration=120", nil), textWindowOpts(source, "virtual-text-canceled", 120),
		func(_ context.Context, extractOpts StreamExtractOpts) error {
			if _, writeErr := io.WriteString(extractOpts.Writer, "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nwin"); writeErr != nil {
				return writeErr
			}
			return context.Canceled
		})
	if err == nil {
		t.Fatal("a canceled window extract was served as success")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want none", rec.Body.String())
	}
}

// A successful text window is still served whole through the hold: the gate above
// narrows serving without taking the ordinary path away.
func TestSuccessfulTextWindowIsServedThroughTheHold(t *testing.T) {
	c, source := newTestCache(t)
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.vtt?position=120&duration=120", nil), textWindowOpts(source, "virtual-text-ok", 120),
		func(_ context.Context, extractOpts StreamExtractOpts) error {
			_, writeErr := io.WriteString(extractOpts.Writer, textWindowCue)
			return writeErr
		}); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if rec.Body.String() != textWindowCue {
		t.Fatalf("body = %q, want the whole window", rec.Body.String())
	}
}

// textWindowOpts builds one bounded text window for a remote identity, the shape
// the web player asks for.
func textWindowOpts(source, identity string, seek float64) StreamExtractOpts {
	return StreamExtractOpts{
		InputPath:             source,
		CacheIdentity:         identity,
		SourceCodec:           "subrip",
		TargetFormat:          "vtt",
		WindowRequested:       true,
		SeekSeconds:           seek,
		DurationSeconds:       120,
		DisableBackgroundWarm: true,
	}
}

// A .sup muxer that refuses the mapped stream's codec is a rotation the drift
// probe missed, not a broken extraction: it must reach the same re-probe and
// remap path a missing ordinal does, instead of failing the client's request.
func TestSupMuxerCodecMismatchReadsAsAStreamMapError(t *testing.T) {
	rotated := errors.New("ffmpeg subtitle stream failed: exit status 234 " +
		"(stderr: [sup @ 0x55d] sup muxer supports only codec hdmv_pgs_subtitle for type subtitle\n" +
		"Could not write header for output file #0)")
	if !IsSubtitleStreamMapError(rotated) {
		t.Fatal("a muxer codec mismatch was not recognized as a rotated stream mapping")
	}
	// Genuine ffmpeg failures stay loud.
	if IsSubtitleStreamMapError(errors.New("ffmpeg subtitle stream failed: exit status 1 (stderr: Invalid data found)")) {
		t.Fatal("an unrelated ffmpeg failure was classified as a stream map error")
	}
}

// A row whose provider never declared a size must still page from its artifact
// once the bitrate prices a source read as unaffordable: the refusal starts the
// fill, and the pages that fill publishes are the pages served. Otherwise a
// virtual row with an unknown size could never use the artifact at all.
func TestProgressiveFillServesPagesWhenTheSourceSizeIsUnknown(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 1200, 120)
	opts.CacheIdentity = "virtual-bitrate-only"
	// ~28 Mbps is ~3.5 MB/s, so page 1200 would discard about 4 GB of container.
	opts.SourceBitrateKbps = 28000
	opts.DisableBackgroundWarm = true

	refused := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page priced from its bitrate alone")
		return nil
	}
	err := c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1200&duration=120", nil), opts, refused)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) || retry.Reason != SubtitlePageTooCostlyReason {
		t.Fatalf("serve error = %v, want the page-cost retryable shape", err)
	}

	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10), pgsDisplaySetAt(1300), pgsDisplaySetAt(1400))
	defer fill.Discard()
	served := false
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		served = true
		_, writeErr := extractOpts.Writer.Write(pgsDisplaySetAt(1200))
		return writeErr
	}
	rec := httptest.NewRecorder()
	if serveErr := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1200&duration=120", nil), opts, extract); serveErr != nil {
		t.Fatalf("serve from the artifact: %v", serveErr)
	}
	if served {
		t.Fatal("the source was read for a page the artifact covers")
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the page covered by the artifact produced no bytes")
	}
}

// The observed read rate keeps the newest sample, so a source that got faster is
// no longer refused on its stale rate, and rates never leak across identities.
func TestPageReadRateKeepsTheNewestSample(t *testing.T) {
	c, _ := newTestCache(t)
	if _, ok := c.pageReadRate("identity"); ok {
		t.Fatal("a rate was reported before any was observed")
	}
	c.notePageReadRate("identity", 1<<20)
	if rate, ok := c.pageReadRate("identity"); !ok || rate != 1<<20 {
		t.Fatalf("rate = %v (ok=%v), want 1 MiB/s", rate, ok)
	}
	c.notePageReadRate("identity", 40<<20)
	if rate, _ := c.pageReadRate("identity"); rate != 40<<20 {
		t.Fatalf("rate = %v, want the newest sample", rate)
	}
	if _, ok := c.pageReadRate("other"); ok {
		t.Fatal("a rate leaked across identities")
	}
}
