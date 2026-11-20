package playback

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A page's window can never hold more than the whole container. A window that
// reaches past the end of a known-size source, or a bitrate that disagrees with
// the declared size, must not price the page above the source and refuse a page
// it can serve.
func TestBitmapPageWindowIsClampedToTheKnownSourceSize(t *testing.T) {
	cases := []struct {
		name        string
		size        int64
		duration    float64
		bitrateKbps int
		seek        float64
		window      float64
	}{
		{
			// 3000 s + 4000 s runs past a 3600 s source: the span prices
			// 4000 s x 58 kB/s = 233 MB, more container than exists.
			name:     "window past the end of the source",
			size:     200 << 20,
			duration: 3600,
			seek:     3000,
			window:   4000,
		},
		{
			// 28 Mbit/s over a 120 s window is 420 MB of a container declared
			// to be 8 MB: the size is the evidence that the bitrate is wrong.
			name:        "bitrate that disagrees with the declared size",
			size:        8 << 20,
			bitrateKbps: 28000,
			seek:        1200,
			window:      120,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, source := newTestCache(t)
			opts := progressiveSUPOpts(source, tc.seek, tc.window)
			opts.SourceSizeBytes = tc.size
			opts.SourceDurationSeconds = tc.duration
			opts.SourceBitrateKbps = tc.bitrateKbps

			windowBytes, _, priced := opts.pageWindowEstimate(0)
			if !priced {
				t.Fatal("the page was not priced")
			}
			if windowBytes != tc.size {
				t.Fatalf("window = %d bytes, want the container's %d", windowBytes, tc.size)
			}
			if refusal := c.pageWindowRefusal(opts); refusal != nil {
				t.Fatalf("page refused: %v", refusal)
			}
		})
	}
}

// A fast link is not evidence about how many bytes a window holds. With the
// container's byte rate unknown the page is unpriced and keeps its extract under
// the unpriced read cap; pricing it from the link speed would price a 120 s
// window at gigabytes and refuse it on the fastest link.
func TestUnknownContainerRateIsNotPricedFromTheLinkSpeed(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 600, 120)
	opts.CacheIdentity = "virtual-unpriced"
	opts.DisableBackgroundWarm = true
	c.notePageReadRate(opts.CacheIdentity, 200<<20)

	if _, _, priced := opts.pageWindowEstimate(200 << 20); priced {
		t.Fatal("a window was priced from the link's read rate")
	}
	if c.pageWindowBeyondBudget(opts) {
		t.Fatal("an unpriced window was refused")
	}
	if got := opts.pageReadBudget(); got != subtitlePageReadBudgetBytes {
		t.Fatalf("unpriced page budget = %d, want %d", got, subtitlePageReadBudgetBytes)
	}
}

// A bounded page is priced by the window it reads, not by the container prefix
// in front of it. The extract carries an input seek to the page, and against a
// source that honors Range that seek reads the window: a 120 s page at 3000 s
// of a 102 MB cued MKV read 13.0 MB in total (5.9 MB probe+header, 3.4 MB first
// cluster, 3.6 kB of cues, 5.8 MB at the seek offset) where the prefix scan the
// old gate priced read 93.9 MB. Pricing the prefix refused every deep page of a
// large source for a read it was never going to make.
func TestDeepBitmapPagePricesItsWindowNotItsPrefix(t *testing.T) {
	c, source := newTestCache(t)
	// 18 GB over two hours is ~2.5 MB/s. The page at 3600 s would discard about
	// 9 GB of container if it could not seek; its own window is 300 MB.
	opts := progressiveSUPOpts(source, 3600, 120)
	opts.SourceSizeBytes = 18 << 30
	opts.SourceDurationSeconds = 7200
	opts.DisableBackgroundWarm = true

	if refusal := c.pageWindowRefusal(opts); refusal != nil {
		t.Fatalf("deep page refused for a prefix it seeks past: %v", refusal)
	}

	// The command the serve builds seeks to the window and keeps its cues on the
	// source timeline, which is what makes the window read the page and not the
	// prefix.
	args := streamExtractArgs(opts)
	if !containsArgPair(args, "-ss", "3600.000") {
		t.Fatalf("ffmpeg args %v carry no input seek to the page", args)
	}
	if !containsArg(args, "-copyts") {
		t.Fatalf("ffmpeg args %v do not keep the window on the source timeline", args)
	}

	extracted := false
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		extracted = true
		if extractOpts.SeekSeconds != 3600 || !extractOpts.AllowWindow {
			t.Fatalf("extract seek=%v allow_window=%v, want the requested window", extractOpts.SeekSeconds, extractOpts.AllowWindow)
		}
		_, err := extractOpts.Writer.Write(pgsDisplaySetAt(3600))
		return err
	}
	rec := httptest.NewRecorder()
	if err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=3600&duration=120", nil), opts, extract); err != nil {
		t.Fatalf("serve deep page: %v", err)
	}
	if !extracted {
		t.Fatal("the deep page was refused instead of extracted")
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the deep page produced no bytes")
	}
}

