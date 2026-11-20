package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// pgsSegmentAt builds one PGS segment with a presentation timestamp, so a test
// stream can put display sets on the playback timeline.
func pgsSegmentAt(kind byte, pts float64, payload []byte) []byte {
	seg := make([]byte, supSegmentHeaderLen+len(payload))
	seg[0], seg[1] = 'P', 'G'
	binary.BigEndian.PutUint32(seg[2:6], uint32(pts*90000))
	binary.BigEndian.PutUint32(seg[6:10], uint32(pts*90000))
	seg[10] = kind
	binary.BigEndian.PutUint16(seg[11:13], uint16(len(payload)))
	copy(seg[supSegmentHeaderLen:], payload)
	return seg
}

// pgsDisplaySetAt builds one complete display set: a PCS, an object segment and
// the END that closes it.
func pgsDisplaySetAt(pts float64) []byte {
	var out []byte
	out = append(out, pgsSegmentAt(supSegmentPCS, pts, []byte{0, 1, 2, 3})...)
	out = append(out, pgsSegmentAt(0x15, pts, []byte{0, 0, 0, 0})...)
	out = append(out, pgsSegmentAt(supSegmentEND, pts, nil)...)
	return out
}

// supSets joins artifact parts into one payload, in order.
func supSets(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

// progressiveSUPOpts builds a bounded bitmap page for a remote identity whose
// container is large enough that a page read is not worth attempting.
func progressiveSUPOpts(source string, seek, duration float64) StreamExtractOpts {
	opts := windowedSupOpts(source, 0, seek, duration)
	opts.CacheIdentity = "virtual-pgs-identity"
	opts.SourceSizeBytes = 18 << 30
	return opts
}

// startProgressiveFill reserves the track's full-track fill and writes parts
// into its artifact, as a running unbounded extract does.
func startProgressiveFill(t *testing.T, c *SubtitleCache, opts StreamExtractOpts, parts ...[]byte) *SubtitleCacheFill {
	t.Helper()
	fill := c.beginFill(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if fill == nil {
		t.Fatal("the fill reservation was refused")
	}
	tee := fill.Tee(io.Discard)
	for _, part := range parts {
		if _, err := tee.Write(part); err != nil {
			t.Fatalf("artifact write: %v", err)
		}
	}
	return fill
}

// The index drops its oldest sets at its cap. A window whose on-screen set was
// dropped must be refused, not served without it.
func TestSupCoverageRefusesAWindowItsCompactionDropped(t *testing.T) {
	coverage := &supCoverage{}
	for i := 0; i < supProgressiveMaxSets+1; i++ {
		at := float64(i) * 10
		coverage.appendSet(supDisplaySet{
			off:  int64(i) * 47,
			end:  int64(i+1) * 47,
			from: at,
			to:   at,
		})
	}
	if !coverage.compacted {
		t.Fatal("the index never compacted")
	}
	firstRetained := coverage.sets[0].from
	if _, _, ok := coverage.window(0, firstRetained); ok {
		t.Fatal("a window before the retained sets was answered")
	}
	start := firstRetained + 20
	if _, _, ok := coverage.window(start, start+20); !ok {
		t.Fatalf("a window inside the retained sets (%v-%v) was refused", start, start+20)
	}
}

// The watermark must advance only at a complete display set: a page may never
// be served from a run that is still arriving.
func TestSupCoverageWatermarkAdvancesOnlyAtCompleteDisplaySets(t *testing.T) {
	coverage := &supCoverage{}
	first := pgsDisplaySetAt(10)
	partial := append(pgsSegmentAt(supSegmentPCS, 200, []byte{0, 1, 2, 3}),
		pgsSegmentAt(0x15, 200, []byte{0, 0, 0, 0})...)

	// Byte at a time, the watermark must stay at zero until the first END.
	for i := range first {
		coverage.feed(first[i : i+1])
		want := 0.0
		if i == len(first)-1 {
			want = 10
		}
		if got, ok := coverage.covered(); !ok || got != want {
			t.Fatalf("byte %d/%d: coverage = %v (ok=%v), want %v", i+1, len(first), got, ok, want)
		}
	}
	// A set that is still arriving contributes nothing.
	for i := range partial {
		coverage.feed(partial[i : i+1])
	}
	if got, _ := coverage.covered(); got != 10 {
		t.Fatalf("coverage after a partial set = %v, want 10", got)
	}
	// The END that closes it advances the watermark.
	coverage.feed(pgsSegmentAt(supSegmentEND, 200, nil))
	if got, _ := coverage.covered(); got != 200 {
		t.Fatalf("coverage after the closing END = %v, want 200", got)
	}
}

// A window's slice must include the display set that is still on screen at the
// window's start, and stop at the last set that starts before its end.
func TestSupCoverageWindowIncludesTheSetOnScreenAtStart(t *testing.T) {
	coverage := &supCoverage{}
	first, second := pgsDisplaySetAt(10), pgsDisplaySetAt(200)
	coverage.feed(append(append([]byte{}, first...), second...))

	off, end, ok := coverage.window(120, 200)
	if !ok {
		t.Fatal("a window inside coverage was refused")
	}
	// The set at 10 s is still on screen at 120 s, so the slice must start where
	// the artifact starts (byte 0) rather than at the next set (byte 47).
	if off != 0 {
		t.Fatalf("slice starts at %d, want 0: the set on screen at the window start must be included", off)
	}
	if want := int64(len(first) + len(second)); end != want {
		t.Fatalf("slice ends at %d, want %d", end, want)
	}

	// Past the watermark there is no answer, so nothing can be served.
	if _, _, ok := coverage.window(0, 300); ok {
		t.Fatal("a window past the watermark was answered")
	}

	// A covered window before the first display set is an empty slice, not a
	// refusal; a window that overlaps a set on screen includes that set.
	off, end, ok = coverage.window(0, 5)
	if !ok || off != end {
		t.Fatalf("empty covered window = (%d, %d, ok=%v), want an empty served range", off, end, ok)
	}
	if off, end, ok = coverage.window(50, 100); !ok || off != 0 || end != int64(len(first)) {
		t.Fatalf("window over the on-screen set = (%d, %d, ok=%v), want the set at 10 s", off, end, ok)
	}
}

// A page the artifact already covers is a local read: the source must not be
// touched at all.
func TestProgressiveSUPPageBehindWatermarkReadsNoSource(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 120, 120)
	first, second := pgsDisplaySetAt(10), pgsDisplaySetAt(130)
	fill := startProgressiveFill(t, c, opts, first, second, pgsDisplaySetAt(250))
	defer fill.Discard()

	relayReads := 0
	extract := func(context.Context, StreamExtractOpts) error {
		relayReads++
		return errors.New("the progressive artifact was not used")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=120&duration=120", nil)
	if err := c.ServeSUPExtract(rec, req, opts, extract); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if relayReads != 0 {
		t.Fatalf("the source was read %d times for a page the artifact covers", relayReads)
	}
	want := supSets(first, second)
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("page = %d bytes, want the artifact's display sets (%d bytes)", rec.Body.Len(), len(want))
	}
	if got := rec.Result().Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("content type = %q", got)
	}
}

