package transcodenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/encodinglimit"
)

func TestChapterThumbnailEncodingCapacity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the FFmpeg fixture uses a POSIX shell")
	}
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		name    string
		maxJobs int
		want    int
	}{
		{name: "full worker returns capacity reason", maxJobs: 1, want: http.StatusServiceUnavailable},
		{name: "default unlimited admits another extraction", maxJobs: 0, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encodinglimit.Configure(tc.maxJobs)
			t.Cleanup(func() { encodinglimit.Configure(0) })
			release, err := encodinglimit.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
			const script = "#!/bin/sh\nprintf invoked > \"$0.ran\"\nprintf thumbnail-frame\n"
			if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			server := newTestServer(t)
			server.watcher.Config().Playback.FFmpegPath = ffmpeg
			server.watcher.Config().Playback.HWAccel = "none"
			extract := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/chapter-thumbnails/extract", strings.NewReader(`{"input_path":"/media/movie.mkv","seek_seconds":5}`))
				req.Header.Set("Authorization", "Bearer "+testSecret)
				w := httptest.NewRecorder()
				server.Handler().ServeHTTP(w, req)
				return w
			}
			w := extract()
			if w.Code != tc.want {
				t.Fatalf("extraction status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			assertChapterEncodingResponse(t, w, ffmpeg, tc.want)
			if got := encodinglimit.Snapshot(); got.MaxJobs != tc.maxJobs || got.ActiveJobs != 1 {
				t.Fatalf("extraction changed the held reservation: %#v", got)
			}

			release()
			w = extract()
			if w.Code != http.StatusOK || w.Body.String() != "thumbnail-frame" {
				t.Fatalf("extraction after release = %d %q, want frame", w.Code, w.Body.String())
			}
			if got := encodinglimit.Snapshot().ActiveJobs; got != 0 {
				t.Fatalf("completed extraction left %d encoding reservations", got)
			}
		})
	}
}

func assertChapterEncodingResponse(t *testing.T, w *httptest.ResponseRecorder, ffmpeg string, want int) {
	t.Helper()
	if want != http.StatusServiceUnavailable {
		if w.Body.String() != "thumbnail-frame" || w.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("unlimited extraction = %s %q", w.Header().Get("Content-Type"), w.Body.String())
		}
		return
	}
	var failure chapterthumbs.RemoteExtractErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Reason != "transcode_node_capacity_exhausted" || failure.Error != "node is at encoding capacity" {
		t.Fatalf("capacity failure = %#v, want transcode capacity reason and original message", failure)
	}
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	if _, err := os.Stat(ffmpeg + ".ran"); !os.IsNotExist(err) {
		t.Fatalf("refused extraction invoked FFmpeg: %v", err)
	}
}
