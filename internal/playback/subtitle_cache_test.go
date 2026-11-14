package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestServeExtractTextCacheVariants(t *testing.T) {
	c, source := newTestCache(t)
	for _, target := range []string{"", "vtt", ""} {
		opts := StreamExtractOpts{InputPath: source, SourceCodec: "ass", TargetFormat: target}
		_, format := streamExtractOutput(opts.SourceCodec, opts.TargetFormat)
		payload := "complete " + format + " track"
		calls := 0
		extract := func(_ context.Context, opts StreamExtractOpts) error {
			calls++
			_, err := io.WriteString(opts.Writer, payload)
			return err
		}
		for range 2 {
			rec := httptest.NewRecorder()
			if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, extract); err != nil {
				t.Fatal(err)
			}
			if rec.Body.String() != payload {
				t.Fatalf("format %q body = %q, want %q", target, rec.Body.String(), payload)
			}
		}
		if calls > 1 {
			t.Fatalf("format %q extracted %d times, want at most once", target, calls)
		}
	}
	// Both current variants must survive another variant's commit.
	for _, format := range []string{"ass", "vtt"} {
		f, _, ok := c.lookup(source, "", 0, format)
		if !ok {
			t.Fatalf("missing %s variant", format)
		}
		_ = f.Close()
	}
}

// The cache identity carries the row's probed subtitle layout, so an artifact
// committed against one inventory is not served under another. An unchanged
// layout keeps the same identity (the cache survives ordinary re-probing);
// a changed one re-keys it.
func TestVirtualSubtitleCacheIdentityTracksProbedInventory(t *testing.T) {
	const uri = "virtual://movie/tt0113277?result=candidate"
	first := []models.SubtitleTrack{
		{Index: 0, Language: "eng", Codec: "subrip"},
		{Index: 1, Language: "fra", Codec: "ass"},
	}
	sameLayoutOtherTitles := []models.SubtitleTrack{
		{Index: 0, Language: "ENG", Codec: "SubRip", Title: "English SDH"},
		{Index: 1, Language: "fra", Codec: "ass", ContainerTrackID: "9"},
	}
	changedLayout := []models.SubtitleTrack{
		{Index: 0, Language: "fra", Codec: "ass"},
		{Index: 1, Language: "eng", Codec: "subrip"},
	}

	base := VirtualSubtitleCacheIdentity(42, uri, 0, SubtitleInventoryToken(first))
	if got := VirtualSubtitleCacheIdentity(42, uri, 0, SubtitleInventoryToken(sameLayoutOtherTitles)); got != base {
		t.Fatalf("identity changed for cosmetic metadata: %s vs %s", got, base)
	}
	if got := VirtualSubtitleCacheIdentity(42, uri, 0, SubtitleInventoryToken(changedLayout)); got == base {
		t.Fatal("a changed probed inventory kept the cache identity")
	}
	if got := VirtualSubtitleCacheIdentity(42, uri, 1, SubtitleInventoryToken(first)); got == base {
		t.Fatal("a different track ordinal kept the cache identity")
	}
	if got := VirtualSubtitleCacheIdentity(43, uri, 0, SubtitleInventoryToken(first)); got == base {
		t.Fatal("a different file id kept the cache identity")
	}
}

// A windowed ASS request must take the windowed branch and stream its own
// slice rather than waiting on or starting a whole-track fill: a full-track
// ASS demux over a virtual relay takes ~100s, which is the stall this path
// removes. Mirrors the windowed VTT tests.
func TestServeExtractWindowedASSStreamsWithoutFullTrackFill(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{
		InputPath:             source,
		SourceCodec:           "ass",
		TrackIndex:            0,
		SeekSeconds:           120,
		DurationSeconds:       600,
		DisableBackgroundWarm: true,
	}
	var got StreamExtractOpts
	calls := 0
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle.ass?position=120&duration=600", nil), opts, func(_ context.Context, o StreamExtractOpts) error {
		calls++
		got = o
		_, err := io.WriteString(o.Writer, "[Script Info]\n")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1 (window only)", calls)
	}
	if got.SeekSeconds != 120 || got.DurationSeconds != 600 {
		t.Fatalf("window parameters not preserved: %+v", got)
	}
	if rec.Body.String() != "[Script Info]\n" {
		t.Fatalf("windowed body = %q", rec.Body.String())
	}
	if f, _, ok := c.lookup(source, "", 0, subtitleFormatASS); ok {
		_ = f.Close()
		t.Fatal("windowed ASS extract must not commit a full-track entry")
	}
}

// A warmed virtual subtitle artifact must survive well past the old
// ten-minute generation bucket. The key already pins the provider result id,
// so a rotated release produces a different identity; the bucket is only
// residual insurance against the same pinned id serving changed bytes. A
// ten-minute bucket threw away a ~100s ASS warm between plays.
func TestVirtualSubtitleCacheArtifactSurvivesPastTenMinuteBucket(t *testing.T) {
	c, source := newTestCache(t)
	const identity = "virtual-result-stable-id"
	fill := c.beginFill(source, identity, 0, subtitleFormatASS)
	if fill == nil {
		t.Fatal("failed to reserve identity-keyed fill")
	}
	if _, err := fill.Tee(io.Discard).Write([]byte("[Script Info]\n")); err != nil {
		t.Fatal(err)
	}
	if err := fill.Commit(); err != nil {
		t.Fatal(err)
	}

	// Anchor mid-bucket so an 11-minute advance stays inside any bucket wider
	// than ~22 minutes (the durable value) while the old 10-minute bucket
	// always rotates. The artifact must still resolve to the same entry.
	bucketSeconds := int64(subtitleCacheGenerationBucket / time.Second)
	base := fill.srcMtime.Add(time.Duration(bucketSeconds/2) * time.Second)
	later := base.Add(11 * time.Minute)

	resolve := func(at time.Time) string {
		resolved, modTime, size, ok := subtitleCacheSource(source, identity, at)
		if !ok {
			t.Fatalf("identity-keyed source must resolve at %v", at)
		}
		return filepath.Join(c.dir(), subtitleCacheFormatKey(resolved, 0, modTime, size, subtitleFormatASS))
	}
	committed := resolve(base)
	if laterPath := resolve(later); laterPath != committed {
		t.Fatalf("virtual subtitle artifact rotated after 11 minutes:\n got %s\nwant %s", laterPath, committed)
	}
	if _, err := os.Stat(committed); err != nil {
		t.Fatalf("warmed artifact not on disk after the generation advance: %v", err)
	}
	// The live lookup (which uses the wall clock) still serves it.
	if _, _, ok := c.cachedFormatEntryPath(source, identity, 0, subtitleFormatASS); !ok {
		t.Fatal("identity-keyed artifact not served by the live lookup")
	}
}

// A shifted window that a cached slice already covers must be served from that
// slice instead of re-demuxing the source. Exact repeats stay on the fast path;
// this exercises only the covering fallback.
func TestServeExtractTextWindowedShiftedRangeServesCoveringWindow(t *testing.T) {
	c, source := newTestCache(t)
	base := StreamExtractOpts{
		InputPath:             source,
		SourceCodec:           "subrip",
		TrackIndex:            0,
		SeekSeconds:           90,
		DurationSeconds:       150,
		DisableBackgroundWarm: true,
	}
	calls := 0
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		calls++
		_, err := io.WriteString(opts.Writer, textWindowCue)
		return err
	}
	serve := func(seek, duration float64) *httptest.ResponseRecorder {
		opts := base
		opts.SeekSeconds = seek
		opts.DurationSeconds = duration
		rec := httptest.NewRecorder()
		if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, extract); err != nil {
			t.Fatal(err)
		}
		return rec
	}

	if rec := serve(90, 150); rec.Body.String() != textWindowCue {
		t.Fatalf("first body = %q", rec.Body.String())
	}
	// 120..240 is contained by the cached 90..240 slice.
	if rec := serve(120, 120); rec.Body.String() != textWindowCue {
		t.Fatalf("shifted body = %q", rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("extract calls after shifted request = %d, want 1", calls)
	}
	// A range outside the cached slice must still extract.
	serve(400, 120)
	if calls != 2 {
		t.Fatalf("extract calls after uncovered request = %d, want 2", calls)
	}
}