// A page whose start the artifact has already parsed is servable the moment its
// covered prefix reaches a complete display set, even while the page's own end is
// still ahead of the watermark. Refusing the whole page there is what left every
// request at a playhead inside the covered range unanswered in production: the
// client asked for 10-130 s, the artifact had parsed 0-120 s, and the answer was
// a 503 for a page whose first 110 s were on disk.
func TestProgressiveSUPPageInsideWatermarkServesItsCoveredPrefix(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 10, 120)
	first, second, third := pgsDisplaySetAt(10), pgsDisplaySetAt(60), pgsDisplaySetAt(120)
	// The run after the last complete set is still arriving. Its bytes must stay
	// out of the page: the watermark is the last END that was read in full.
	pending := pgsSegmentAt(supSegmentPCS, 126.5, []byte{0, 1, 2, 3})
	fill := startProgressiveFill(t, c, opts, supSets(first, second, third, pending))
	defer fill.Discard()

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page the artifact has parsed")
		return nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=10&duration=120", nil)
	if err := c.ServeSUPExtract(rec, req, opts, extract); err != nil {
		var retry *SubtitleExtractionRetryError
		if errors.As(err, &retry) {
			t.Fatalf("a page the artifact has parsed to %v was refused (covered_until=%v)", 126.5, retry.CoveredUntil)
		}
		t.Fatalf("serve: %v", err)
	}

	want := supSets(first, second, third)
	if !supHasDisplaySet(want) {
		t.Fatal("the test's own artifact holds no display set")
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("page = %d bytes, want the parsed display sets (%d bytes): the page must not include the run ahead of the watermark", rec.Body.Len(), len(want))
	}
	if got := rec.Result().Header.Get(SubtitleCoverageHeader); got != "10.000-120.000" {
		t.Fatalf("coverage = %q, want the range the page really covers (10.000-120.000)", got)
	}
	if got := rec.Result().Header.Get(SubtitleWindowedHeader); got != "true" {
		t.Fatalf("windowed = %q, want true", got)
	}
}

