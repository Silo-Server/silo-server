package playback

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withRemuxFirstOutputWindows shrinks the first-output window and its
// slow-source grace for one test, and restores both afterwards.
func withRemuxFirstOutputWindows(t *testing.T, window, grace time.Duration) {
	t.Helper()
	prevWindow, prevGrace := remuxFirstOutputTimeout, remuxFirstOutputSlowSourceGrace
	remuxFirstOutputTimeout, remuxFirstOutputSlowSourceGrace = window, grace
	t.Cleanup(func() { remuxFirstOutputTimeout, remuxFirstOutputSlowSourceGrace = prevWindow, prevGrace })
}

// TestServeRemuxExtendsTheFirstOutputWindowForALiveSource pins the watchdog's
// one extension: a cold provider that has produced nothing inside the ordinary
// budget and has named no failure is slow, not dead. The production measurement
// was a first byte at 15-20 s against a 15 s watchdog, so ten of those plays
// were killed and then played fine on the retry.
func TestServeRemuxExtendsTheFirstOutputWindowForALiveSource(t *testing.T) {
	withRemuxFirstOutputWindows(t, 150*time.Millisecond, 3*time.Second)
	source := remuxSourceFile(t)
	w := &failoverResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)

	// First byte well past the ordinary window, then a clean exit.
	bin := remuxFakeFFmpeg(t, "sleep 0.5\nprintf x\nsleep 0.1\n")

	begin := time.Now()
	err := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath: bin,
	})
	elapsed := time.Since(begin)

	if err != nil {
		t.Fatalf("a live source that delivered after the ordinary window returned %v", err)
	}
	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 after the extension produced bytes", w.status)
	}
	if w.body.String() != "x" {
		t.Fatalf("body = %q, want the byte FFmpeg produced", w.body.String())
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("delivered after %v, which is inside the ordinary window; the test never exercised the extension", elapsed)
	}
}

// TestServeRemuxIndictsARefusedInputAtTheOrdinaryWindow is the other direction:
// when FFmpeg has already named a permanent reason the input cannot be read, the
// watchdog answers at the ordinary budget instead of waiting out the extension.
func TestServeRemuxIndictsARefusedInputAtTheOrdinaryWindow(t *testing.T) {
	withRemuxFirstOutputWindows(t, 150*time.Millisecond, 5*time.Second)
	source := remuxSourceFile(t)
	w := &failoverResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)

	bin := remuxFakeFFmpeg(t, "echo '[http] Protocol not found' >&2\nsleep 5\n")

	begin := time.Now()
	err := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath:      bin,
		DeferStartError: true,
	})
	elapsed := time.Since(begin)

	if err == nil {
		t.Fatal("a refused input returned no error")
	}
	if !errors.Is(err, errRemuxNoOutput) {
		t.Fatalf("error = %v, want it to identify the no-output failure", err)
	}
	if !strings.Contains(err.Error(), "the input protocol is not available") {
		t.Fatalf("error = %v, want the cause FFmpeg named", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("refused input took %v; the watchdog waited out the extension instead of reading FFmpeg's stderr", elapsed)
	}
	if w.status != 0 {
		t.Fatalf("pre-body failure committed status %d", w.status)
	}
}

// TestRemuxInputRefusalNamesOnlyPermanentCauses guards the classifier both ways:
// a named permanent refusal counts as evidence, and a transient read or connect
// problem does not — those are the cases the extension exists for.
func TestRemuxInputRefusalNamesOnlyPermanentCauses(t *testing.T) {
	for _, stderr := range []string{
		"[http @ 0x1] Protocol not found\n",
		"[mov,mp4,m4a @ 0x1] moov atom not found\n",
		"[http @ 0x1] Server returned 404 Not Found\n",
		"movie.mkv: No such file or directory\n",
		"Invalid data found when processing input\n",
	} {
		if err := remuxInputRefusal(stderr); err == nil {
			t.Fatalf("stderr %q was not recognized as a permanent input refusal", stderr)
		}
	}
	for _, stderr := range []string{
		"",
		"[http @ 0x1] Connection timed out\n",
		"[http @ 0x1] Error during demuxing: Input/output error\n",
		"frame= 0 fps=0.0 q=0.0 size=0kB time=00:00:00.00 bitrate=N/A\n",
	} {
		if err := remuxInputRefusal(stderr); err != nil {
			t.Fatalf("stderr %q was indicted as a permanent refusal: %v", stderr, err)
		}
	}
}
