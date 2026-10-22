package playback

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// net/http drops the body of a HEAD response, so a remux started for one runs
// ffmpeg over the whole file for nothing. HEAD answers with the headers a GET
// sends and never starts ffmpeg.
func TestServeRemuxHeadDoesNotStartFFmpeg(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(source, []byte("not really a movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The fake ffmpeg leaves a marker when it is asked to remux to stdout.
	marker := filepath.Join(dir, "remux-started")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = \"pipe:1\" ]; then\n" +
		"    : > '" + marker + "'\n" +
		"    printf remuxed\n" +
		"  fi\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := RemuxServeOptions{FFmpegPath: ffmpeg, ContentType: RemuxContentType(true), AudioOnly: true}
	serve := func(method string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		if err := ServeRemuxWithOptions(rec, httptest.NewRequest(method, "/api/v1/stream/session-1", nil), source, "mp4", 0, false, 0, 0, opts); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return rec
	}

	head := serve(http.MethodHead)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Type") != AudioOnlyRemuxMIMEV3 {
		t.Fatalf("HEAD: %d %v %q", head.Code, head.Header(), head.Body.String())
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("HEAD started a remux (marker: %v)", err)
	}

	get := serve(http.MethodGet)
	if get.Code != http.StatusOK || get.Body.String() != "remuxed" || get.Header().Get("Content-Type") != head.Header().Get("Content-Type") ||
		get.Header().Get("Transfer-Encoding") != head.Header().Get("Transfer-Encoding") {
		t.Fatalf("GET: %d %v %q", get.Code, get.Header(), get.Body.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("GET did not start the remux: %v", err)
	}
}
