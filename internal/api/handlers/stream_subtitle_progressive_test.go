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
