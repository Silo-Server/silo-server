package playback

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A progressive remux generates its body from a media position, so a range that
// starts past zero has no byte offset to resume at. It is still not an error:
// the client is resuming its own read (a browser does that on a seek), so the
// answer is the whole stream from the generation's start — exactly what the
// transport answered before it read Range at all. A 416 here closed the player
// on every web seek.
func TestServeRemuxAnswersAStartOffsetFromTheGenerationStart(t *testing.T) {
	source := remuxSourceFile(t)
	// Record the seek the generation was started with, so the test also pins
	// that the plan's anchor is where the restarted body begins.
	spawned := filepath.Join(t.TempDir(), "argv")
	bin := remuxFakeFFmpeg(t, "printf '%s ' \"$@\" > '"+spawned+"'\nprintf body\n")

	for _, rangeHeader := range []string{
		"bytes=1000000-",      // a client resuming its own read mid-stream
		"bytes=900000000000-", // past any size: the total is unknown, so not unsatisfiable
		"bytes=-500",          // suffix: the end of a body whose length is not known yet
		"bytes=0-10,20-30",    // multi-range
	} {
		t.Run(rangeHeader, func(t *testing.T) {
			w := &failoverResponseWriter{}
			request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)
			request.Header.Set("Range", rangeHeader)

			if err := ServeRemuxWithOptions(w, request, source, "mp4", 328.19, false, 0, 0, RemuxServeOptions{
				FFmpegPath:      bin,
				DeferStartError: true,
			}); err != nil {
				t.Fatalf("serve %q: %v", rangeHeader, err)
			}
			if w.status != http.StatusOK {
				t.Fatalf("status = %d, want the pre-range 200 from the generation's start", w.status)
			}
			if got := w.header.Get("Content-Range"); got != "" {
				t.Fatalf("Content-Range = %q, want none on a whole response", got)
			}
			if got := w.body.String(); got != "body" {
				t.Fatalf("body = %q, want every produced byte", got)
			}
			argv, readErr := os.ReadFile(spawned)
			if readErr != nil {
				t.Fatalf("read ffmpeg argv: %v", readErr)
			}
			if !strings.Contains(string(argv), "-ss 328.190") {
				t.Fatalf("ffmpeg argv = %q, want the plan's seek anchor", argv)
			}
		})
	}
}

// A prefix range is honored by answering with exactly the bytes it asked for:
// the stream ends at the requested count instead of running to the title's end.
func TestServeRemuxHonoursAPrefixRange(t *testing.T) {
	source := remuxSourceFile(t)
	w := &failoverResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)
	request.Header.Set("Range", "bytes=0-99")

	// More than the range asks for, so the test proves the response is cut.
	body := strings.Repeat("a", 64) + strings.Repeat("b", 64)
	bin := remuxFakeFFmpeg(t, fmt.Sprintf("printf '%%s' '%s'\n", body))

	if err := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath: bin,
	}); err != nil {
		t.Fatalf("prefix range error = %v", err)
	}
	if w.status != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.status)
	}
	if got := w.header.Get("Content-Range"); got != "bytes 0-99/*" {
		t.Fatalf("Content-Range = %q, want the requested prefix", got)
	}
	if got := w.body.Len(); got != 100 {
		t.Fatalf("body = %d bytes, want exactly the 100 requested", got)
	}
}

// The no-range case is unchanged: a whole 200 stream of every byte FFmpeg
// produced.
func TestServeRemuxWithoutRangeStreamsEverything(t *testing.T) {
	source := remuxSourceFile(t)
	bin := remuxFakeFFmpeg(t, "printf 'abcdef'\n")

	serve := func(rangeHeader string) *failoverResponseWriter {
		t.Helper()
		w := &failoverResponseWriter{}
		request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)
		if rangeHeader != "" {
			request.Header.Set("Range", rangeHeader)
		}
		if err := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
			FFmpegPath: bin,
		}); err != nil {
			t.Fatalf("serve range %q: %v", rangeHeader, err)
		}
		return w
	}

	w := serve("")
	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.status)
	}
	if w.header.Get("Content-Range") != "" {
		t.Fatalf("Content-Range = %q, want none on a whole response", w.header.Get("Content-Range"))
	}
	if got := w.body.String(); got != "abcdef" {
		t.Fatalf("body = %q, want every byte", got)
	}
	// `bytes=0-` is the whole stream too, not a zero-length prefix.
	if w := serve("bytes=0-"); w.status != http.StatusOK || w.body.String() != "abcdef" {
		t.Fatalf("bytes=0- answered %d %q, want the whole 200 stream", w.status, w.body.String())
	}
}
