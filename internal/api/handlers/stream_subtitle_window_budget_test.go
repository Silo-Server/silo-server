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
	// A window extract (-ss ...) never reaches its range; a full-track warm (no
	// -ss) commits promptly, so the breach's warm is observable.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argsLog + "'\n" +
		"case \"$*\" in\n" +
		"  *-ss*)\n    sleep 60\n    ;;\n" +
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
	// The 503 carries the standard JSON problem body; what it must never carry
	// is a partial slice.
	if body := rec.Body.String(); !strings.Contains(body, "subtitle_source_unavailable") {
		t.Fatalf("body = %q, want the subtitle_source_unavailable problem", body)
	} else if strings.Contains(body, "WEBVTT") {
		t.Fatalf("body = %q, want no partial subtitle slice", body)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("answered after %s, want the configured budget", elapsed)
	}
	if rec.Header().Get("X-Subtitle-Coverage") != "" {
		t.Fatalf("coverage header = %q, want none on a 503", rec.Header().Get("X-Subtitle-Coverage"))
	}

	// The breach started the detached warm: the retry reads a committed slice.
	waitForWarmInvocation(t, argsLog)
}