func TestServeExtractTextWindowDoesNotPoisonFullTrack(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip", DurationSeconds: 600}
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, func(_ context.Context, opts StreamExtractOpts) error {
		_, err := io.WriteString(opts.Writer, "partial window")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if f, _, ok := c.lookup(source, "", 0, "vtt"); ok {
		_ = f.Close()
		t.Fatal("window was cached as a complete track")
	}
}

// A windowed text request against a committed full-track entry must extract
// from the small cached artifact even when background warms are disabled
// (virtual relay sources set DisableBackgroundWarm, but an already-populated
// entry must still be served instead of re-demuxing the multi-GB source).
// Disabling the warm must also suppress any detached warm.
func TestServeExtractTextWindowedUsesCachedTrackWithWarmDisabled(t *testing.T) {
	c, source := newTestCache(t)

	// Populate the full-track VTT entry through the real fill path.
	full := StreamExtractOpts{InputPath: source, SourceCodec: "subrip", TrackIndex: 0}
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), full, func(_ context.Context, opts StreamExtractOpts) error {
		_, err := io.WriteString(opts.Writer, "FULL TRACK")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cachedPath := textEntryPath(t, c, source, 0, "vtt")
	if _, err := os.Stat(cachedPath); err != nil {
		t.Fatalf("full-track entry not committed: %v", err)
	}

	var (
		mu                     sync.Mutex
		got                    StreamExtractOpts
		windowCalls, warmCalls int
	)
	windowed := StreamExtractOpts{
		InputPath:             source,
		SourceCodec:           "subrip",
		TrackIndex:            0,
		SeekSeconds:           600,
		DurationSeconds:       600,
		DisableBackgroundWarm: true,
	}
	rec = httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), windowed, func(_ context.Context, opts StreamExtractOpts) error {
		mu.Lock()
		defer mu.Unlock()
		if opts.SeekSeconds == 0 && opts.DurationSeconds == 0 {
			warmCalls++
			return nil
		}
		windowCalls++
		got = opts
		_, err := io.WriteString(opts.Writer, textWindowCue)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if windowCalls != 1 || warmCalls != 0 {
		t.Fatalf("extract calls: window=%d warm=%d, want window=1 warm=0", windowCalls, warmCalls)
	}
	if rec.Body.String() != textWindowCue {
		t.Fatalf("windowed body = %q", rec.Body.String())
	}
	if got.InputPath != cachedPath {
		t.Fatalf("windowed extract input = %q, want cached entry %q", got.InputPath, cachedPath)
	}
	if got.InputIsExtractedText != "vtt" {
		t.Fatalf("windowed extract InputIsExtractedText = %q, want %q", got.InputIsExtractedText, "vtt")
	}
	if got.SeekSeconds != 600 || got.DurationSeconds != 600 {
		t.Fatalf("window parameters not preserved: %+v", got)
	}
}

// A windowed text miss with background warms enabled must trigger exactly one
// detached full-track warm no matter how many windowed requests arrive while
// it runs, and once the warm commits the next windowed request extracts from
// the cached artifact. Mirrors the SUP warm flow in
// TestServeSUPExtractWindowedMissWarmsOnce.
func TestServeExtractTextWindowedMissWarmsOnce(t *testing.T) {
	c, source := newTestCache(t)

	var (
		mu          sync.Mutex
		warmOpts    []StreamExtractOpts
		windowOpts  []StreamExtractOpts
		warmRelease = make(chan struct{})
	)
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		if opts.SeekSeconds == 0 && opts.DurationSeconds == 0 && opts.InputIsExtractedText == "" {
			mu.Lock()
			warmOpts = append(warmOpts, opts)
			mu.Unlock()
			<-warmRelease
			_, err := opts.Writer.Write([]byte("FULL TRACK"))
			return err
		}
		mu.Lock()
		windowOpts = append(windowOpts, opts)
		mu.Unlock()
		_, err := opts.Writer.Write([]byte(textWindowCue))
		return err
	}

	windowed := StreamExtractOpts{InputPath: source, SourceCodec: "subrip", SeekSeconds: 100, DurationSeconds: 3600}
	for i := 0; i < 4; i++ {
		// Distinct windows: an exact repeat would be served from the last-good
		// window cache instead of exercising the pre-warm path.
		next := windowed
		next.SeekSeconds = windowed.SeekSeconds + float64(i)
		rec := httptest.NewRecorder()
		if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), next, extract); err != nil {
			t.Fatal(err)
		}
		if rec.Body.String() != textWindowCue {
			t.Fatalf("windowed body = %q", rec.Body.String())
		}
	}
	close(warmRelease)
	waitForTextCacheEntry(t, c, source, 0, "vtt")

	mu.Lock()
	if len(warmOpts) != 1 {
		t.Fatalf("warm extracts = %d, want exactly 1", len(warmOpts))
	}
	warm := warmOpts[0]
	if warm.InputPath != source || warm.SeekSeconds != 0 || warm.DurationSeconds != 0 || warm.AllowWindow || warm.InputIsExtractedText != "" {
		t.Fatalf("warm must be a full-track extract of the original file: %+v", warm)
	}
	if len(windowOpts) != 4 {
		t.Fatalf("windowed extracts = %d, want 4", len(windowOpts))
	}
	for _, wo := range windowOpts {
		if wo.InputPath != source || wo.InputIsExtractedText != "" {
			t.Fatalf("pre-warm windowed extract must read the original file: %+v", wo)
		}
	}
	mu.Unlock()

	// Warm committed → a fresh window reads the cached artifact.
	postWarm := windowed
	postWarm.SeekSeconds = 500
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), postWarm, extract); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := windowOpts[len(windowOpts)-1]
	mu.Unlock()
	if last.InputPath != textEntryPath(t, c, source, 0, "vtt") || last.InputIsExtractedText != "vtt" {
		t.Fatalf("post-warm windowed extract must read the cached artifact: %+v", last)
	}
}

// A windowed text miss on a virtual identity with the cache's own warm
// disabled (the handler owns the warm for request-scoped relay inputs) must
// still stream the window from the source and must not commit a full-track
// entry itself.
func TestServeExtractTextWindowedVirtualMissSkipsCacheWarm(t *testing.T) {
	c, source := newTestCache(t)
	windowed := StreamExtractOpts{
		InputPath:             source,
		SourceCodec:           "subrip",
		TrackIndex:            0,
		SeekSeconds:           600,
		DurationSeconds:       600,
		CacheIdentity:         "virtual-result-abc",
		DisableBackgroundWarm: true,
	}
	var (
		warmCalls, windowCalls int
		got                    StreamExtractOpts
	)
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), windowed, func(_ context.Context, opts StreamExtractOpts) error {
		if opts.SeekSeconds == 0 && opts.DurationSeconds == 0 {
			warmCalls++
			return nil
		}
		windowCalls++
		got = opts
		_, err := io.WriteString(opts.Writer, textWindowCue)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if windowCalls != 1 || warmCalls != 0 {
		t.Fatalf("extract calls: window=%d warm=%d, want window=1 warm=0", windowCalls, warmCalls)
	}
	if got.InputPath != source || got.InputIsExtractedText != "" {
		t.Fatalf("windowed extract must read the original source: %+v", got)
	}
	if rec.Body.String() != textWindowCue {
		t.Fatalf("windowed body = %q", rec.Body.String())
	}
	if c.HasCommittedTextEntry(source, windowed.CacheIdentity, 0, "subrip", "") {
		t.Fatal("a virtual window miss must not commit a full-track entry")
	}
}

// HasCommittedTextEntry derives the cache format from codec/target exactly as
// ServeExtract does, so the handler's pre-probe check can never disagree with
// the artifact a windowed serve would read. Bitmap codecs and a nil cache read
// as false.
func TestHasCommittedTextEntryFormatAndBitmap(t *testing.T) {
	c, source := newTestCache(t)

	// Commit a VTT artifact through the real full-track serve path.
	full := StreamExtractOpts{InputPath: source, SourceCodec: "subrip", TrackIndex: 0}
	if err := c.ServeExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle", nil), full, func(_ context.Context, opts StreamExtractOpts) error {
		_, err := io.WriteString(opts.Writer, "FULL VTT TRACK")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if !c.HasCommittedTextEntry(source, "", 0, "subrip", "") {
		t.Fatal("vtt entry must be visible for subrip with the default target")
	}
	if !c.HasCommittedTextEntry(source, "", 0, "subrip", "vtt") {
		t.Fatal("vtt entry must be visible for a forced vtt target")
	}
	if !c.HasCommittedTextEntry(source, "", 0, "subrip", ".VTT") {
		t.Fatal("vtt entry must be visible for a case-insensitive target")
	}
	if c.HasCommittedTextEntry(source, "", 0, "ass", "") {
		t.Fatal("ass must not read the vtt entry")
	}
	if c.HasCommittedTextEntry(source, "", 1, "subrip", "") {
		t.Fatal("a different track ordinal must not read the vtt entry")
	}
	if c.HasCommittedTextEntry(source, "", 0, "hdmv_pgs_subtitle", "") {
		t.Fatal("pgs must never read as a committed text entry")
	}
	var nilCache *SubtitleCache
	if nilCache.HasCommittedTextEntry(source, "", 0, "subrip", "") {
		t.Fatal("nil cache must read as a miss")
	}
}

// HasCommittedEntry generalizes HasCommittedTextEntry to every sidecar class
// (VTT, ASS, SUP) so the implicit-window gate can leave a committed track whole
// for text, ASS, and PGS alike.
func TestHasCommittedEntryAllFormats(t *testing.T) {
	c, source := newTestCache(t)

	extractText := func(opts StreamExtractOpts, payload string) {
		t.Helper()
		if err := c.ServeExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, func(_ context.Context, o StreamExtractOpts) error {
			_, err := io.WriteString(o.Writer, payload)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	extractText(StreamExtractOpts{InputPath: source, SourceCodec: "subrip", TrackIndex: 0}, "FULL VTT")
	extractText(StreamExtractOpts{InputPath: source, SourceCodec: "ass", TrackIndex: 1}, "[Script Info]\n")
	if err := c.ServeSUPExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle.sup", nil),
		StreamExtractOpts{InputPath: source, SourceCodec: "hdmv_pgs_subtitle", TrackIndex: 2},
		func(_ context.Context, o StreamExtractOpts) error {
			_, err := o.Writer.Write([]byte("PG"))
			return err
		}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		track int
		codec string
	}{
		{0, "subrip"},
		{1, "ass"},
		{2, "hdmv_pgs_subtitle"},
	} {
		if !c.HasCommittedEntry(source, "", tc.track, tc.codec, "") {
			t.Fatalf("committed %s track %d not visible", tc.codec, tc.track)
		}
	}
	if c.HasCommittedEntry(source, "", 0, "hdmv_pgs_subtitle", "") {
		t.Fatal("PGS must not read the VTT entry at the same ordinal")
	}
	var nilCache *SubtitleCache
	if nilCache.HasCommittedEntry(source, "", 0, "subrip", "") {
		t.Fatal("nil cache must read as a miss")
	}
}

// HasCommittedTextEntry is identity-keyed for a virtual source: the same
// generation bucket and identity that a warm commits under is what the serve
// path queries, so a handler can trust a hit to be the artifact it will read.
// The cache identity carries the row's probed subtitle layout, so an artifact
// committed against one inventory is not served under another. An unchanged
// layout keeps the same identity (the cache survives ordinary re-probing);
// a changed one re-keys it.

func TestHasCommittedTextEntryVirtualIdentity(t *testing.T) {
	c, source := newTestCache(t)
	const identity = "virtual-result-window-1"
	fill := c.beginFill(source, identity, 0, subtitleFormatASS)
	if fill == nil {
		t.Fatal("failed to reserve identity-keyed fill")
	}
	if _, err := fill.Tee(io.Discard).Write([]byte("[Script Info]\n")); err != nil {
		t.Fatal(err)
	}
	if err := fill.Commit(); err != nil {
		t.Fatal(err)
	}

	if !c.HasCommittedTextEntry(source, identity, 0, "ass", "") {
		t.Fatal("committed ass entry must be visible under its virtual identity")
	}
	if c.HasCommittedTextEntry(source, "virtual-result-other", 0, "ass", "") {
		t.Fatal("a different virtual identity must not read the entry")
	}
}

// A duplicate request that joins an in-flight fill which outlives its bounded
// wait must answer retryable instead of holding the connection open and then
// stacking a second full extract for the same key. This is the production
// shape: a 16 s remote demux and a client that refetches the same track.
func TestServeExtractTextBusyFillAnswersRetryableWithoutStacking(t *testing.T) {
	c, source := newTestCache(t)
	c.fillWaitTimeout = 30 * time.Millisecond
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip"}

	held := c.beginFill(source, "", 0, SubtitleFormatVTTV3)
	if held == nil {
		t.Fatal("failed to hold the in-flight fill")
	}
	defer held.Discard()

	extracted := false
	err := c.ServeExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts,
		func(context.Context, StreamExtractOpts) error {
			extracted = true
			return nil
		})
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("duplicate extract error = %v, want the retryable busy answer", err)
	}
	if retry.RetryAfter != subtitleFillBusyRetryAfter {
		t.Fatalf("Retry-After = %s, want %s", retry.RetryAfter, subtitleFillBusyRetryAfter)
	}
	if extracted {
		t.Fatal("the duplicate request ran its own extraction while another fill was in flight")
	}
	select {
	case <-held.done:
		t.Fatal("the duplicate request settled the fill it was waiting on")
	default:
	}
}

