package handlers

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const budgetRecoveryASS = "[Script Info]\nScriptType: v4.00+\n\n" +
	"[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
	"Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1\n\n" +
	"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
	"Dialogue: 0,0:09:40.00,0:09:42.00,Default,,0,0,0,,hello\n" //nolint:misspell // ASS event keyword

// A text/ASS window killed at its request budget answers the retryable 503 and
// starts a detached recovery warm that covers the range the client retries.
// The retry must be served from that warm with no second extract: the
// playhead-grid prefetch this path used before started earlier with the same
// duration, so its range ended before the shifted retry's end and the covering
// lookup rejected it. Production repeated the 20 s read three times and then
// stopped rendering on the track ("Discovering…" permanently).
func TestSubtitleWindowBudgetBreachRetryServedFromRecoveryWarm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	// A request-path window never finishes inside its budget; the detached warm
	// (the same -ss shape, so the range is observable in the args log) does.
	// If the warm were bounded by the request budget too, it would be killed
	// here and nothing would be committed.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argsLog + "'\n" +
		"case \"$*\" in\n" +
		"  *-ss*)\n    sleep 0.8\n    ;;\n" +
		"esac\n" +
		"cat <<'ASS'\n" + budgetRecoveryASS + "ASS\n"

	warmLogs := &warmLogBuffer{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(warmLogs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, script)
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "ass"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)
	// Mid-file: the ordinary warm shape here is the window prefetch, which is
	// exactly the path that missed the shifted retry.
	session.IsPaused = true
	session.Position = 600
	handler.SubtitleCache.SetWindowExtractBudgetForTest(150 * time.Millisecond)

	serve := func(position int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/subtitle?position="+strconv.Itoa(position)+"&duration=120", nil)
		rec := httptest.NewRecorder()
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
		return rec
	}

	rec := serve(580)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("breached window = %d %q, want 503", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "Dialogue:") { //nolint:misspell // ASS event keyword
		t.Fatalf("breached window body = %q, want no partial slice", body)
	}
	// The recovery warm must run to completion despite the budget breach that
	// armed the identity's cooldown.
	handler.waitForBackgroundSubtitleWarms()

	if killed := warmLogs.count("subtitle window extract killed at its budget"); killed != 1 {
		t.Fatalf("budget kills = %d, want exactly 1 (the request; the warm is detached)\n%s", killed, warmLogs.String())
	}
	// The warm spans the breached window plus one full client window: the client
	// walks on the live playhead while the warm builds, so a one-cell lead ends
	// before the retries it exists to serve do (production: a 64.5 s build for
	// 1680-1830 while the retries asked for 1740-1860 and 1800-1920).
	assertFFmpegArgs(t, argsLog, "-ss 580", "-to 820")

	// The client retries on the advanced playhead, up to one full window past
	// the breached start. Every one of those windows is inside the warm and must
	// be served from it with no new source read.
	for _, position := range []int{590, 640, 700} {
		if got := countFFmpegArgs(t, argsLog, "-ss "+strconv.Itoa(position)); got != 0 {
			t.Fatalf("retry window %d extracts before the retry = %d, want 0", position, got)
		}
		rec = serve(position)
		if rec.Code != http.StatusOK {
			t.Fatalf("retry %d = %d %q, want 200 served from the recovery warm", position, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Dialogue:") { //nolint:misspell // ASS event keyword
			t.Fatalf("retry %d body = %q, want the ASS window", position, rec.Body.String())
		}
		if got := countFFmpegArgs(t, argsLog, "-ss "+strconv.Itoa(position)); got != 0 {
			t.Fatalf("retry window %d extracts = %d, want 0: the retry must read the recovery warm, not the source", position, got)
		}
	}
}

// A window that has already produced cues when its budget expires is the case
// production hit: the held slice is dropped (nothing reaches the client), but
// the extract had written bytes, which used to make the cache return the raw
// deadline error instead of the budget shape. The handler then answered a
// generic subtitle_source_unavailable 503 and started no warm, so every retry
// paid the same 20 s read and the switch ended in "Subtitles couldn't load".
// The answer must be the retryable budget reason that starts the covering warm,
// with no partial slice on the wire.
func TestSubtitleWindowBudgetBreachAfterHeldBytesStartsTheRecoveryWarm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	// Every extract emits a complete-looking slice and then stalls. The
	// request-path window is killed at its budget after those bytes were
	// written; the detached warm has no request-path budget, so it sleeps
	// inside its own timeout and then completes.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argsLog + "'\n" +
		"cat <<'ASS'\n" + budgetRecoveryASS + "ASS\n" +
		"sleep 0.8\n"

	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, script)
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "ass"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)
	session.IsPaused = true
	session.Position = 600
	handler.SubtitleCache.SetWindowExtractBudgetForTest(150 * time.Millisecond)

	serve := func(position int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/subtitle?position="+strconv.Itoa(position)+"&duration=120", nil)
		rec := httptest.NewRecorder()
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
		return rec
	}

	rec := serve(580)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("breached window = %d %q, want 503", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), playback.SubtitleWindowBudgetExceededReason) {
		t.Fatalf("breached window body = %q, want the budget reason (not a source verdict)", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Dialogue:") { //nolint:misspell // ASS event keyword
		t.Fatalf("breached window body = %q, want no partial slice", rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("breached window carries no Retry-After")
	}

	handler.waitForBackgroundSubtitleWarms()

	// The retry a few seconds forward is served from the covering warm: no
	// second source read for the range it covers.
	if got := countFFmpegArgs(t, argsLog, "-ss 590"); got != 0 {
		t.Fatalf("retry window extracts before the retry = %d, want 0", got)
	}
	rec = serve(590)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry = %d %q, want 200 served from the recovery warm", rec.Code, rec.Body.String())
	}
	if got := countFFmpegArgs(t, argsLog, "-ss 590"); got != 0 {
		t.Fatalf("retry window extracts = %d, want 0: the retry must read the recovery warm, not the source", got)
	}
}

// A fast window is unaffected: it streams directly, commits its last-good
// slice, and never enters the breach path.
func TestSubtitleWindowBudgetFastWindowServesDirectly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argsLog + "'\n" +
		"cat <<'ASS'\n" + budgetRecoveryASS + "ASS\n"
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, script)
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "ass"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)
	session.IsPaused = true
	session.Position = 600
	handler.SubtitleCache.SetWindowExtractBudgetForTest(150 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=580&duration=120", nil)
	rec := httptest.NewRecorder()
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("fast window = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Dialogue:") { //nolint:misspell // ASS event keyword
		t.Fatalf("fast window body = %q, want the ASS window", rec.Body.String())
	}
	assertFFmpegArgs(t, argsLog, "-ss 580")
}

func assertFFmpegArgs(t *testing.T, argsLog string, wants ...string) {
	t.Helper()
	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, want := range wants {
		found := false
		for _, line := range lines {
			if strings.Contains(line, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ffmpeg args missing %q:\n%s", want, data)
		}
	}
}

func countFFmpegArgs(t *testing.T, argsLog, needle string) int {
	t.Helper()
	data, err := os.ReadFile(argsLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, needle) {
			count++
		}
	}
	return count
}
