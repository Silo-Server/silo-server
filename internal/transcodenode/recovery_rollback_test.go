package transcodenode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const workerRecoveryManifest = "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:9\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:2.669000,\nseg_00009.m4s\n#EXTINF:1.669000,\nseg_00010.m4s\n#EXTINF:1.668000,\nseg_00011.m4s\n"

func newWorkerRecoverySession(t *testing.T, probe string) (*Server, *playback.TranscodeSession) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the FFmpeg fixture uses a POSIX shell")
	}
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	server := newTestServer(t)
	server.tracker = &recordingSessionTracker{}
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := fmt.Sprintf("#!/bin/sh\nlast=\nfor arg in \"$@\"; do\n  if [ \"$arg\" = framecrc ]; then\n%s\n    exit 0\n  fi\n  last=$arg\ndone\nprintf '%%s' '%s' > \"$last\"\nprintf 'existing segment' > \"${last%%/*}/seg_00009.m4s\"\nexec sleep 30\n", probe, workerRecoveryManifest)
	if err := os.WriteFile(ffmpeg, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpeg
	const id = "worker-copy-recovery"
	opts := playback.TranscodeOpts{
		SessionID: id, InputPath: "/input.mkv", OutputDir: server.sessionOutputDir(id), FFmpegPath: ffmpeg,
		SeekSeconds: 18.261, StreamOriginSeconds: 18, CopySeekAnchorResolved: true,
		TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2, StartSegmentNumber: 9,
	}
	session, err := playback.StartTranscode(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	server.sessions[id] = session
	server.noteSessionAccessLocked(id)
	server.activeJobs.Store(1)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		if data, err := os.ReadFile(filepath.Join(opts.OutputDir, "stream.m3u8")); err == nil && strings.Contains(string(data), "seg_00011.m4s\n") {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("initial encoder did not publish its recovery timeline")
		}
	}
	card := playback.NewRecipeCard(1, "", 1, "http://node", opts)
	server.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
	return server, session
}

func TestRestartSegmentLocked_CopyProbeFailureKeepsRestoredWorker(t *testing.T) {
	for _, timestamp := range []string{"", "0, 17000, 17000, 41, 1024, 0x12345678"} {
		t.Run(timestamp, func(t *testing.T) {
			probe := "    printf '%s\\n' '#tb 0: 1/1000' '" + timestamp + "'"
			server, session := newWorkerRecoverySession(t, probe)
			before := session.Opts()
			_, ok, err := server.restartSegmentLocked(t.Context(), before.SessionID, session, 10)
			if ok || timestamp == "" && err == nil || timestamp != "" && err != nil {
				t.Fatalf("probe result changed: ok=%t err=%v", ok, err)
			}
			if !session.IsRunning() || server.sessions[before.SessionID] != session || server.activeJobs.Load() != 1 || session.Opts() != before {
				t.Fatal("worker evicted or changed a restored producer")
			}
			tracker := server.tracker.(*recordingSessionTracker)
			tracker.mu.Lock()
			defer tracker.mu.Unlock()
			if len(tracker.removed) != 0 {
				t.Fatal("worker removed tracking for a restored producer")
			}
		})
	}
}

func TestRestartSegmentLocked_CopyRestoreFailureAllowsWorkerReconstruction(t *testing.T) {
	server, session := newWorkerRecoverySession(t, "    rm -- \"$0\"\n    printf '%s\\n' '#tb 0: 1/1000' '0, 17000, 17000, 41, 1024, 0x12345678'")
	before := session.Opts()
	_, ok, err := server.restartSegmentLocked(t.Context(), before.SessionID, session, 10)
	if ok || err == nil || session.IsRunning() {
		t.Fatalf("unexpected restoration result: ok=%t err=%v running=%t", ok, err, session.IsRunning())
	}
	if server.sessions[before.SessionID] != nil || server.activeJobs.Load() != 0 || encodinglimit.Snapshot().ActiveJobs != 0 {
		t.Fatal("worker retained the failed generation or its active-job accounting")
	}
	if _, exists := server.lastAccess[before.SessionID]; exists {
		t.Fatal("failed generation retained an idle-clock entry")
	}
	tracker := server.tracker.(*recordingSessionTracker)
	tracker.mu.Lock()
	removed := len(tracker.removed) == 1 && tracker.removed[0] == before.SessionID
	tracker.mu.Unlock()
	if !removed {
		t.Fatal("worker retained tracking for the failed generation")
	}
	recipes := server.recipeStore.(*stubRecipeStore)
	if !recipes.ok || len(recipes.deletes) != 0 {
		t.Fatal("failed recovery revoked reconstruction authority")
	}
	if _, err := os.Stat(filepath.Join(before.OutputDir, "stream.m3u8")); err != nil {
		t.Fatal("failed recovery removed the known timeline")
	}

	// Infrastructure becomes usable again. The ordinary segment handler must
	// rebuild from its stored recipe rather than repeatedly serve the dead job.
	manifest := workerRecoveryManifest + "#EXTINF:2.000000,\nseg_00012.m4s\n"
	script := fmt.Sprintf("#!/bin/sh\nfor last in \"$@\"; do :; done\nprintf '%%s' '%s' > \"$last\"\nprintf 'reconstructed segment' > \"${last%%/*}/seg_00012.m4s\"\nexec sleep 30\n", manifest)
	if err := os.WriteFile(before.FFmpegPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Get("/transcode/{session_id}/segment/{name}", server.handleSegment)
	request := httptest.NewRequest(http.MethodGet, "/transcode/"+before.SessionID+"/segment/seg_00099.m4s", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("normal segment request failed reconstruction: %d %s", response.Code, response.Body.String())
	}
	restored := server.sessions[before.SessionID]
	if restored == nil || restored == session || !restored.IsRunning() || server.activeJobs.Load() != 1 || encodinglimit.Snapshot().ActiveJobs != 1 {
		t.Fatal("normal segment request did not register a new producer")
	}
	lease, err := restored.WaitForOpenSegment("seg_00012.m4s", time.Second)
	if err != nil {
		t.Fatalf("reconstructed producer made no progress: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/transcode/"+before.SessionID+"/segment/seg_00012.m4s", nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "reconstructed segment") {
		t.Fatalf("normal segment retry did not resume delivery: %d %s", response.Code, response.Body.String())
	}
}