// The cached VTT is served without running ffmpeg again: the identity that
// committed the artifact is the identity the next request resolves.
func TestServeExtractTextCachedArtifactSkipsExtract(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip"}
	const payload = "WEBVTT\n\n00:00.000 --> 00:01.000\ncached\n"

	calls := 0
	first := httptest.NewRecorder()
	if err := c.ServeExtract(first, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts,
		func(_ context.Context, o StreamExtractOpts) error {
			calls++
			_, err := io.WriteString(o.Writer, payload)
			return err
		}); err != nil {
		t.Fatal(err)
	}

	second := httptest.NewRecorder()
	if err := c.ServeExtract(second, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts,
		func(context.Context, StreamExtractOpts) error {
			calls++
			return errors.New("the cached artifact must be served without a re-extract")
		}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1 (the fill)", calls)
	}
	if got := second.Body.String(); got != payload {
		t.Fatalf("cached body = %q, want %q", got, payload)
	}
}

// A fill that misses the cache while another fill for the same key is in flight
// must wait for that fill and serve its committed bytes — a plan-time warm is
// not duplicated by the first client fetch. Exactly one extract (the warm)
// runs.
func TestServeExtractTextWaitsForInFlightFillAndServesCommitted(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip"}

	var (
		mu    sync.Mutex
		calls int
	)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFn)
	done := c.WarmTrackInBackground(opts, func(ctx context.Context, o StreamExtractOpts) error {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err := io.WriteString(o.Writer, "WARM TRACK")
		return err
	}, "")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background warm did not start")
	}

	clientDone := make(chan error, 1)
	rec := httptest.NewRecorder()
	go func() {
		clientDone <- c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, func(_ context.Context, o StreamExtractOpts) error {
			mu.Lock()
			calls++
			mu.Unlock()
			_, err := io.WriteString(o.Writer, "CLIENT TRACK")
			return err
		})
	}()

	// The client must be waiting on the warm, not running its own demux.
	select {
	case err := <-clientDone:
		t.Fatalf("client returned before the warm committed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseFn()
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background warm did not finish")
	}

	if got := rec.Body.String(); got != "WARM TRACK" {
		t.Fatalf("client body = %q, want the warm's committed bytes", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("extract ran %d times, want exactly 1 (the warm)", calls)
	}
}

// With no in-flight fill the cold path streams from its own extract unchanged.
func TestServeExtractTextColdPathStreamsImmediately(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip"}
	calls := 0
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, func(_ context.Context, o StreamExtractOpts) error {
		calls++
		_, err := io.WriteString(o.Writer, "COLD TRACK")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("cold path extract calls = %d, want 1", calls)
	}
	if got := rec.Body.String(); got != "COLD TRACK" {
		t.Fatalf("cold body = %q", got)
	}
}

// When the fill a request is waiting on fails without committing, the waiter
// must take over the reservation and extract its own response rather than
// hanging or dereferencing a nil fill.
func TestServeExtractTextInFlightFillFailureRetries(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip"}

	held := c.beginFill(source, "", 0, SubtitleFormatVTTV3)
	if held == nil {
		t.Fatal("failed to hold the in-flight fill")
	}

	clientDone := make(chan error, 1)
	rec := httptest.NewRecorder()
	go func() {
		clientDone <- c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, func(_ context.Context, o StreamExtractOpts) error {
			_, err := io.WriteString(o.Writer, "CLIENT TRACK")
			return err
		})
	}()

	select {
	case err := <-clientDone:
		t.Fatalf("client returned before the held fill settled: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// The holder fails: no entry lands, so the waiter re-attempts the fill.
	held.Discard()

	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	if got := rec.Body.String(); got != "CLIENT TRACK" {
		t.Fatalf("body = %q", got)
	}
	f, _, ok := c.lookup(source, "", 0, SubtitleFormatVTTV3)
	if !ok {
		t.Fatal("waiter's takeover extract was not committed")
	}
	_ = f.Close()
}

// A canceled waiter returns its context error instead of blocking until the
// in-flight fill finishes, and must not touch the holder's fill.
func TestServeExtractTextWaitHonorsContextCancel(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip"}

	held := c.beginFill(source, "", 0, SubtitleFormatVTTV3)
	if held == nil {
		t.Fatal("failed to hold the in-flight fill")
	}
	defer held.Discard()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	extracted := false
	err := c.ServeExtract(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/subtitle", nil), opts, func(context.Context, StreamExtractOpts) error {
		extracted = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
	if extracted {
		t.Fatal("canceled waiter must not extract")
	}
}

// The SUP full-track path gets the same treatment: a request arriving while a
// fill is in flight waits and serves the committed .sup instead of running a
// duplicate full demux.
func TestServeSUPExtractWaitsForInFlightFill(t *testing.T) {
	c, source := newTestCache(t)
	opts := supExtractOpts(source, 0)

	held := c.beginFill(source, "", 0, subtitleFormatSUP)
	if held == nil {
		t.Fatal("failed to hold the in-flight fill")
	}

	extracted := false
	clientDone := make(chan error, 1)
	rec := httptest.NewRecorder()
	go func() {
		clientDone <- c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup", nil), opts, func(_ context.Context, o StreamExtractOpts) error {
			extracted = true
			_, err := o.Writer.Write([]byte("CLIENT SUP"))
			return err
		})
	}()

	select {
	case err := <-clientDone:
		t.Fatalf("client returned before the fill committed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// The holder commits: the waiter must serve these bytes from cache.
	if _, err := held.Tee(io.Discard).Write([]byte("SUP PAYLOAD")); err != nil {
		t.Fatal(err)
	}
	if err := held.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	if got := rec.Body.String(); got != "SUP PAYLOAD" {
		t.Fatalf("client body = %q, want the committed .sup", got)
	}
	// A committed entry is served with Last-Modified, never a streamed 200.
	if rec.Header().Get("Last-Modified") == "" {
		t.Fatal("waited serve must be a cached ServeContent response")
	}
	if extracted {
		t.Fatal("waited request must not run its own extract")
	}
}

func TestServeExtractTextCancelledViewerDoesNotDiscardFill(t *testing.T) {
	c, source := newTestCache(t)
	fill := c.beginFill(source, "", 0, "vtt")
	if fill == nil {
		t.Fatal("failed to reserve fill")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := c.ServeExtract(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/subtitle", nil),
		StreamExtractOpts{InputPath: source, SourceCodec: "subrip"}, func(context.Context, StreamExtractOpts) error {
			t.Error("canceled request must not extract")
			return nil
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if _, err := fill.Tee(io.Discard).Write([]byte("complete track")); err != nil {
		t.Fatal(err)
	}
	if err := fill.Commit(); err != nil {
		t.Fatal(err)
	}
	f, _, ok := c.lookup(source, "", 0, "vtt")
	if !ok {
		t.Fatal("canceled viewer discarded the active fill")
	}
	_ = f.Close()
}

func TestServeExtractOutlivesServerWriteTimeout(t *testing.T) {
	for _, codec := range []string{"subrip", "ass", "hdmv_pgs_subtitle"} {
		t.Run(codec, func(t *testing.T) {
			c, source := newTestCache(t)
			const timeout = 25 * time.Millisecond
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				err := c.ServeExtract(w, r, StreamExtractOpts{InputPath: source, SourceCodec: codec}, func(ctx context.Context, opts StreamExtractOpts) error {
					// Explicitly cross the listener's absolute deadline before producing cues.
					timer := time.NewTimer(3 * timeout)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-ctx.Done():
						return ctx.Err()
					}
					_, err := io.WriteString(opts.Writer, "complete subtitle track")
					return err
				})
				if err != nil {
					t.Errorf("extract: %v", err)
				}
			}))
			server.Config.WriteTimeout = timeout
			server.Start()
			defer server.Close()
			resp, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if err != nil || string(body) != "complete subtitle track" {
				t.Fatalf("body=%q error=%v", body, err)
			}
			f, _, ok := c.lookup(source, "", 0, map[string]string{"subrip": "vtt", "ass": "ass", "hdmv_pgs_subtitle": "sup"}[codec])
			if !ok {
				t.Fatal("completed extraction not cached")
			}
			_ = f.Close()
		})
	}
}

func TestServeExtractTextFailedFillRetries(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "subrip"}
	for _, fail := range []bool{true, false} {
		err := c.ServeExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle", nil), opts, func(_ context.Context, opts StreamExtractOpts) error {
			if _, err := io.WriteString(opts.Writer, "partial track"); err != nil {
				return err
			}
			if fail {
				return errors.New("demux interrupted")
			}
			return nil
		})
		if (err != nil) != fail {
			t.Fatalf("fail=%v extract error=%v", fail, err)
		}
		f, _, ok := c.lookup(source, "", 0, "vtt")
		if ok {
			_ = f.Close()
		}
		if ok == fail {
			t.Fatalf("fail=%v cache hit=%v", fail, ok)
		}
	}
}