// A page that ends exactly at the watermark is fully parsed: no byte of it is
// ahead of what the artifact read, so it must serve whole.
func TestProgressiveSUPPageAtTheWatermarkIsServedWhole(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 0, 120)
	first, second := pgsDisplaySetAt(10), pgsDisplaySetAt(120)
	fill := startProgressiveFill(t, c, opts, first, second)
	defer fill.Discard()

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page the artifact has parsed")
		return nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=0&duration=120", nil)
	if err := c.ServeSUPExtract(rec, req, opts, extract); err != nil {
		t.Fatalf("a page ending at the watermark was refused: %v", err)
	}
	want := supSets(first, second)
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("page = %d bytes, want the artifact's display sets (%d bytes)", rec.Body.Len(), len(want))
	}
	if got := rec.Result().Header.Get(SubtitleCoverageHeader); got != "0.000-120.000" {
		t.Fatalf("coverage = %q, want 0.000-120.000", got)
	}
}

// A page just behind the watermark is fully parsed too: the display sets that
// show in it are all complete, so the tail of the artifact past the window is
// not needed and the page serves whole.
func TestProgressiveSUPPageJustBehindTheWatermarkIsServedWhole(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 0, 119)
	first, second := pgsDisplaySetAt(10), pgsDisplaySetAt(60)
	fill := startProgressiveFill(t, c, opts, first, second, pgsDisplaySetAt(120))
	defer fill.Discard()

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page the artifact has parsed")
		return nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=0&duration=119", nil)
	if err := c.ServeSUPExtract(rec, req, opts, extract); err != nil {
		t.Fatalf("a page just behind the watermark was refused: %v", err)
	}
	// The set at 120 s shows after this window, so it is not part of the page.
	want := supSets(first, second)
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("page = %d bytes, want the display sets inside the window (%d bytes)", rec.Body.Len(), len(want))
	}
	if got := rec.Result().Header.Get(SubtitleCoverageHeader); got != "0.000-119.000" {
		t.Fatalf("coverage = %q, want the window the page really covers (0.000-119.000)", got)
	}
}

// A page served in part must not be remembered as the whole page: once the fill
// parses further, the same request is answered with the fuller range. Otherwise
// the unparsed tail would be treated as delivered for as long as the window
// cache held the short copy.
func TestProgressiveSUPPageGrowsAsTheWatermarkAdvances(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 10, 120)
	first, second, third := pgsDisplaySetAt(10), pgsDisplaySetAt(60), pgsDisplaySetAt(130)
	fill := startProgressiveFill(t, c, opts, first, second)
	defer fill.Discard()

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page the artifact has parsed")
		return nil
	}
	req := func() *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		if err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=10&duration=120", nil), opts, extract); err != nil {
			t.Fatalf("serve: %v", err)
		}
		return rec
	}

	short := req()
	wantShort := supSets(first, second)
	if !bytes.Equal(short.Body.Bytes(), wantShort) {
		t.Fatalf("first page = %d bytes, want the parsed prefix (%d bytes)", short.Body.Len(), len(wantShort))
	}
	if got := short.Result().Header.Get(SubtitleCoverageHeader); got != "10.000-60.000" {
		t.Fatalf("first coverage = %q, want 10.000-60.000", got)
	}

	// The fill parses the set at 130 s: the page's 10-130 s range is complete now.
	if _, err := fill.Tee(io.Discard).Write(third); err != nil {
		t.Fatalf("artifact write: %v", err)
	}
	grown := req()
	wantGrown := supSets(wantShort, third)
	if !bytes.Equal(grown.Body.Bytes(), wantGrown) {
		t.Fatalf("second page = %d bytes, want the whole page (%d bytes)", grown.Body.Len(), len(wantGrown))
	}
	if got := grown.Result().Header.Get(SubtitleCoverageHeader); got != "10.000-130.000" {
		t.Fatalf("second coverage = %q, want 10.000-130.000", got)
	}
}

