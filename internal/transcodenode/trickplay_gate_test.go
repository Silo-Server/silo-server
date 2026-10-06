package transcodenode

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestTrickplayRejectsReprobeBeforeResolvingHardware(t *testing.T) {
	server := newTrickplayTestServer(t, "ffmpeg")
	server.watcher.Config().Playback.HWAccel = "auto"
	calls := 0
	server.trickplay.resolver().Resolve = func(context.Context, string, string, string) string {
		calls++
		return "none"
	}
	if _, ok := server.gpu.beginReprobe(nil); !ok {
		t.Fatal("could not start reprobe")
	}
	defer server.gpu.endReprobe()
	rec := postTrickplay(server, sheetsRequestJSON(t, filepath.Join(t.TempDir(), "missing.mkv")))
	if calls != 0 {
		t.Fatalf("hardware resolved %d times during reprobe", calls)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
	}
	if server.activeJobs.Load() != 0 {
		t.Fatalf("active jobs=%d", server.activeJobs.Load())
	}
}

func TestTrickplayResolutionHoldsAndReleasesGPUAdmission(t *testing.T) {
	server := newTrickplayTestServer(t, "ffmpeg")
	server.watcher.Config().Playback.HWAccel = "auto"
	calls := 0
	server.trickplay.resolver().Resolve = func(context.Context, string, string, string) string {
		calls++
		busy, ok := server.gpu.beginReprobe(nil)
		if ok {
			server.gpu.endReprobe()
			t.Error("reprobe admitted during hardware resolution")
		}
		if busy != 1 {
			t.Errorf("GPU work during resolution=%d, want 1", busy)
		}
		return "none"
	}
	rec := postTrickplay(server, sheetsRequestJSON(t, filepath.Join(t.TempDir(), "missing.mkv")))
	if calls != 1 || rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("calls=%d response=%d %s", calls, rec.Code, rec.Body.String())
	}
	if busy, ok := server.gpu.beginReprobe(nil); !ok {
		t.Fatalf("GPU admission leaked after extraction error: busy=%d", busy)
	}
	server.gpu.endReprobe()
	if server.activeJobs.Load() != 0 {
		t.Fatalf("active jobs=%d", server.activeJobs.Load())
	}
}
