package handlers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A request-path window that reads its relay for its whole budget must answer
// the retryable 503 with a Retry-After inside that budget, and start the
// detached warm so the retry the 503 asks for reads a cached slice instead of
// repeating the read. Before the budget existed this request waited out the
// two-minute window extract timeout while the client's own 30 s stall deadline
// expired first, so the client got no answer at all.
func TestSubtitleWindowBudgetAnswersRetryableAndWarms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	// The request window (-to 1200) never reaches its range; the recovery warm
	// extracts the same start plus one full client window (position 600,
	// duration 600, +120 s = -to 1320) and commits promptly, so the breach's
	// warm is observable.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argsLog + "'\n" +
		"case \"$*\" in\n" +
		"  *'-to 1200.000'*)\n    sleep 60\n    ;;\n" +
		"  *)\n    cat <<'VTT'\n" + warmSubtitleVTT + "VTT\n    ;;\n" +
		"esac\n"
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, script)
	handler.SubtitleCache.SetWindowExtractBudgetForTest(150 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=600&duration=600", nil)
	start := time.Now()
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%q, want 503", rec.Code, rec.Body.String())
	}
	retryAfter, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retryAfter < 1 {
		t.Fatalf("Retry-After = %q, want a positive delay", rec.Header().Get("Retry-After"))
	}
	// The 503 is a cost refusal, not a source failure: its body names the
	// budget so a client and an operator can tell them apart, and it never
	// carries a partial slice.
	if body := rec.Body.String(); !strings.Contains(body, playback.SubtitleWindowBudgetExceededReason) {
		t.Fatalf("body = %q, want the %s problem", body, playback.SubtitleWindowBudgetExceededReason)
	} else if strings.Contains(body, "WEBVTT") {
		t.Fatalf("body = %q, want no partial subtitle slice", body)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("answered after %s, want the configured budget", elapsed)
	}
	if rec.Header().Get("X-Subtitle-Coverage") != "" {
		t.Fatalf("coverage header = %q, want none on a 503", rec.Header().Get("X-Subtitle-Coverage"))
	}

	// The breach started the detached recovery warm for the range the retries
	// ask for: the same start plus one full client window, because the client
	// walks on the live playhead while the warm builds. It must settle (commit)
	// despite the cooldown the breach armed, or the retry repeats the read.
	handler.waitForBackgroundSubtitleWarms()
	assertFFmpegArgs(t, argsLog, "-ss 600", "-to 1320")
}