// The refusal that remains is a page whose own window cannot be read inside one
// page extract at the rate this identity reads at. That is a real cost, not an
// assumption about a prefix.
func TestBitmapPageWhoseWindowExceedsTheReadRateIsRefused(t *testing.T) {
	c, source := newTestCache(t)
	// 20 Mbit/s container: a 120 s window is 300 MB. The link has been measured
	// at 2 MB/s, which is 240 MB per page extract deadline: this window does not
	// fit.
	opts := progressiveSUPOpts(source, 1800, 120)
	opts.SourceBitrateKbps = 20000
	opts.DisableBackgroundWarm = true
	c.notePageReadRate(opts.CacheIdentity, 2<<20)

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a window that cannot fit its extract deadline")
		return nil
	}
	err := c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1800&duration=120", nil), opts, extract)
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

	// With the fill already running, the same page advertises how far the track
	// is parsed, so the client can tell "not ready yet" from "unavailable".
	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10), pgsDisplaySetAt(200))
	defer fill.Discard()
	err = c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1800&duration=120", nil), opts, extract)
	if !errors.As(err, &retry) {
		t.Fatalf("serve with a fill error = %v, want the retryable shape", err)
	}
	if retry.CoveredUntil != 200 {
		t.Fatalf("covered_until = %v, want the artifact's watermark (200)", retry.CoveredUntil)
	}
}

// A window that reaches over many pages is the same class of real cost: it is
// one extract's read, and it is priced as such.
func TestMultiPageBitmapWindowBeyondTheCeilingIsRefused(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 1200, 900)
	opts.SourceBitrateKbps = 20000
	opts.DisableBackgroundWarm = true

	if !c.pageWindowBeyondBudget(opts) {
		t.Fatal("a 900 s window of a 20 Mbit/s source was priced as affordable")
	}
	err := c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1200&duration=900", nil), opts,
		func(context.Context, StreamExtractOpts) error {
			t.Error("the source was read for a nine-hundred second window")
			return nil
		})
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) || retry.Reason != SubtitlePageTooCostlyReason {
		t.Fatalf("serve error = %v, want the page-cost retryable shape", err)
	}
}

// The observed read rate prices the page's own window, so the same page is
// served when the link can read the window it actually asks for.
func TestBitmapPageIsServedWhenTheLinkCanReadItsWindow(t *testing.T) {
	c, source := newTestCache(t)
	// 1 GB over two hours is ~139 kB/s, so a 120 s window is ~17 MB.
	opts := progressiveSUPOpts(source, 1200, 120)
	opts.SourceSizeBytes = 1 << 30
	opts.SourceDurationSeconds = 7200
	opts.DisableBackgroundWarm = true
	c.notePageReadRate(opts.CacheIdentity, 1<<20)

	extracted := false
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		extracted = true
		_, writeErr := extractOpts.Writer.Write(pgsDisplaySetAt(1200))
		return writeErr
	}
	if serveErr := c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1200&duration=120", nil), opts, extract); serveErr != nil {
		t.Fatalf("serve = %v, want the page", serveErr)
	}
	if !extracted {
		t.Fatal("the page was refused although its window reads in seconds at the observed rate")
	}
}