// newTestCache builds a cache rooted under a temp transcode dir and returns
// it with the path of a fake source media file.
func newTestCache(t *testing.T) (*SubtitleCache, string) {
	t.Helper()
	base := t.TempDir()
	source := filepath.Join(base, "movie.mkv")
	if err := os.WriteFile(source, []byte("fake mkv contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewSubtitleCache(func() string { return base }), source
}

// fillEntry populates the cache for source+track with the given payload via
// the real BeginFill → Tee → Commit path.
func fillEntry(t *testing.T, c *SubtitleCache, source string, track int, payload string) {
	t.Helper()
	fill := c.BeginFill(source, track)
	if fill == nil {
		t.Fatalf("BeginFill returned nil for track %d", track)
	}
	if _, err := fill.Tee(io.Discard).Write([]byte(payload)); err != nil {
		t.Fatalf("tee write: %v", err)
	}
	if err := fill.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// supExtractOpts builds the base extract options a handler would pass to
// ServeSUPExtract for a PGS track.
func supExtractOpts(source string, track int) StreamExtractOpts {
	return StreamExtractOpts{
		InputPath:   source,
		TrackIndex:  track,
		SourceCodec: "hdmv_pgs_subtitle",
	}
}

// windowedSupOpts builds options for a windowed (?windowed=1) PGS request.
func windowedSupOpts(source string, track int, seek, duration float64) StreamExtractOpts {
	opts := supExtractOpts(source, track)
	opts.AllowWindow = true
	opts.SeekSeconds = seek
	opts.DurationSeconds = duration
	return opts
}

// waitForCacheEntry polls until the cache holds a committed entry for
// source+track — used to observe asynchronous background warms.
func waitForCacheEntry(t *testing.T, c *SubtitleCache, source string, track int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f, _, ok := c.Lookup(source, track); ok {
			_ = f.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cache entry for track %d never appeared", track)
}

func readAllAndClose(t *testing.T, f *os.File) string {
	t.Helper()
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// textEntryPath computes the committed cache-entry path for source+track in
// the given text format (e.g. "vtt", "ass").
func textEntryPath(t *testing.T, c *SubtitleCache, source string, track int, format string) string {
	t.Helper()
	src, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(c.dir(), subtitleCacheFormatKey(source, track, src.ModTime(), src.Size(), format))
}

// waitForTextCacheEntry polls until the cache holds a committed entry for
// source+track in the given text format, like waitForCacheEntry but for
// non-SUP artifacts.
func waitForTextCacheEntry(t *testing.T, c *SubtitleCache, source string, track int, format string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f, _, ok := c.lookup(source, "", track, format); ok {
			_ = f.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cache entry for track %d format %q never appeared", track, format)
}

func TestSubtitleCacheMissThenHit(t *testing.T) {
	c, source := newTestCache(t)

	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("expected miss on empty cache")
	}

	fillEntry(t, c, source, 0, "PGS DATA TRACK 0")

	f, modTime, ok := c.Lookup(source, 0)
	if !ok {
		t.Fatal("expected hit after commit")
	}
	if got := readAllAndClose(t, f); got != "PGS DATA TRACK 0" {
		t.Fatalf("cached content = %q", got)
	}
	src, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if !modTime.Equal(src.ModTime()) {
		t.Fatalf("hit modTime = %v, want source mtime %v", modTime, src.ModTime())
	}

	// A different track ordinal is a distinct entry.
	if _, _, ok := c.Lookup(source, 1); ok {
		t.Fatal("expected miss for uncached track ordinal")
	}
}

func TestSubtitleCacheTextRoundTripAndFormatIsolation(t *testing.T) {
	c, source := newTestCache(t)
	if _, ok := c.LookupText(source, 0, "srt"); ok {
		t.Fatal("expected text miss on empty cache")
	}
	if _, err := c.ExtractText(t.Context(), source, 0, "subrip", func(context.Context) ([]byte, error) { return []byte("SRT DATA"), nil }); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.LookupText(source, 0, "srt"); !ok || string(got) != "SRT DATA" {
		t.Fatalf("SRT lookup = %q, %t", got, ok)
	}
	if _, ok := c.LookupText(source, 0, "ass"); ok {
		t.Fatal("SRT cache entry must not satisfy ASS lookup")
	}
	if _, ok := c.LookupText(source, 1, "srt"); ok {
		t.Fatal("text cache entry must not satisfy another track")
	}
}

func TestSubtitleCacheTextInvalidatedBySourceChange(t *testing.T) {
	c, source := newTestCache(t)
	if _, err := c.ExtractText(t.Context(), source, 0, "srt", func(context.Context) ([]byte, error) { return []byte("old extract"), nil }); err != nil {
		t.Fatal(err)
	}

	newTime := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(source, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupText(source, 0, "srt"); ok {
		t.Fatal("expected text miss after source mtime changed")
	}
	if _, err := c.ExtractText(t.Context(), source, 0, "srt", func(context.Context) ([]byte, error) { return []byte("new extract"), nil }); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.LookupText(source, 0, "srt"); !ok || string(got) != "new extract" {
		t.Fatalf("refilled SRT lookup = %q, %t", got, ok)
	}
	if n := countMatching(t, c, func(name string) bool { return strings.HasSuffix(name, ".srt") }); n != 1 {
		t.Fatalf("stale text sibling not removed: %d entries", n)
	}
}

func TestSubtitleCacheInvalidatedBySourceMtime(t *testing.T) {
	c, source := newTestCache(t)
	fillEntry(t, c, source, 0, "old extract")

	newTime := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(source, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("expected miss after source mtime changed")
	}

	// Re-filling under the new source identity overwrites, and the stale
	// sibling entry is cleaned up.
	fillEntry(t, c, source, 0, "new extract")
	f, _, ok := c.Lookup(source, 0)
	if !ok {
		t.Fatal("expected hit after refill")
	}
	if got := readAllAndClose(t, f); got != "new extract" {
		t.Fatalf("cached content = %q", got)
	}
	if n := countCacheEntries(t, c); n != 1 {
		t.Fatalf("stale sibling not removed: %d entries", n)
	}
}

func TestSubtitleCacheInvalidatedBySourceSize(t *testing.T) {
	c, source := newTestCache(t)
	fillEntry(t, c, source, 0, "old extract")

	src, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("different length contents!"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Restore the original mtime so only size differs.
	if err := os.Chtimes(source, src.ModTime(), src.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("expected miss after source size changed")
	}
}

func TestSubtitleCacheCommitRefusesChangedSource(t *testing.T) {
	c, source := newTestCache(t)
	fill := c.BeginFill(source, 0)
	if fill == nil {
		t.Fatal("BeginFill returned nil")
	}
	if _, err := fill.Tee(io.Discard).Write([]byte("extract from old source")); err != nil {
		t.Fatal(err)
	}
	// Source replaced mid-extract.
	newTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(source, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if err := fill.Commit(); err == nil {
		t.Fatal("Commit must refuse when source changed mid-fill")
	}
	if n := countCacheFiles(t, c); n != 0 {
		t.Fatalf("refused commit left %d files", n)
	}
}

func TestSubtitleCacheTeeWriteFailureKeepsServingClient(t *testing.T) {
	c, source := newTestCache(t)
	fill := c.BeginFill(source, 0)
	if fill == nil {
		t.Fatal("BeginFill returned nil")
	}
	// Force temp-file writes to fail (simulates disk full).
	_ = fill.tmp.Close()

	var client strings.Builder
	n, err := fill.Tee(&client).Write([]byte("bytes for the viewer"))
	if err != nil || n != len("bytes for the viewer") {
		t.Fatalf("client write must succeed despite cache failure: n=%d err=%v", n, err)
	}
	if client.String() != "bytes for the viewer" {
		t.Fatalf("client got %q", client.String())
	}
	if err := fill.Commit(); err == nil {
		t.Fatal("Commit must fail after tee write error")
	}
	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("failed fill must not be served")
	}
}

func TestSubtitleCacheEvictionUnderCap(t *testing.T) {
	c, source := newTestCache(t)
	c.maxBytes = 25 // each payload below is 10 bytes

	base := time.Now().Add(-time.Hour)
	for track := 0; track < 3; track++ {
		fillEntry(t, c, source, track, fmt.Sprintf("0123456%03d", track))
		// Pin distinct LRU mtimes: track 0 oldest.
		path := entryPath(t, c, source, track)
		mt := base.Add(time.Duration(track) * time.Minute)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	// 4th commit (10 bytes) pushes the total to 40 > 25; eviction must
	// remove the two oldest entries (tracks 0 and 1) to get back to 20.
	fillEntry(t, c, source, 3, "0123456003")

	for track, want := range map[int]bool{0: false, 1: false, 2: true, 3: true} {
		_, _, ok := c.Lookup(source, track)
		if ok != want {
			t.Errorf("track %d cached = %v, want %v", track, ok, want)
		}
	}
}

func TestSubtitleCacheCoalescing(t *testing.T) {
	c, source := newTestCache(t)

	first := c.BeginFill(source, 0)
	if first == nil {
		t.Fatal("first BeginFill returned nil")
	}
	if second := c.BeginFill(source, 0); second != nil {
		second.Discard()
		t.Fatal("second BeginFill for in-flight track must return nil")
	}
	// A different track is independent.
	other := c.BeginFill(source, 1)
	if other == nil {
		t.Fatal("BeginFill for a different track must not be blocked")
	}
	other.Discard()

	if _, err := first.Tee(io.Discard).Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	// Slot released after commit.
	if again := c.BeginFill(source, 0); again == nil {
		t.Fatal("BeginFill must work again after Commit")
	} else {
		again.Discard()
	}
}

func TestSubtitleCacheCoalescingConcurrent(t *testing.T) {
	c, source := newTestCache(t)

	const workers = 16
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		fills []*SubtitleCacheFill
	)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if f := c.BeginFill(source, 0); f != nil {
				mu.Lock()
				fills = append(fills, f)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(fills) != 1 {
		t.Fatalf("exactly one concurrent BeginFill must win, got %d", len(fills))
	}
	fills[0].Discard()
}

func TestServeSUPExtractCacheFlow(t *testing.T) {
	c, source := newTestCache(t)

	extractCalls := 0
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		extractCalls++
		_, err := opts.Writer.Write([]byte("SUP PAYLOAD"))
		return err
	}

	// First request: miss → streamed 200 with no-store, entry committed.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	if err := c.ServeSUPExtract(rec, req, supExtractOpts(source, 0), extract); err != nil {
		t.Fatal(err)
	}
	if extractCalls != 1 {
		t.Fatalf("extract calls = %d", extractCalls)
	}
	if rec.Body.String() != "SUP PAYLOAD" {
		t.Fatalf("miss body = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("miss Cache-Control = %q", cc)
	}

	// Second request: hit → served from cache, no extract, revalidatable.
	rec = httptest.NewRecorder()
	if err := c.ServeSUPExtract(rec, req, supExtractOpts(source, 0), extract); err != nil {
		t.Fatal(err)
	}
	if extractCalls != 1 {
		t.Fatal("cache hit must not invoke extract")
	}
	if rec.Body.String() != "SUP PAYLOAD" {
		t.Fatalf("hit body = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("hit Cache-Control = %q", cc)
	}
	if rec.Header().Get("Last-Modified") == "" {
		t.Fatal("hit must carry Last-Modified")
	}
	if cl := rec.Header().Get("Content-Length"); cl != "11" {
		t.Fatalf("hit Content-Length = %q", cl)
	}

	// Range request against the cached entry.
	rec = httptest.NewRecorder()
	rangeReq := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	rangeReq.Header.Set("Range", "bytes=4-10")
	if err := c.ServeSUPExtract(rec, rangeReq, supExtractOpts(source, 0), extract); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "PAYLOAD" {
		t.Fatalf("range: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// HEAD uses the same cached binary representation and metadata without
	// invoking a fresh extract or returning a body.
	rec = httptest.NewRecorder()
	headReq := httptest.NewRequest(http.MethodHead, "/sub.sup", nil)
	if err := c.ServeSUPExtract(rec, headReq, supExtractOpts(source, 0), extract); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("head: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if extractCalls != 1 {
		t.Fatal("cached HEAD must not invoke extract")
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("head Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "11" {
		t.Fatalf("head Content-Length = %q", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("head Accept-Ranges = %q", got)
	}
}

// A windowed request against a cached track must run its extract with the
// cached .sup as input (small file → near-instant window) instead of
// re-demuxing the original media, must never publish its sliced output as a
// cache entry, and must bump the entry's LRU recency.
func TestServeSUPExtractWindowedUsesCachedTrack(t *testing.T) {
	c, source := newTestCache(t)
	fillEntry(t, c, source, 0, "FULL TRACK")

	// Age the entry so the LRU recency bump is observable.
	entry := entryPath(t, c, source, 0)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(entry, old, old); err != nil {
		t.Fatal(err)
	}

	var got StreamExtractOpts
	extractCalls := 0
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1200&duration=3600", nil)
	err := c.ServeSUPExtract(rec, req, windowedSupOpts(source, 0, 1200, 3600), func(_ context.Context, opts StreamExtractOpts) error {
		extractCalls++
		got = opts
		_, err := opts.Writer.Write(pgsWindowPage)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if extractCalls != 1 {
		t.Fatalf("extract calls = %d", extractCalls)
	}
	if !bytes.Equal(rec.Body.Bytes(), pgsWindowPage) {
		t.Fatalf("windowed body = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("windowed Cache-Control = %q", cc)
	}
	if got.InputPath != entry {
		t.Fatalf("windowed extract input = %q, want cached entry %q", got.InputPath, entry)
	}
	if !got.InputIsExtractedSup {
		t.Fatal("windowed extract from cache must set InputIsExtractedSup")
	}
	if got.SeekSeconds != 1200 || got.DurationSeconds != 3600 || !got.AllowWindow {
		t.Fatalf("window parameters not preserved: %+v", got)
	}

	// The full-track entry must be untouched, with recency bumped.
	f, _, ok := c.Lookup(source, 0)
	if !ok {
		t.Fatal("full-track entry lost")
	}
	if content := readAllAndClose(t, f); content != "FULL TRACK" {
		t.Fatalf("full-track entry corrupted: %q", content)
	}
	info, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().After(old.Add(time.Minute)) {
		t.Fatalf("windowed serve must bump LRU recency: mtime = %v", info.ModTime())
	}
}

// A windowed miss must trigger exactly one detached background warm no
// matter how many windowed requests arrive while it runs, and once the warm
// commits, the next windowed request extracts from the cached track.
func TestServeSUPExtractWindowedMissWarmsOnce(t *testing.T) {
	c, source := newTestCache(t)

	var (
		mu          sync.Mutex
		warmOpts    []StreamExtractOpts
		windowOpts  []StreamExtractOpts
		warmRelease = make(chan struct{})
	)
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		if opts.AllowWindow {
			mu.Lock()
			windowOpts = append(windowOpts, opts)
			mu.Unlock()
			_, err := opts.Writer.Write(pgsWindowPage)
			return err
		}
		mu.Lock()
		warmOpts = append(warmOpts, opts)
		mu.Unlock()
		<-warmRelease
		_, err := opts.Writer.Write([]byte("FULL TRACK"))
		return err
	}

	// N windowed misses: each still streams its own windowed slice from the
	// original file; only the first starts a warm (BeginFill coalescing keeps
	// the rest out — deterministic because the in-flight slot is reserved
	// synchronously before ServeSUPExtract returns). Distinct windows: an exact
	// repeat would be served from the last-good window cache instead of
	// exercising the pre-warm path.
	for i := 0; i < 4; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=100&duration=3600", nil)
		if err := c.ServeSUPExtract(rec, req, windowedSupOpts(source, 0, 100+float64(i), 3600), extract); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(rec.Body.Bytes(), pgsWindowPage) {
			t.Fatalf("windowed body = %q", rec.Body.String())
		}
	}
	close(warmRelease)
	waitForCacheEntry(t, c, source, 0)

	mu.Lock()
	if len(warmOpts) != 1 {
		t.Fatalf("warm extracts = %d, want exactly 1", len(warmOpts))
	}
	warm := warmOpts[0]
	if warm.InputPath != source || warm.SeekSeconds != 0 || warm.DurationSeconds != 0 || warm.AllowWindow || warm.InputIsExtractedSup {
		t.Fatalf("warm must be a full-track extract of the original file: %+v", warm)
	}
	if len(windowOpts) != 4 {
		t.Fatalf("windowed extracts = %d, want 4", len(windowOpts))
	}
	for _, wo := range windowOpts {
		if wo.InputPath != source || wo.InputIsExtractedSup {
			t.Fatalf("pre-warm windowed extract must read the original file: %+v", wo)
		}
	}
	mu.Unlock()

	// Warm committed → the next windowed request reads the cached track.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=200&duration=3600", nil)
	if err := c.ServeSUPExtract(rec, req, windowedSupOpts(source, 0, 200, 3600), extract); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := windowOpts[len(windowOpts)-1]
	mu.Unlock()
	if last.InputPath != entryPath(t, c, source, 0) || !last.InputIsExtractedSup {
		t.Fatalf("post-warm windowed extract must read the cached track: %+v", last)
	}
}

func TestServeSUPExtractWindowedRemoteInputSkipsDetachedWarm(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 100, 3600)
	opts.CacheIdentity = "virtual://movie/tt123?profile=1080p"
	// A remote container small enough that a page read is cheap keeps the direct
	// page extract; a large or unknown one is served from the progressive
	// artifact instead (see subtitle_progressive_sup_test.go).
	opts.SourceSizeBytes = 100 << 20
	opts.DisableBackgroundWarm = true

	var warmCalls, windowCalls int
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil)
	err := c.ServeSUPExtract(recorder, request, opts, func(_ context.Context, extractOpts StreamExtractOpts) error {
		if extractOpts.AllowWindow {
			windowCalls++
			_, writeErr := extractOpts.Writer.Write(pgsWindowPage)
			return writeErr
		}
		warmCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if windowCalls != 1 || warmCalls != 0 {
		t.Fatalf("extract calls: window=%d warm=%d, want window=1 warm=0", windowCalls, warmCalls)
	}
	if !bytes.Equal(recorder.Body.Bytes(), pgsWindowPage) {
		t.Fatalf("windowed body = %q", recorder.Body.String())
	}
}

// The window cache key is the original source identity, so a window stored
// before a full-track artifact exists is still a hit after one is committed.
func TestServeSUPExtractWindowedCacheKeySurvivesArtifactCommit(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 300, 600)
	opts.DisableBackgroundWarm = true
	payload := string(pgsWindowPage)

	calls := 0
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		calls++
		_, err := io.WriteString(extractOpts.Writer, payload)
		return err
	}
	rec := httptest.NewRecorder()
	if err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil), opts, extract); err != nil {
		t.Fatalf("source-backed request: %v", err)
	}

	// A full-track artifact now exists, so a repeat would take the cached-.sup
	// fast path if it missed the window cache. The key must not change.
	fillEntry(t, c, source, 0, "FULL TRACK")

	rec = httptest.NewRecorder()
	if err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil), opts, extract); err != nil {
		t.Fatalf("artifact-backed request: %v", err)
	}
	if rec.Body.String() != payload {
		t.Fatalf("body = %q, want the cached window %q", rec.Body.String(), payload)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want the cached window (still 1)", calls)
	}
}

// A repeated identical windowed SUP request must serve the last-good copy
// instead of re-running ffmpeg.
func TestServeSUPExtractWindowedRepeatServesLastGood(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 300, 600)
	opts.DisableBackgroundWarm = true
	payload := string(pgsWindowPage)

	calls := 0
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		calls++
		_, err := io.WriteString(extractOpts.Writer, payload)
		return err
	}
	for attempt := range 2 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=300&duration=600", nil)
		if err := c.ServeSUPExtract(rec, req, opts, extract); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if rec.Body.String() != payload {
			t.Fatalf("attempt %d body = %q, want %q", attempt, rec.Body.String(), payload)
		}
		wantCacheControl := "private, no-cache"
		if attempt == 0 {
			wantCacheControl = "no-store"
		}
		if cc := rec.Header().Get("Cache-Control"); cc != wantCacheControl {
			t.Fatalf("attempt %d Cache-Control = %q, want %q", attempt, cc, wantCacheControl)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Fatalf("attempt %d Content-Type = %q", attempt, ct)
		}
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1 for a repeated window", calls)
	}
}

// Concurrent identical windowed SUP requests must coalesce onto one extraction
// and all serve its bytes.
func TestServeSUPExtractWindowedCoalescesConcurrentRepeats(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 300, 600)
	opts.DisableBackgroundWarm = true
	payload := string(pgsWindowPage)

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })

	var (
		mu    sync.Mutex
		calls int
	)
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		mu.Lock()
		calls++
		leader := calls == 1
		mu.Unlock()
		if leader {
			close(started)
			<-release
		}
		_, err := io.WriteString(extractOpts.Writer, payload)
		return err
	}

	type result struct {
		body string
		err  error
	}
	const requests = 4
	begin := make(chan struct{})
	results := make(chan result, requests)
	for range requests {
		go func() {
			<-begin
			rec := httptest.NewRecorder()
			err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil), opts, extract)
			results <- result{body: rec.Body.String(), err: err}
		}()
	}
	close(begin)

	// The leader holds the extraction open; the others either join its flight
	// or, if they arrive after it stores the slice, serve that stored copy.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("coalesced window extract did not start")
	}
	releaseOnce.Do(func() { close(release) })

	for range requests {
		got := <-results
		if got.err != nil {
			t.Fatalf("request error = %v", got.err)
		}
		if got.body != payload {
			t.Fatalf("body = %q, want %q", got.body, payload)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1 coalesced extraction", calls)
	}
}

