package transcodenode

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func releaseRequest(id, startID string) *http.Request {
	target := "/transcode/" + id + "?fence=0"
	if startID != "" {
		target += "&start_id=" + startID
	}
	return withNodeRouteParams(httptest.NewRequest(http.MethodDelete, target, nil), map[string]string{"session_id": id})
}

// A move's rollback releases the start it sent. When another API server's
// start for the same transport has replaced that job on this node, the late
// release must leave the newer job running.
func TestHandleStop_ReleaseForAnotherStartLeavesNewerJob(t *testing.T) {
	const transportID = "sess-moved-twice"
	s := newTestServer(t)
	s.tracker = nodesessions.NewLocalTracker()
	ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.watcher.Config().Playback.FFmpegPath = ffmpegPath

	rec := postStartRequest(t, s, map[string]any{
		"session_id": transportID, "start_id": "start-newer", "input_path": "/media/movie.mkv",
		"source_grant": "/media/movie.mkv", "target_codec_video": "h264", "target_codec_audio": "aac", "segment_duration": 2,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	live := func() *playback.TranscodeSession {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.sessions[transportID]
	}
	newer := live()
	if newer == nil {
		t.Fatal("start did not register the session")
	}
	t.Cleanup(func() { _ = newer.CloseProcess() })

	late := httptest.NewRecorder()
	s.handleStop(late, releaseRequest(transportID, "start-older"))
	if late.Code != http.StatusNotFound {
		t.Fatalf("late release status = %d, want 404", late.Code)
	}
	if live() != newer {
		t.Fatal("a release for an older start ended the newer job")
	}

	own := httptest.NewRecorder()
	s.handleStop(own, releaseRequest(transportID, "start-newer"))
	if own.Code != http.StatusNoContent {
		t.Fatalf("release of the job's own start status = %d, want 204", own.Code)
	}
	if live() != nil {
		t.Fatal("a release for the job's own start left it running")
	}
}

// A retire releases the job on the node a transport left, a job some earlier
// start or rebuild made, so it names no start. When a newer start or rebuild
// publishes its job while the retire waits behind it for the transport's
// lifecycle lock, the retire must leave that job.
func TestHandleStop_ReleaseLeavesJobPublishedWhileItWaited(t *testing.T) {
	const transportID = "sess-retired-node"
	s := newTestServer(t)
	s.tracker = nodesessions.NewLocalTracker()
	s.sessions[transportID] = &playback.TranscodeSession{}
	s.activeJobs.Store(1)

	// The newer start holds the lifecycle lock across its spawn and publication.
	unlock := s.lockSessionLifecycle(transportID)
	stopped := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.handleStop(rec, releaseRequest(transportID, ""))
		stopped <- rec
	}()
	waitForLifecycleWaiters(t, s, transportID, 2)

	newer := &playback.TranscodeSession{}
	s.mu.Lock()
	s.forgetSessionLocked(transportID)
	s.registerSessionLocked(transportID, newer, sessionIdentity{}, nil)
	s.mu.Unlock()
	unlock()

	rec := <-stopped
	if rec.Code != http.StatusNotFound {
		t.Fatalf("release status = %d, want 404", rec.Code)
	}
	s.mu.RLock()
	got := s.sessions[transportID]
	s.mu.RUnlock()
	if got != newer {
		t.Fatal("a release that waited behind a newer start ended the job it published")
	}
}

// waitForLifecycleWaiters waits until n holders or waiters share transportID's
// lifecycle lock.
func waitForLifecycleWaiters(t *testing.T, s *Server, transportID string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.lifecycleMu.Lock()
		lk := s.lifecycleLocks[transportID]
		refs := 0
		if lk != nil {
			refs = lk.refs
		}
		s.lifecycleMu.Unlock()
		if refs >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lifecycle lock refs = %d, want %d", refs, n)
		}
		runtime.Gosched()
	}
}

// A rebuild on the node a transport is leaving resolves with the core before
// the move commits and publishes after it. A retire that waits behind that
// rebuild for the lifecycle lock must still end it, or the old node keeps a
// duplicate FFmpeg until the idle reaper.
func TestHandleStop_ReleaseEndsRebuildPublishedWhileItWaited(t *testing.T) {
	const transportID = "sess-rebuilt-on-old-node"
	s := newTestServer(t)
	s.tracker = nodesessions.NewLocalTracker()

	unlock := s.lockSessionLifecycle(transportID)
	stopped := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.handleStop(rec, releaseRequest(transportID, ""))
		stopped <- rec
	}()
	waitForLifecycleWaiters(t, s, transportID, 2)

	s.mu.Lock()
	s.registerSessionLocked(transportID, &playback.TranscodeSession{}, identityOfResolution(&nodecontrol.Resolution{}), nil)
	s.activeJobs.Store(1)
	s.mu.Unlock()
	unlock()

	rec := <-stopped
	if rec.Code != http.StatusNoContent {
		t.Fatalf("release status = %d, want 204", rec.Code)
	}
	s.mu.RLock()
	_, running := s.sessions[transportID]
	s.mu.RUnlock()
	if running {
		t.Fatal("a release left a rebuild of the old route that published while it waited")
	}
}