// A page served from the progressive artifact must carry both of its headers on
// the response an intermediary sees: they have to be set before the copy, since
// the first Write commits the status and headers. A window slice is
// position-dependent, so it must not be cached.
func TestProgressiveSUPPageCommittedHeadersSurviveTheCopy(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 120, 120)
	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10), pgsDisplaySetAt(130), pgsDisplaySetAt(250))
	defer fill.Discard()

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page the artifact covers")
		return nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=120&duration=120", nil)
	if err := c.ServeSUPExtract(rec, req, opts, extract); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the served page carried no bytes")
	}
	// Result() reports the headers as committed at the first Write. Header()
	// reads the live map, so a Set after the copy would still show there and
	// hide the bug.
	committed := rec.Result().Header
	if got := committed.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("committed content type = %q, want application/octet-stream", got)
	}
	if got := committed.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("committed cache-control = %q, want no-store: a window slice must not be cached", got)
	}
}

// A page ahead of the watermark must answer retryable, describing what is
// covered, and must not read the source for it.
func TestProgressiveSUPPageAheadOfWatermarkIsRetryable(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 240, 120)
	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10), pgsDisplaySetAt(200))
	defer fill.Discard()

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read for a page ahead of the watermark")
		return nil
	}
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=240&duration=120", nil)
		err := c.ServeSUPExtract(rec, req, opts, extract)
		var retry *SubtitleExtractionRetryError
		if !errors.As(err, &retry) {
			t.Fatalf("serve %d error = %v, want the retryable shape", attempt, err)
		}
		if retry.CoveredUntil != 200 {
			t.Fatalf("serve %d covered_until = %v, want 200", attempt, retry.CoveredUntil)
		}
		// The source is fine and the artifact is filling: the page is not ready,
		// and the answer must say that rather than blame the source.
		if retry.Reason != SubtitlePageNotReadyReason {
			t.Fatalf("serve %d reason = %q, want %q", attempt, retry.Reason, SubtitlePageNotReadyReason)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("serve %d body = %d bytes, want none", attempt, rec.Body.Len())
		}
	}
	if got := c.inflightCount(); got != 1 {
		t.Fatalf("in-flight fills = %d, want the one already running", got)
	}
}

// The first miss kicks exactly one fill, however many requests arrive while it
// runs.
func TestProgressiveSUPPageMissKicksOneFill(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 600, 120)
	release := make(chan struct{})
	var calls atomic.Int32
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		calls.Add(1)
		<-release
		_, err := extractOpts.Writer.Write(pgsDisplaySetAt(10))
		return err
	}
	for attempt := 1; attempt <= 3; attempt++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=600&duration=120", nil)
		err := c.ServeSUPExtract(rec, req, opts, extract)
		var retry *SubtitleExtractionRetryError
		if !errors.As(err, &retry) {
			t.Fatalf("serve %d error = %v, want the retryable shape while the fill runs", attempt, err)
		}
	}
	waitForInflightFill(t, c, 1)
	close(release)
	waitForInflightFill(t, c, 0)
	if got := calls.Load(); got != 1 {
		t.Fatalf("fill extractions = %d, want 1: the miss must kick one fill, not one per request", got)
	}
}