// Without a cached window, an upstream 5xx leaves the identity in a backoff so
// an immediate retry is answered retryable without opening the source again.
func TestServeSUPExtractWindowedUpstreamFailureBacksOff(t *testing.T) {
	c, source := newTestCache(t)
	c.extractRetryDelays = []time.Duration{}
	opts := windowedSupOpts(source, 0, 300, 600)
	opts.DisableBackgroundWarm = true

	calls := 0
	extract := func(context.Context, StreamExtractOpts) error {
		calls++
		return upstreamSubtitleError()
	}
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil)
	err := c.ServeSUPExtract(httptest.NewRecorder(), req, opts, extract)
	if !IsSubtitleUpstreamError(err) {
		t.Fatalf("first request error = %v, want the upstream failure", err)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1", calls)
	}

	rec := httptest.NewRecorder()
	err = c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil), opts, extract)
	var retry *SubtitleExtractionRetryError
	if !errors.As(err, &retry) {
		t.Fatalf("retry error = %v, want SubtitleExtractionRetryError", err)
	}
	if retry.RetryAfter <= 0 || retry.RetryAfter > subtitleExtractionFailureBaseCooldown {
		t.Fatalf("retry after = %s, want within %s", retry.RetryAfter, subtitleExtractionFailureBaseCooldown)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d after a retry inside the backoff, want 1", calls)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("backoff response body = %q, want none", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Fatalf("backoff response must not commit a Content-Type, got %q", ct)
	}
}

