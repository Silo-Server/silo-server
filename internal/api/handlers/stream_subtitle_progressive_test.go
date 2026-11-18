package handlers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A page far into a large relay source is refused before any source read: the
// container prefix it would have to discard is gigabytes, and reading it is the
// production wall clock (88 s per page) this gate removes. The refusal is the
// retryable 503 the client backs off from, and it is what starts the track's
// progressive fill.
func TestVirtualExpensiveBitmapPageIsRefusedBeforeTheSourceRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	// An 18 GB release that runs two hours reads ~2.5 MB/s, so page 3600 would
	// discard about 9 GB of container before reaching its own window.
	file.FileSize = 18 << 30
	file.Duration = 7200
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "fr"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	session.IsPaused = false
	session.Position = 3600
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?windowed=1&position=3600&duration=120", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the retryable 503 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"`+playback.SubtitlePageTooCostlyReason+`"`) {
		t.Fatalf("body = %q, want the page-cost problem code", rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("a refused page carries no Retry-After")
	}
	// The page never read the source; the fill that will serve it did.
	if got := countArgsMatching(t, argsLog, "-ss 3600"); got != 0 {
		t.Fatalf("the refused page was extracted %d times", got)
	}
	handler.waitForBackgroundSubtitleWarms()
	if _, warm := ffmpegLogLines(t, argsLog); warm != 1 {
		t.Fatalf("progressive fills = %d, want the one that will serve the page", warm)
	}
}

// A pinned release can rotate under a row whose stored evidence still matches:
// the page path trusts the row and never probes, so every page is refused and
// starts a warm whose own probe then finds no same-class track and abandons.
// Nothing used to record that positive verdict, so the loop repeated with no
// failure and no progress. The warm records it, and the next page is answered
// with the rotation verdict the client replans from — without starting another
// warm.
func TestVirtualRotatedBitmapLayoutAnswersTheNextPageFromTheWarmVerdict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	// The row still says French PGS; the live release rotated and now carries
	// PGS in English only, so no plan track can be carried.
	file.FileSize = 18 << 30
	file.Duration = 7200
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "fr"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	session.IsPaused = false
	session.Position = 3600
	writeSubtitleLayoutProbe(t, dir, models.SubtitleTrack{Index: 3, Codec: "hdmv_pgs_subtitle", Language: "en"})

	first := httptest.NewRecorder()
	handler.streamEmbeddedSubtitle(first, httptest.NewRequest(http.MethodGet,
		"/subtitle?windowed=1&position=3600&duration=120", nil), file, 0, session, false)
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first page status = %d, want the retryable 503 (body %q)", first.Code, first.Body.String())
	}
	handler.waitForBackgroundSubtitleWarms()

	second := httptest.NewRecorder()
	handler.streamEmbeddedSubtitle(second, httptest.NewRequest(http.MethodGet,
		"/subtitle?windowed=1&position=3600&duration=120", nil), file, 0, session, false)
	if second.Code != http.StatusConflict {
		t.Fatalf("second page status = %d, want 409 for the rotation verdict (body %q)", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), `"error":"subtitle_source_changed"`) {
		t.Fatalf("second page body = %q, want the rotation verdict", second.Body.String())
	}
	if got := second.Header().Get("Retry-After"); got == "" {
		t.Fatal("the rotation verdict carries no Retry-After")
	}
	// The answered page never reached the source, and it did not start the warm
	// that would only abandon again.
	if _, warm := ffmpegLogLines(t, argsLog); warm != 0 {
		t.Fatalf("extractions = %d, want none: the probe found no carryable track", warm)
	}
}

// A probe that fails (provider timeout, relay error) says nothing about the
// release. It must not leave a rotation verdict behind: the next page keeps its
// retryable answer so the request can still recover once the probe can answer.
func TestVirtualBitmapLayoutProbeErrorRecordsNoRotationVerdict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	file.FileSize = 18 << 30
	file.Duration = 7200
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "fr"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	session.IsPaused = false
	session.Position = 3600
	// The probe itself fails rather than reporting a layout.
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\nexit 1\n")

	first := httptest.NewRecorder()
	handler.streamEmbeddedSubtitle(first, httptest.NewRequest(http.MethodGet,
		"/subtitle?windowed=1&position=3600&duration=120", nil), file, 0, session, false)
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first page status = %d, want 503 (body %q)", first.Code, first.Body.String())
	}
	handler.waitForBackgroundSubtitleWarms()

	second := httptest.NewRecorder()
	handler.streamEmbeddedSubtitle(second, httptest.NewRequest(http.MethodGet,
		"/subtitle?windowed=1&position=3600&duration=120", nil), file, 0, session, false)
	if second.Code == http.StatusConflict {
		t.Fatalf("a failed probe left a rotation verdict behind (body %q)", second.Body.String())
	}
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second page status = %d, want the retryable 503 (body %q)", second.Code, second.Body.String())
	}
}

// A bitmap page that could not be cut from the source at an affordable read cost
// is waiting for the track's progressive artifact, and that artifact only exists
// if the whole-track fill runs. This is the one case where a bitmap fill is
// started while the viewer is playing: the bounded page prefetch would re-read
// the same source for pages the fill is already publishing.
func TestVirtualWaitingBitmapPageWarmsWholeTrackWhilePlaying(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\nprintf 'PG'\n")
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	session.IsPaused = false
	// A known, large container is what a page cannot be extracted from
	// cheaply; the whole-track fill is the page path for it.
	file.FileSize = virtualSubtitleImplicitWindowMinSourceBytes
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)

	handler.warmVirtualSubtitleForWaitingPage(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		SourceSizeBytes: file.FileSize,
		TrackIndex:      0,
		SourceCodec:     "hdmv_pgs_subtitle",
		SeekSeconds:     600,
		DurationSeconds: 120,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, nil, true, "")

	waitForWarmInvocation(t, argsLog)
	window, warm := ffmpegLogLines(t, argsLog)
	if warm != 1 {
		t.Fatalf("full-track fills = %d, want exactly 1", warm)
	}
	if window != 0 {
		t.Fatalf("bounded page extracts = %d, want none: the fill is the page path", window)
	}
	waitForCommittedEntry(t, handler.SubtitleCache, identity, 0, "hdmv_pgs_subtitle")
}