// A page refused for its read cost arms the identity's extraction cooldown, and
// a retry that lands inside that cooldown must keep saying what the page is
// waiting for. The generic subtitle_source_unavailable answer dropped the reason,
// so the handler (which starts the recovery fill only for a page refusal) never
// kicked one and the retry could only repeat the read that earned the refusal.
func TestBitmapPageCooldownRefusalKeepsItsReason(t *testing.T) {
	c, source := newTestCache(t)
	// An unknown-size source: nothing prices the page, so neither the prefix
	// gate nor the progressive page path answers it and the cooldown refusal is
	// what the retry meets.
	opts := windowedSupOpts(source, 0, 600, 120)
	opts.CacheIdentity = "virtual-pgs-cooldown"
	opts.DisableBackgroundWarm = true
	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page inside its extraction cooldown")
		return nil
	}
	serve := func() *SubtitleExtractionRetryError {
		t.Helper()
		err := c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=600&duration=120", nil), opts, extract)
		var retry *SubtitleExtractionRetryError
		if !errors.As(err, &retry) {
			t.Fatalf("serve error = %v, want the retryable shape", err)
		}
		return retry
	}
	// The refusal a runtime read-cap kill or a page timeout produces.
	c.noteExtractionFailure(opts, subtitleFormatSUP)

	retry := serve()
	if retry.Reason != SubtitlePageTooCostlyReason {
		t.Fatalf("reason = %q, want %q with no fill running", retry.Reason, SubtitlePageTooCostlyReason)
	}
	if retry.RetryAfter < subtitlePageCostlyRetryAfter {
		t.Fatalf("retry after = %s, want at least %s", retry.RetryAfter, subtitlePageCostlyRetryAfter)
	}

	// Once the recovery fill is running, the cooldown's own delay is the honest
	// answer: the page path answered from the artifact already if it could, and
	// this page cannot use the artifact yet.
	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10), pgsDisplaySetAt(200))
	defer fill.Discard()
	retry = serve()
	if retry.Reason != "" {
		t.Fatalf("reason = %q, want the generic cooldown answer while a fill runs", retry.Reason)
	}
}

// The cooldown a page refusal arms must not block the fill it owns: a page the
// recovering artifact's watermark has passed is served from it with no source
// read, which is exactly what the retry the 503 asks for receives.
func TestBitmapPageRefusalCooldownServesItsOwnFill(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 120, 120)
	opts.SourceDurationSeconds = 7200
	opts.DisableBackgroundWarm = true
	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page the artifact has parsed")
		return nil
	}
	// The refusal (runtime read cap or page timeout) armed the cooldown.
	c.noteExtractionFailure(opts, subtitleFormatSUP)
	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10), pgsDisplaySetAt(200))
	defer fill.Discard()

	rec := httptest.NewRecorder()
	if err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=120&duration=120", nil), opts, extract); err != nil {
		t.Fatalf("page inside the watermark error = %v, want it served from the fill", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("page inside the watermark = %d, want 200 from the fill", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the page produced no bytes")
	}
}

// An opening page has no prefix, so its window prices exactly as it extracts:
// it is never refused.
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
	// ~28 Mbps is ~3.5 MB/s, so the 120 s window is 420 MB. The identity's reads
	// have been measured at 3 MB/s, which is 360 MB per page extract deadline:
	// the window does not fit, and the page is priced from the link rather than
	// from a prefix.
	opts.SourceBitrateKbps = 28000
	opts.DisableBackgroundWarm = true
	c.notePageReadRate(opts.CacheIdentity, 3<<20)

	refused := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a window that cannot fit its extract deadline")
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

// containsArg reports whether args carries flag as its own token.
func containsArg(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

// containsArgPair reports whether args carries flag immediately followed by value.
func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