// A cached window stays servable while its identity is in the failure backoff.
func TestServeSUPExtractWindowedServesLastGoodDuringCooldown(t *testing.T) {
	c, source := newTestCache(t)
	opts := windowedSupOpts(source, 0, 300, 600)
	opts.DisableBackgroundWarm = true
	payload := string(pgsWindowPage)

	calls := 0
	succeed := func(_ context.Context, extractOpts StreamExtractOpts) error {
		calls++
		_, err := io.WriteString(extractOpts.Writer, payload)
		return err
	}
	rec := httptest.NewRecorder()
	if err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil), opts, succeed); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	if rec.Body.String() != payload {
		t.Fatalf("seed body = %q, want %q", rec.Body.String(), payload)
	}

	// A different window of the same identity failed upstream, so the identity
	// is cooling down.
	c.noteExtractionFailure(opts, subtitleFormatSUP)

	failing := func(context.Context, StreamExtractOpts) error {
		calls++
		return upstreamSubtitleError()
	}
	rec = httptest.NewRecorder()
	if err := c.ServeSUPExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil), opts, failing); err != nil {
		t.Fatalf("cached window during cooldown: %v", err)
	}
	if rec.Body.String() != payload {
		t.Fatalf("cached body during cooldown = %q, want %q", rec.Body.String(), payload)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("cached window Cache-Control = %q", cc)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want the cached copy (still 1)", calls)
	}
}

// Warms beyond the server-wide slot budget are dropped, not queued, and a
// dropped warm must not leave an in-flight reservation behind.
func TestWarmInBackgroundSemaphoreDrop(t *testing.T) {
	c, source := newTestCache(t)

	release := make(chan struct{})
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		<-release
		_, err := opts.Writer.Write([]byte("FULL TRACK"))
		return err
	}

	// Occupy every warm slot (slots are acquired synchronously).
	for track := 0; track < subtitleCacheWarmSlots; track++ {
		c.WarmInBackground(supExtractOpts(source, track), extract, "")
	}
	// One more: dropped without reserving the track's in-flight slot.
	overflow := subtitleCacheWarmSlots
	c.WarmInBackground(supExtractOpts(source, overflow), extract, "")
	if fill := c.BeginFill(source, overflow); fill == nil {
		t.Fatal("dropped warm must not hold the in-flight slot")
	} else {
		fill.Discard()
	}

	close(release)
	for track := 0; track < subtitleCacheWarmSlots; track++ {
		waitForCacheEntry(t, c, source, track)
	}
	if _, _, ok := c.Lookup(source, overflow); ok {
		t.Fatal("dropped warm must not populate the cache")
	}

	// A committed file is visible before the worker releases its warm slot.
	// Acquire every slot to wait for those deferred releases, then return them.
	for range subtitleCacheWarmSlots {
		select {
		case c.warmSem <- struct{}{}:
		case <-time.After(5 * time.Second):
			t.Fatal("completed warms did not release their slots")
		}
	}
	for range subtitleCacheWarmSlots {
		<-c.warmSem
	}

	// With slots free again, the overflow track's warm goes through.
	c.WarmInBackground(supExtractOpts(source, overflow), extract, "")
	waitForCacheEntry(t, c, source, overflow)
}

// A warm that races an already-in-flight client fill must skip (BeginFill
// coalescing) and release its warm slot for other tracks.
func TestWarmInBackgroundSkipsInFlightFill(t *testing.T) {
	c, source := newTestCache(t)

	clientFill := c.BeginFill(source, 0)
	if clientFill == nil {
		t.Fatal("BeginFill returned nil")
	}
	warmed := make(chan struct{}, 1)
	c.WarmInBackground(supExtractOpts(source, 0), func(_ context.Context, opts StreamExtractOpts) error {
		warmed <- struct{}{}
		_, err := opts.Writer.Write([]byte("WARM"))
		return err
	}, "")

	// The skipped warm must have released its slot synchronously: all
	// subtitleCacheWarmSlots slots are still available.
	for track := 1; track <= subtitleCacheWarmSlots; track++ {
		c.WarmInBackground(supExtractOpts(source, track), func(_ context.Context, opts StreamExtractOpts) error {
			_, err := opts.Writer.Write([]byte("FULL TRACK"))
			return err
		}, "")
	}
	for track := 1; track <= subtitleCacheWarmSlots; track++ {
		waitForCacheEntry(t, c, source, track)
	}

	select {
	case <-warmed:
		t.Fatal("warm for an in-flight track must not run")
	default:
	}
	clientFill.Discard()
}

// A warm that cannot reserve the track because the request path already owns the
// fill must say why. An admitted-but-idle warm with no skip-reason line is
// indistinguishable from a broken one (production: 17 kicks, zero admissions,
// zero skip reasons). The request-path fill is the artifact the warm exists for,
// so refusing to join it is the right policy; the refusal just has to be
// visible.
func TestWarmTrackInBackgroundLogsTheFillItYieldsTo(t *testing.T) {
	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	c, source := newTestCache(t)
	clientFill := c.BeginFill(source, 0)
	if clientFill == nil {
		t.Fatal("BeginFill returned nil")
	}
	defer clientFill.Discard()

	var ran atomic.Bool
	done := c.WarmTrackInBackground(supExtractOpts(source, 0), func(context.Context, StreamExtractOpts) error {
		ran.Store(true)
		return nil
	}, "warm-skip")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("warm did not settle")
	}
	if ran.Load() {
		t.Fatal("warm extracted while the request path owned the track")
	}
	if !strings.Contains(logs.String(), "warm skipped: a fill already owns this track") {
		t.Fatalf("warm skip reason not logged: %s", logs.String())
	}
}

func TestServeSUPExtractDiscardsOnExtractError(t *testing.T) {
	c, source := newTestCache(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	wantErr := errors.New("ffmpeg exploded")
	err := c.ServeSUPExtract(rec, req, supExtractOpts(source, 0), func(_ context.Context, opts StreamExtractOpts) error {
		_, _ = opts.Writer.Write([]byte("PARTIAL"))
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v", err)
	}
	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("partial extract must not be cached")
	}
	if n := countCacheFiles(t, c); n != 0 {
		t.Fatalf("failed extract left %d files", n)
	}
}

func TestServeSUPExtractNilCacheStreams(t *testing.T) {
	var c *SubtitleCache
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		_, err := opts.Writer.Write([]byte("UNCACHED"))
		return err
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	if err := c.ServeSUPExtract(rec, req, supExtractOpts("/nonexistent.mkv", 0), extract); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "UNCACHED" {
		t.Fatalf("body = %q", rec.Body.String())
	}

	// Windowed requests on a nil cache stream too (no lookup, no warm).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=10", nil)
	if err := c.ServeSUPExtract(rec, req, windowedSupOpts("/nonexistent.mkv", 0, 10, 3600), extract); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "UNCACHED" {
		t.Fatalf("windowed body = %q", rec.Body.String())
	}
}

// entryPath computes the committed entry path for source+track.
func entryPath(t *testing.T, c *SubtitleCache, source string, track int) string {
	t.Helper()
	src, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(c.dir(), subtitleCacheFormatKey(source, track, src.ModTime(), src.Size(), "sup"))
}

// countCacheEntries counts committed .sup entries in the cache dir.
func countCacheEntries(t *testing.T, c *SubtitleCache) int {
	t.Helper()
	return countMatching(t, c, func(name string) bool {
		return strings.HasSuffix(name, ".sup") && !strings.Contains(name, ".part-")
	})
}

// countCacheFiles counts every file in the cache dir, temp files included.
func countCacheFiles(t *testing.T, c *SubtitleCache) int {
	t.Helper()
	return countMatching(t, c, func(string) bool { return true })
}

func countMatching(t *testing.T, c *SubtitleCache, match func(string) bool) int {
	t.Helper()
	entries, err := os.ReadDir(c.dir())
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if match(e.Name()) {
			n++
		}
	}
	return n
}

func TestSubtitleCacheTextRejectsSourceChangedDuringExtraction(t *testing.T) {
	cache, source := newTestCache(t)
	data, err := cache.ExtractText(t.Context(), source, 0, "srt", func(context.Context) ([]byte, error) {
		if err := os.WriteFile(source, []byte("replacement source with a different size"), 0o600); err != nil {
			return nil, err
		}
		return []byte("old source subtitle"), nil
	})
	if err != nil || string(data) != "old source subtitle" {
		t.Fatalf("current extraction: %q %v", data, err)
	}
	if _, ok := cache.LookupText(source, 0, "srt"); ok {
		t.Fatal("old extract cached under replacement source")
	}
}

