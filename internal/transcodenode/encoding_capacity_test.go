package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestHealthAndStatusReportOperatorEncodingCapacity(t *testing.T) {
	encodinglimit.Configure(4)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	release, err := encodinglimit.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s := newTestServer(t)
	for _, handle := range []http.HandlerFunc{s.handleHealth, s.handleStatus} {
		w := httptest.NewRecorder()
		handle(w, httptest.NewRequest(http.MethodGet, "/status", nil))
		var report struct {
			Capacity *encodinglimit.Capacity `json:"encoding_capacity"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if report.Capacity == nil || report.Capacity.MaxJobs != 4 || report.Capacity.ActiveJobs != 1 || report.Capacity.AvailableJobs == nil || *report.Capacity.AvailableJobs != 3 {
			t.Fatalf("missing or inaccurate node capacity: %s", w.Body.String())
		}
	}
}

func TestWorkerStartReturnsRetryableCapacityFailure(t *testing.T) {
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	release, err := encodinglimit.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s := newTestServer(t)
	s.watcher.Config().Playback.HWAccel = "none"
	s.watcher.Config().Playback.FFmpegPath = "/does-not-exist"
	w := httptest.NewRecorder()
	s.handleStart(w, httptest.NewRequest(http.MethodPost, "/transcode/start", strings.NewReader(`{"session_id":"limited","input_path":"/input.mkv","target_codec_video":"h264"}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("full worker returned %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("capacity failure has no retry signal")
	}
	if len(s.sessions) != 0 {
		t.Fatal("refused request registered a session")
	}
}

func TestTrickplayCapacityFailureIsRetryable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the FFmpeg fixture uses a POSIX shell")
	}
	t.Setenv("PATH", t.TempDir())
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	const script = `#!/bin/sh
case "$*" in
  *-filters*) printf '... metadata V->V\n' ;;
  *-muxers*) exit 0 ;;
  *" -f concat "*) printf "Impossible to open 'file:/nonexistent/silo-concat-check.mkv'\n" >&2; exit 1 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Warm capability inventory before filling the node so this exercises the
	// extraction process, rather than failing in capability preflight.
	if _, err := mediasample.LoadCapabilities(context.Background(), ffmpeg); err != nil {
		t.Fatal(err)
	}
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	release, err := encodinglimit.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rec := postTrickplay(newTrickplayTestServer(t, ffmpeg), sheetsRequestJSON(t, "/media/movie.mkv"))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("capacity failure is not retryable: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCapabilityCapacityFailureIncludesRetrySignal(t *testing.T) {
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	release, err := encodinglimit.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s := newTestServer(t)
	s.watcher.Config().Playback.HWAccel = "none"
	s.watcher.Config().Playback.FFmpegPath = "ffmpeg"
	w := httptest.NewRecorder()
	s.handleHWCapabilities(w, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Errorf("capability saturation lost retry signal: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	writeToneMapRecipeError(w, errors.Join(playback.ErrToneMapExecutorUnavailable, encodinglimit.ErrAtCapacity))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Errorf("tone-map saturation lost retry signal: %d %s", w.Code, w.Body.String())
	}
}
