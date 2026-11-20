package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A deep bounded bitmap page is extracted, not refused. The request carries its
// own window, and the extract seeks to it: a 120 s page at 3000 s of a 102 MB
// cued MKV read 13.0 MB in total, where the prefix scan the old gate priced read
// 93.9 MB. Production answered such requests `subtitle_page_too_costly` in
// 6-12 ms, 27 times over six minutes, without reading a byte.
func TestVirtualDeepBitmapPageSeeksItsWindowInsteadOfBeingRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	// An 18 GB release that runs two hours reads ~2.5 MB/s: the page at 3600 s
	// would discard about 9 GB of container if it could not seek, and its own
	// window is 300 MB.
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

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the page (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the page produced no bytes")
	}
	// The extract that produced it seeks to the page and keeps the window on the
	// source timeline, which is the behavior the cost gate now prices.
	if got := countArgsMatching(t, argsLog, "-ss 3600.000", "-copyts"); got != 1 {
		t.Fatalf("windowed extracts with the page seek = %d, want 1", got)
	}
	if _, warm := ffmpegLogLines(t, argsLog); warm != 0 {
		t.Fatalf("full-track fills = %d, want none: the page did not need the artifact", warm)
	}
}

// A live layout that differs from the plan only in metadata is not a rotation.
// The provider listing says French and the container carries no language tag
// (`und`). Extraction addresses the ordinal, so the page at that ordinal is still
// the subtitles the plan promised, and a successful extract must not be thrown
// away because a later probe compares tags.
func TestVirtualMetadataOnlyLayoutDriftDoesNotWithholdTheTrack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	gate := filepath.Join(dir, "warm-gate")
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, warmArgsLogScript(argsLog, gate))
	file.FileSize = 18 << 30
	file.Duration = 7200
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "fr"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	session.Position = 3600
	writeSubtitleLayoutProbe(t, dir, models.SubtitleTrack{Index: 0, Codec: "subrip", Language: "und"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?windowed=1&position=3600&duration=120", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("page after metadata drift = %d %q, want the track served", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the page produced no bytes")
	}
	// The successful extract left no rotation verdict behind: the same track's
	// next page is not answered as if the plan were stale.
	if handler.virtualSubtitleLayoutUnmatchable(&playback.StreamExtractOpts{
		CacheIdentity: virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, 0),
		TrackIndex:    0,
	}, time.Now()) {
		t.Fatal("a metadata-only difference left a rotation verdict behind")
	}
}

// planOrdinalStillCarries is the narrow fallback: it carries a same-class track
// at the plan ordinal when the live layout cannot contradict the plan, and
// refuses everything that is a real rotation.
func TestPlanOrdinalFallbackCarriesOnlyMetadataDrift(t *testing.T) {
	plan := models.SubtitleTrack{Index: 0, Codec: "subrip", Language: "fr"}
	cases := []struct {
		name    string
		live    []models.SubtitleTrack
		ordinal int
		want    bool
	}{
		{"no language tag in the container", []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "und"}}, 0, true},
		{"language absent from the container", []models.SubtitleTrack{{Index: 0, Codec: "subrip"}}, 0, true},
		{"a different named language", []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "en"}}, 0, false},
		{"a different class", []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "und"}}, 0, false},
		{"the plan ordinal is gone", []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "und"}}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := planOrdinalStillCarries(plan, tc.live, tc.ordinal, ""); ok != tc.want {
				t.Fatalf("carried = %v, want %v", ok, tc.want)
			}
		})
	}
}

// A warm that positively finds no carryable track for the plan ordinal answers
// the next page retryably, never with the 409 that tells the client to replan.
// The verdict is our background fill's opinion; production turned it into seven
// 409s after a successful page and left the viewer with subtitles off and no way
// back, because the replan re-derived the same plan.
func TestWarmLayoutVerdictAnswersRetryablyNotReplanned(t *testing.T) {
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
	session.IsPaused = true
	session.Position = 3600
	// The live release carries a different class entirely: no plan track can be
	// carried, so the warm records a positive rotation verdict.
	writeSubtitleLayoutProbe(t, dir, models.SubtitleTrack{Index: 3, Codec: "subrip", Language: "en"})

	opts := playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, 0),
		TrackIndex:      0,
		SourceCodec:     "hdmv_pgs_subtitle",
		SeekSeconds:     3600,
		DurationSeconds: 120,
		AllowWindow:     true,
		WindowRequested: true,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}
	handler.warmVirtualSubtitleForWaitingPage(file, file.SubtitleTracks[0], session, opts, nil, true, "")
	handler.waitForBackgroundSubtitleWarms()
	// The state transition the test reads: the fill recorded the verdict.
	if !handler.virtualSubtitleLayoutUnmatchable(&opts, time.Now()) {
		t.Fatal("the warm recorded no layout verdict for the page to answer from")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?windowed=1&position=3600&duration=120", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("page from the warm verdict = %d %q, want the retryable 503", rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusConflict {
		t.Fatal("a background verdict answered the client's plan as stale")
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("the retryable refusal carries no Retry-After")
	}
	if !strings.Contains(rec.Body.String(), `"error":"subtitle_source_unavailable"`) {
		t.Fatalf("body = %q, want the source-unavailable problem code", rec.Body.String())
	}
	if window, warm := ffmpegLogLines(t, argsLog); window != 0 || warm != 0 {
		t.Fatalf("extractions = %d windowed, %d whole-track, want none", window, warm)
	}
}

// A probe that fails (provider timeout, relay error) says nothing about the
// release. It must not leave a rotation verdict behind: the next page is still
// served, because a failed probe is not evidence about the bytes.
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
	session.IsPaused = true
	session.Position = 3600
	// The probe itself fails rather than reporting a layout.
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\nexit 1\n")

	opts := playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, 0),
		TrackIndex:      0,
		SourceCodec:     "hdmv_pgs_subtitle",
		SeekSeconds:     3600,
		DurationSeconds: 120,
		AllowWindow:     true,
		WindowRequested: true,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}
	handler.warmVirtualSubtitleForWaitingPage(file, file.SubtitleTracks[0], session, opts, nil, true, "")
	handler.waitForBackgroundSubtitleWarms()
	if handler.virtualSubtitleLayoutUnmatchable(&opts, time.Now()) {
		t.Fatal("a failed probe left a rotation verdict behind")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?windowed=1&position=3600&duration=120", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("page after a failed probe = %d %q, want the page", rec.Code, rec.Body.String())
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