// Explicit position=0 is a window (WindowRequested), so ServeExtract must
// take the windowed branch and must not run the full-track fill/serve path
// for the whole source.
func TestServeExtractExplicitZeroWindowDoesNotFullTrack(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{
		InputPath:             source,
		SourceCodec:           "ass",
		WindowRequested:       true,
		SeekSeconds:           0,
		DurationSeconds:       600,
		DisableBackgroundWarm: true,
	}
	var got StreamExtractOpts
	calls := 0
	rec := httptest.NewRecorder()
	if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/subtitle.ass?position=0&duration=600", nil), opts, func(_ context.Context, o StreamExtractOpts) error {
		calls++
		got = o
		_, err := io.WriteString(o.Writer, "[Script Info]\n")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("extract calls = %d, want 1", calls)
	}
	if got.InputIsExtractedText != "" {
		t.Fatalf("explicit-zero window must read the source, got input %q", got.InputIsExtractedText)
	}
	if !got.WindowRequested || got.SeekSeconds != 0 || got.DurationSeconds != 600 {
		t.Fatalf("window parameters not preserved: %+v", got)
	}
	if f, _, ok := c.lookup(source, "", 0, subtitleFormatASS); ok {
		_ = f.Close()
		t.Fatal("windowed extract must not commit a full-track entry")
	}
}

