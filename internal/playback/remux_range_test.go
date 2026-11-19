package playback

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A progressive remux generates its body from a media position, so a byte range
// that starts past zero has no position to restart at. It used to be ignored:
// the client asked for 1.5 MB and received the whole title. The answer is the
// unsatisfiable range, before any FFmpeg work and without the response being
// read as evidence about the source.
func TestServeRemuxRefusesAByteOffsetItCannotResume(t *testing.T) {
	source := remuxSourceFile(t)
	w := &failoverResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/stream/session-1", nil)
	request.Header.Set("Range", "bytes=1000000-")

	marker := filepath.Join(t.TempDir(), "spawned")
	bin := remuxFakeFFmpeg(t, "touch '"+marker+"'\nprintf body\n")

	err := ServeRemuxWithOptions(w, request, source, "mp4", 0, false, 0, 0, RemuxServeOptions{
		FFmpegPath:      bin,
		DeferStartError: true,
	})
	if !errors.Is(err, ErrRemuxRangeUnsatisfiable) {
		t.Fatalf("error = %v, want ErrRemuxRangeUnsatisfiable", err)
	}
	if w.status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", w.status)
	}
	if got := w.header.Get("Content-Range"); got != "bytes */*" {
		t.Fatalf("Content-Range = %q, want the unknown-length unsatisfied form", got)
	}
	if w.body.Len() != 0 {
		t.Fatalf("body = %d bytes, want none", w.body.Len())
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("ffmpeg was spawned for an unsatisfiable range")
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