// A window past the watermark must never be answered with the truncated run the
// artifact happens to hold: the run would draw nothing.
func TestProgressiveSUPPageNeverServesATruncatedRun(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 120, 240)
	truncated := append(pgsSegmentAt(supSegmentPCS, 250, []byte{0, 1, 2, 3}),
		pgsSegmentAt(0x15, 250, []byte{0, 0, 0, 0})...)
	fill := startProgressiveFill(t, c, opts, pgsDisplaySetAt(10), truncated)
	defer fill.Discard()

	extract := func(context.Context, StreamExtractOpts) error {
		t.Error("the source was read past the watermark")
		return nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=120&duration=240", nil)
	err := c.ServeSUPExtract(rec, req, opts, extract)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("error = %v, want the retryable shape", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %d bytes, want none: a truncated run must never be served", rec.Body.Len())
	}
	if retry.CoveredUntil != 10 {
		t.Fatalf("covered_until = %v, want 10", retry.CoveredUntil)
	}

	// A window the artifact really does cover is still served, so the rule above
	// narrows serving without disabling it.
	coveredOpts := opts
	coveredOpts.SeekSeconds = 0
	coveredOpts.DurationSeconds = 5
	covered := httptest.NewRecorder()
	err = c.ServeSUPExtract(covered, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=0&duration=5", nil), coveredOpts, extract)
	if err != nil {
		t.Fatalf("covered window: %v", err)
	}
	if covered.Body.Len() != 0 {
		t.Fatalf("covered empty window body = %d bytes, want none", covered.Body.Len())
	}
}

// End of file flips the artifact into the committed whole-track path, which is
// what later pages and whole-track fetches read.
func TestProgressiveSUPEOFFlipsToWholeTrack(t *testing.T) {
	c, source := newTestCache(t)
	opts := progressiveSUPOpts(source, 600, 120)
	artifact := append(pgsDisplaySetAt(10), pgsDisplaySetAt(700)...)
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		if extractOpts.SeekSeconds != 0 || extractOpts.DurationSeconds != 0 {
			return errors.New("the full-track fill must be unbounded")
		}
		_, err := extractOpts.Writer.Write(artifact)
		return err
	}

	first := httptest.NewRecorder()
	err := c.ServeSUPExtract(first, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=600&duration=120", nil), opts, extract)
	if err == nil {
		t.Fatal("the first miss was served instead of kicked")
	}
	waitForIdentityEntry(t, c, opts)

	// The track is a committed artifact now: a page is cut from the artifact,
	// not from the source.
	var fromArtifact bool
	cut := func(_ context.Context, extractOpts StreamExtractOpts) error {
		if extractOpts.InputIsExtractedSup && extractOpts.InputPath != source {
			fromArtifact = true
		}
		return nil
	}
	second := httptest.NewRecorder()
	if err := c.ServeSUPExtract(second, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=600&duration=120", nil), opts, cut); err != nil {
		t.Fatalf("serve after EOF: %v", err)
	}
	if !fromArtifact {
		t.Fatal("the page after EOF did not read the committed artifact")
	}
}

// Text windows keep their path: the bitmap machinery must not claim them.
func TestTextWindowKeepsItsSourceExtract(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{
		InputPath:       source,
		CacheIdentity:   "virtual-text-identity",
		SourceSizeBytes: 18 << 30,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		TargetFormat:    "vtt",
		WindowRequested: true,
		SeekSeconds:     120,
		DurationSeconds: 120,
		// The window serve is what this test checks; a detached full-track warm
		// would extract a second time on its own goroutine.
		DisableBackgroundWarm: true,
	}
	if progressiveSUPEligible(opts) {
		t.Fatal("a text window is eligible for the bitmap progressive path")
	}
	var inputPath string
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		inputPath = extractOpts.InputPath
		_, err := io.WriteString(extractOpts.Writer, textWindowCue)
		return err
	}
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.vtt?position=120&duration=120", nil), opts, extract); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if inputPath != source {
		t.Fatalf("text window read %q, want the source", inputPath)
	}
	if rec.Body.String() != textWindowCue {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// inflightCount reports how many fills the cache holds.
func (c *SubtitleCache) inflightCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inflight)
}

// waitForInflightFill waits for the cache to hold n fills.
func waitForInflightFill(t *testing.T, c *SubtitleCache, n int) {
	t.Helper()
	pollUntil(t, 2*time.Second, func() bool {
		got := c.inflightCount()
		if n == 0 {
			return got == 0
		}
		return got >= n
	}, "in-flight fills never reached the expected state")
}

// waitForIdentityEntry waits for the committed artifact of a virtual identity's
// track.
func waitForIdentityEntry(t *testing.T, c *SubtitleCache, opts StreamExtractOpts) {
	t.Helper()
	pollUntil(t, 5*time.Second, func() bool {
		_, _, ok := c.cachedEntryPath(opts.InputPath, opts.CacheIdentity, opts.TrackIndex)
		return ok
	}, "the committed artifact never appeared")
}

// pollUntil waits on observable state (the cache's own bookkeeping) rather than
// on a fixed delay.
func pollUntil(t *testing.T, timeout time.Duration, done func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if done() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
	}
}