// ResolveCommittedTextEntry binds the exact artifact; passing the token back
// through PinnedTextArtifact makes ServeExtract use that artifact without
// re-resolving, so a generation rollover between the identity check and the
// read cannot turn a validated artifact into a miss. The pinned path here is
// one a fresh lookup cannot produce, proving the token is authoritative.
func TestPinnedTextArtifactServedWithoutReResolution(t *testing.T) {
	c, source := newTestCache(t)
	if err := os.MkdirAll(c.dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(c.dir(), "pinned-external.ass")
	if err := os.WriteFile(external, []byte("[Script Info]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.cachedFormatEntryPath(source, "", 0, subtitleFormatASS); ok {
		t.Fatal("test requires no normally resolvable entry")
	}
	token := CommittedTextArtifact{path: external, format: subtitleFormatASS}
	opts := StreamExtractOpts{
		InputPath:             source,
		SourceCodec:           "ass",
		WindowRequested:       true,
		DurationSeconds:       600,
		DisableBackgroundWarm: true,
		PinnedTextArtifact:    &token,
	}
	var got StreamExtractOpts
	rec := httptest.NewRecorder()
	result, err := c.ServeExtractWithResult(rec, httptest.NewRequest(http.MethodGet, "/subtitle.ass", nil), opts, func(_ context.Context, o StreamExtractOpts) error {
		got = o
		_, err := io.WriteString(o.Writer, assWindowCue)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ServedCommittedArtifact {
		t.Fatal("pinned artifact must be reported as served")
	}
	if got.InputPath != external || got.InputIsExtractedText != subtitleFormatASS {
		t.Fatalf("serve must use the pinned artifact: %+v", got)
	}
	if rec.Body.String() != assWindowCue {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// A committed artifact can be evicted between the identity check and the
// read. When the caller pinned it, ServeExtractWithResult must not silently
// fall back to an unvalidated source extract: it reports the miss so the
// caller can revalidate and retry.
func TestPinnedTextArtifactEvictionDoesNotFallBackToSource(t *testing.T) {
	c, source := newTestCache(t)
	fill := c.beginFill(source, "", 0, subtitleFormatASS)
	if fill == nil {
		t.Fatal("failed to reserve fill")
	}
	if _, err := fill.Tee(io.Discard).Write([]byte("[Script Info]\n")); err != nil {
		t.Fatal(err)
	}
	if err := fill.Commit(); err != nil {
		t.Fatal(err)
	}
	token, ok := c.ResolveCommittedTextEntry(source, "", 0, "ass", "")
	if !ok {
		t.Fatal("expected a committed artifact to resolve")
	}
	if err := os.Remove(token.path); err != nil {
		t.Fatal(err)
	}
	extracted := false
	opts := StreamExtractOpts{
		InputPath:             source,
		SourceCodec:           "ass",
		WindowRequested:       true,
		DurationSeconds:       600,
		DisableBackgroundWarm: true,
		PinnedTextArtifact:    &token,
	}
	result, err := c.ServeExtractWithResult(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle.ass", nil), opts, func(context.Context, StreamExtractOpts) error {
		extracted = true
		return nil
	})
	if !errors.Is(err, ErrCommittedTextArtifactGone) {
		t.Fatalf("error = %v, want ErrCommittedTextArtifactGone", err)
	}
	if extracted {
		t.Fatal("evicted pinned artifact must not fall back to a source extract")
	}
	if result.ServedCommittedArtifact {
		t.Fatal("no artifact was served")
	}
}

// ServeExtractWithResult reports a windowed serve that used the committed
// artifact, and a window miss that did not — the signal a caller uses to
// decide whether track-identity validation can be skipped.
func TestServeExtractResultReportsCommittedArtifact(t *testing.T) {
	c, source := newTestCache(t)

	// Cold window miss: nothing committed, so the artifact was not served.
	windowed := StreamExtractOpts{
		InputPath:             source,
		SourceCodec:           "ass",
		TrackIndex:            0,
		WindowRequested:       true,
		DurationSeconds:       600,
		DisableBackgroundWarm: true,
	}
	result, err := c.ServeExtractWithResult(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle.ass", nil), windowed, func(_ context.Context, o StreamExtractOpts) error {
		_, err := io.WriteString(o.Writer, assWindowCue)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ServedCommittedArtifact {
		t.Fatal("cold window miss must not report a committed artifact")
	}

	// Commit a full-track artifact, then serve a window from it.
	full := StreamExtractOpts{InputPath: source, SourceCodec: "ass", TrackIndex: 0}
	if err := c.ServeExtract(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle.ass", nil), full, func(_ context.Context, o StreamExtractOpts) error {
		_, err := io.WriteString(o.Writer, "FULL TRACK")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A different window avoids the exact last-good slice cached by the cold
	// miss above, so this serve exercises the committed-artifact input path.
	postCommit := windowed
	postCommit.SeekSeconds = 30
	var served StreamExtractOpts
	result, err = c.ServeExtractWithResult(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/subtitle.ass", nil), postCommit, func(_ context.Context, o StreamExtractOpts) error {
		served = o
		_, err := io.WriteString(o.Writer, assWindowCue)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ServedCommittedArtifact {
		t.Fatal("windowed serve from a committed artifact must report it")
	}
	if served.InputIsExtractedText != subtitleFormatASS {
		t.Fatalf("windowed serve must read the cached artifact: %+v", served)
	}
}

// A warm that fails must put its identity in a cooldown so repeated windowed
// misses do not each spawn a full-track remote read. An immediate retry is
// suppressed; once the cooldown expires the next attempt runs.
func TestWarmTrackInBackgroundFailedWarmCooldown(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{InputPath: source, SourceCodec: "ass", TrackIndex: 0}

	var (
		mu    sync.Mutex
		calls int
	)
	extract := func(_ context.Context, _ StreamExtractOpts) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return errors.New("relay unavailable")
	}
	select {
	case <-c.WarmTrackInBackground(opts, extract, ""):
	case <-time.After(5 * time.Second):
		t.Fatal("first warm did not finish")
	}
	mu.Lock()
	if calls != 1 {
		t.Fatalf("first warm calls = %d, want 1", calls)
	}
	mu.Unlock()

	// Immediate retry: suppressed by the failure cooldown.
	select {
	case <-c.WarmTrackInBackground(opts, extract, ""):
	case <-time.After(5 * time.Second):
		t.Fatal("suppressed retry warm did not finish")
	}
	mu.Lock()
	if calls != 1 {
		t.Fatalf("immediate retry calls = %d, want still 1 (cooldown)", calls)
	}
	mu.Unlock()

	// Expire the cooldown; the next attempt runs.
	key, ok := c.formatEntryKey(source, "", 0, subtitleFormatASS)
	if !ok {
		t.Fatal("failed to compute warm key")
	}
	c.warmMu.Lock()
	guard := c.warmGuard[key]
	guard.retryAt = time.Now().Add(-time.Second)
	c.warmGuard[key] = guard
	c.warmMu.Unlock()
	select {
	case <-c.WarmTrackInBackground(opts, extract, ""):
	case <-time.After(5 * time.Second):
		t.Fatal("post-cooldown warm did not finish")
	}
	mu.Lock()
	if calls != 2 {
		t.Fatalf("post-cooldown warm calls = %d, want 2", calls)
	}
	mu.Unlock()
}

// A successful warm clears the identity's cooldown.
func TestWarmOutcomeSuccessClearsCooldown(t *testing.T) {
	c, _ := newTestCache(t)
	const key = "identity"
	c.noteWarmOutcome(key, false, time.Now())
	if c.warmAdmitted(key, time.Now()) {
		t.Fatal("failed warm must gate admission")
	}
	c.noteWarmOutcome(key, true, time.Now())
	if !c.warmAdmitted(key, time.Now()) {
		t.Fatal("successful warm must clear the cooldown")
	}
}

// A canceled warm (relay gone) must still close its done channel and release
// its warm slot, so the caller's deferred relay-registration cleanup runs and
// no slot leaks. The cancellation is recorded as a failure, gating an
// immediate re-spawn.
func TestWarmTrackInBackgroundCancellationReleasesSlot(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{
		InputPath:     source,
		SourceCodec:   "ass",
		TrackIndex:    0,
		CacheIdentity: "virtual-cancel-1",
	}
	done := c.WarmTrackInBackground(opts, func(context.Context, StreamExtractOpts) error {
		return context.Canceled
	}, "")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("warm done never closed after cancellation")
	}
	// Every warm slot is free again.
	for i := 0; i < subtitleCacheWarmSlots; i++ {
		select {
		case c.warmSem <- struct{}{}:
		default:
			t.Fatal("warm slot leaked after cancellation")
		}
	}
	for i := 0; i < subtitleCacheWarmSlots; i++ {
		<-c.warmSem
	}
	// The canceled warm counts as a failure, so an immediate retry is gated.
	calls := 0
	select {
	case <-c.WarmTrackInBackground(opts, func(context.Context, StreamExtractOpts) error {
		calls++
		return nil
	}, ""):
	case <-time.After(5 * time.Second):
		t.Fatal("gated retry warm did not finish")
	}
	if calls != 0 {
		t.Fatalf("immediate retry after cancellation ran %d times, want 0", calls)
	}
}

// An admitted page extract on the same track owns the shared relay pipe, so an
// in-flight full-track warm must stop, wait for the page, and resume. The yield
// is intentional and must not arm the failed-warm cooldown.
func TestWarmTrackInBackgroundYieldsToAdmittedPage(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{
		InputPath:     source,
		SourceCodec:   "ass",
		TrackIndex:    0,
		CacheIdentity: "virtual-page-yield",
	}
	format := subtitleCacheFormat(opts.SourceCodec, opts.TargetFormat)

	var calls atomic.Int32
	started := make(chan int32, 4)
	canceled := make(chan struct{})
	extract := func(ctx context.Context, extractOpts StreamExtractOpts) error {
		n := calls.Add(1)
		started <- n
		if n == 1 {
			<-ctx.Done()
			close(canceled)
			return ctx.Err()
		}
		_, err := io.WriteString(extractOpts.Writer, "complete ass track")
		return err
	}

	done := c.WarmTrackInBackground(opts, extract, "")
	select {
	case n := <-started:
		if n != 1 {
			t.Fatalf("first warm extract call = %d, want 1", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("warm never started extracting")
	}

	// The page is admitted while the warm is reading. That must cancel the warm
	// rather than let both demux the same source.
	endPage := c.beginPageFlight(opts, format)
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("admitted page did not preempt the running warm")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("warm restarted while the page was still admitted: calls = %d", got)
	}

	// Once the page settles the warm resumes and commits.
	endPage()
	select {
	case n := <-started:
		if n != 2 {
			t.Fatalf("restarted warm call = %d, want 2", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("warm did not resume after the page settled")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("warm never settled after resuming")
	}
	if !c.HasCommittedTextEntry(source, opts.CacheIdentity, 0, "ass", "") {
		t.Fatal("resumed warm did not commit the track")
	}
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		t.Fatal("warm key not derivable")
	}
	if !c.warmAdmitted(key, time.Now()) {
		t.Fatal("yielding to a page armed the failed-warm cooldown")
	}
}

// A page already admitted when the warm starts keeps it out of the relay pipe
// entirely: the warm waits for the page and then extracts once.
func TestWarmTrackInBackgroundWaitsForAdmittedPageAtStart(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{
		InputPath:     source,
		SourceCodec:   "ass",
		TrackIndex:    0,
		CacheIdentity: "virtual-page-start",
	}
	format := subtitleCacheFormat(opts.SourceCodec, opts.TargetFormat)
	endPage := c.beginPageFlight(opts, format)

	var calls atomic.Int32
	started := make(chan struct{}, 2)
	extract := func(_ context.Context, extractOpts StreamExtractOpts) error {
		calls.Add(1)
		started <- struct{}{}
		_, err := io.WriteString(extractOpts.Writer, "complete ass track")
		return err
	}

	done := c.WarmTrackInBackground(opts, extract, "")
	// No extract may run while the page holds the track. The warm goroutine is
	// parked in its settle wait, so a non-blocking channel read is decisive.
	select {
	case <-started:
		t.Fatal("warm extracted while a page was already admitted")
	default:
	}
	endPage()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("warm never extracted after the page settled")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("warm never settled")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("warm extract calls = %d, want 1", got)
	}
	if !c.HasCommittedTextEntry(source, opts.CacheIdentity, 0, "ass", "") {
		t.Fatal("warm did not commit after waiting for the page")
	}
}

// A page that preempts an ASS warm while the track key has been taken over by
// another fill must end the warm cleanly. Losing the re-reserve leaves the
// retained prefix and its progress entry behind unless the deferred cleanup
// still owns them, and a stale entry would let a later window render from a
// prefix no warm is filling.
func TestWarmTrackInBackgroundLostReserveReleasesASSPrefix(t *testing.T) {
	c, source := newTestCache(t)
	opts := StreamExtractOpts{
		InputPath:     source,
		SourceCodec:   "ass",
		TrackIndex:    0,
		CacheIdentity: "virtual-lost-reserve",
	}
	format := subtitleCacheFormat(opts.SourceCodec, opts.TargetFormat)
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		t.Fatal("warm key not derivable")
	}

	const prefix = "[Script Info]\nScriptType: v4.00+\n\n[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,hello\n" //nolint:misspell // ASS/SSA event keyword

	var calls atomic.Int32
	started := make(chan struct{})
	extract := func(ctx context.Context, extractOpts StreamExtractOpts) error {
		calls.Add(1)
		if _, err := io.WriteString(extractOpts.Writer, prefix); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		// Stand in for a competing fill that took the key: the warm's own
		// Discard only releases an entry it still owns, so the re-reserve
		// after it reads as busy and the warm must stop.
		c.mu.Lock()
		c.inflight[key] = &SubtitleCacheFill{key: key}
		c.mu.Unlock()
		return ctx.Err()
	}

	done := c.WarmTrackInBackground(opts, extract, "")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("warm never started extracting")
	}
	c.warmTextMu.Lock()
	progress := c.warmText[key]
	c.warmTextMu.Unlock()
	if progress == nil {
		t.Fatal("warm did not register ASS prefix progress")
	}

	endPage := c.beginPageFlight(opts, format)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("warm never settled after losing the re-reserve")
	}
	endPage()

	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()

	if got := calls.Load(); got != 1 {
		t.Fatalf("warm extract calls = %d, want 1", got)
	}
	c.warmTextMu.Lock()
	leftover := c.warmText[key]
	c.warmTextMu.Unlock()
	if leftover != nil {
		t.Fatal("warm left stale ASS progress registered after losing the re-reserve")
	}
	progress.mu.Lock()
	headerLen, retained := progress.header.Len(), progress.retainedBytes
	progress.mu.Unlock()
	if headerLen != 0 || retained != 0 {
		t.Fatalf("warm prefix not released: header=%d bytes=%d", headerLen, retained)
	}
	if c.HasCommittedTextEntry(source, opts.CacheIdentity, 0, "ass", "") {
		t.Fatal("aborted warm committed a track")
	}
}

// textWindowCue is a WebVTT window payload that carries a cue. Only a document
// that can carry a cue is eligible for the last-good window cache, so window
// fixtures use a real one.
const textWindowCue = "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nwindow cue\n"

// assWindowCue is the ASS equivalent: a script header plus one event.
const assWindowCue = "[Script Info]\nScriptType: v4.00+\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:00.00,0:00:02.00,Default,window cue\n" //nolint:misspell // the ASS event keyword

// pgsWindowPage is a .sup page holding one complete display set.
var pgsWindowPage = append(supSegment(supSegmentPCS, []byte{0, 1, 2, 3}), supSegment(supSegmentEND, nil)...)

// supSegment builds one PGS segment for a synthetic .sup payload.
func supSegment(kind byte, payload []byte) []byte {
	seg := make([]byte, supSegmentHeaderLen+len(payload))
	seg[0], seg[1] = 'P', 'G'
	seg[10] = kind
	binary.BigEndian.PutUint16(seg[11:13], uint16(len(payload)))
	copy(seg[supSegmentHeaderLen:], payload)
	return seg
}

// A windowed extraction can exit 0 having written only the format header (an
// FFmpeg that read nothing of the window, or an empty track). That payload must
// never become the last-good copy: every later request inside the range would
// be answered from it with a document that cannot carry a cue.
func TestServeExtractWindowWithoutCueIsNotKeptAsLastGood(t *testing.T) {
	c, source := newTestCache(t)
	extractCalls := 0
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		extractCalls++
		_, err := io.WriteString(opts.Writer, "WEBVTT\n\n")
		return err
	}
	// DisableBackgroundWarm keeps a detached full-track fill out of the
	// extractor's call count; a virtual-source serve sets it for the same
	// reason.
	opts := StreamExtractOpts{
		InputPath:             source,
		TrackIndex:            0,
		SourceCodec:           "subrip",
		SeekSeconds:           0,
		DurationSeconds:       120,
		WindowRequested:       true,
		DisableBackgroundWarm: true,
	}
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		if err := c.ServeExtract(rec, httptest.NewRequest(http.MethodGet, "/sub.vtt", nil), opts, extract); err != nil {
			t.Fatalf("serve %d: %v", attempt, err)
		}
		if got := rec.Body.String(); got != "WEBVTT\n\n" {
			t.Fatalf("serve %d body = %q, want the cue-less document", attempt, got)
		}
		if extractCalls != attempt {
			t.Fatalf("serve %d: extract calls = %d, want %d: a cue-less window was served from the last-good cache", attempt, extractCalls, attempt)
		}
	}
}

// A .sup page that ends inside a display set (no END after its PCS) is a slice
// of a page, not a page: caching it answers every later request in the range
// with bytes no decoder can paint.
func TestServeSUPExtractWindowWithoutDisplaySetIsNotKeptAsLastGood(t *testing.T) {
	c, source := newTestCache(t)
	partial := append(supSegment(supSegmentPCS, []byte{0, 1, 2, 3}), supSegment(0x17, []byte{0, 0})...)
	extractCalls := 0
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		extractCalls++
		_, err := opts.Writer.Write(partial)
		return err
	}
	opts := windowedSupOpts(source, 0, 0, 120)
	opts.DisableBackgroundWarm = true
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=0&duration=120", nil)
		if err := c.ServeSUPExtract(rec, req, opts, extract); err != nil {
			t.Fatalf("serve %d: %v", attempt, err)
		}
		if !bytes.Equal(rec.Body.Bytes(), partial) {
			t.Fatalf("serve %d body = %d bytes, want the extracted slice", attempt, rec.Body.Len())
		}
		if extractCalls != attempt {
			t.Fatalf("serve %d: extract calls = %d, want %d: a page with no display set was served from the last-good cache", attempt, extractCalls, attempt)
		}
	}
}

// The positive control: a page holding a complete display set is still kept, so
// the rule above narrows what is cached without disabling the last-good cache.
func TestServeSUPExtractWindowWithDisplaySetIsKeptAsLastGood(t *testing.T) {
	c, source := newTestCache(t)
	page := append(supSegment(supSegmentPCS, []byte{0, 1, 2, 3}), supSegment(supSegmentEND, nil)...)
	extractCalls := 0
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		extractCalls++
		_, err := opts.Writer.Write(page)
		return err
	}
	opts := windowedSupOpts(source, 0, 0, 120)
	opts.DisableBackgroundWarm = true
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=0&duration=120", nil)
		if err := c.ServeSUPExtract(rec, req, opts, extract); err != nil {
			t.Fatalf("serve %d: %v", attempt, err)
		}
		if !bytes.Equal(rec.Body.Bytes(), page) {
			t.Fatalf("serve %d body = %d bytes, want the page", attempt, rec.Body.Len())
		}
	}
	if extractCalls != 1 {
		t.Fatalf("extract calls = %d, want 1: a complete page must be kept and re-served", extractCalls)
	}
}
