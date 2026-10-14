package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type preparedTrackingFixture struct {
	recordingSessionTracker
	mu       sync.Mutex
	receipts []*preparedTrackingReceipt
	reject   bool
	block    <-chan struct{}
	legacy   atomic.Int64
}
type preparedTrackingReceipt struct {
	owner      *preparedTrackingFixture
	tracked    chan struct{}
	removed    chan struct{}
	trackOnce  sync.Once
	removeOnce sync.Once
}

func (t *preparedTrackingFixture) PrepareSessionTracking(context.Context, nodesessions.SessionInfo) SessionTracking {
	if t.reject {
		return nil
	}
	receipt := &preparedTrackingReceipt{owner: t, tracked: make(chan struct{}), removed: make(chan struct{})}
	t.mu.Lock()
	t.receipts = append(t.receipts, receipt)
	t.mu.Unlock()
	return receipt
}
func (t *preparedTrackingFixture) Track(context.Context, nodesessions.SessionInfo) { t.legacy.Add(1) }
func (t *preparedTrackingFixture) Remove(context.Context, string)                  { t.legacy.Add(1) }
func (p *preparedTrackingReceipt) Track(context.Context) {
	p.trackOnce.Do(func() { close(p.tracked) })
	if p.owner.block != nil {
		<-p.owner.block
	}
}
func (p *preparedTrackingReceipt) Remove(context.Context) {
	p.removeOnce.Do(func() { close(p.removed) })
}
func (t *preparedTrackingFixture) onlyReceipt(test *testing.T) *preparedTrackingReceipt {
	test.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.receipts) != 1 {
		test.Fatalf("prepared receipts=%d, want1", len(t.receipts))
	}
	return t.receipts[0]
}
func waitTrackingReceipt(t *testing.T, channel <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal(what)
	}
}

func TestPreparedTranscodeTrackingRetiresOnIdleReloadAndShutdown(t *testing.T) {
	for _, operation := range []string{"idle", "reload", "shutdown"} {
		t.Run(operation, func(t *testing.T) {
			server := newTestServer(t)
			tracker := &preparedTrackingFixture{}
			server.tracker = tracker
			directory := server.sessionOutputDir("work")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			session := playback.NewTranscodeSessionForTest(directory)
			receipt := server.prepareTracking(t.Context(), nodesessions.SessionInfo{SessionID: "work"})
			server.holdSessionJob(session, t.Context(), func() {}, receipt)
			server.sessions["work"] = session
			server.lastAccess["work"] = time.Now().Add(-time.Hour)
			server.activeJobs.Store(1)
			switch operation {
			case "idle":
				server.reapIdleSessions(time.Minute)
			case "reload":
				if err := server.teardownForForceReload(t.Context(), "", 0); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				if err := server.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			waitTrackingReceipt(t, tracker.onlyReceipt(t).removed, "producer retirement did not remove exact tracking receipt")
			if tracker.legacy.Load() != 0 {
				t.Fatal("prepared tracker used unscoped legacy cleanup")
			}
			server.jobs.mu.Lock()
			remaining := len(server.jobs.sessions)
			server.jobs.mu.Unlock()
			if remaining != 0 || server.activeJobs.Load() != 0 {
				t.Fatal("producer ownership retained after retirement")
			}
		})
	}
}
func TestPreparedTrackingRejectedOrFailedSpawnDoesNotUseLegacy(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected-registration", true: "failed-spawn"}[failed], func(t *testing.T) {
			server := newTestServer(t)
			tracker := &preparedTrackingFixture{reject: !failed}
			server.tracker = tracker
			program := filepath.Join(t.TempDir(), "ffmpeg")
			script := "#!/bin/sh\nwhile IFS= read -r line; do [ \"$line\" = q ] && exit 0; done\n"
			if failed {
				script = "#!/bin/sh\nexit 1\n"
			}
			if err := os.WriteFile(program, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			server.watcher.Config().Playback.FFmpegPath = program
			server.watcher.Config().Playback.HWAccel = "none"
			body, _ := json.Marshal(TranscodeStartRequest{SessionID: "work", InputPath: "/media/source.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, RequireReady: failed})
			response := httptest.NewRecorder()
			server.handleStart(response, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
			if failed && response.Code != http.StatusInternalServerError || !failed && response.Code != http.StatusAccepted {
				t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
			}
			if err := server.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			tracker.mu.Lock()
			count := len(tracker.receipts)
			tracker.mu.Unlock()
			if count != 0 || tracker.legacy.Load() != 0 {
				t.Fatal("failed/rejected registration leaked ticket or used legacy callback")
			}
		})
	}
}
func TestPreparedDownloadTrackingOwnsExactCleanup(t *testing.T) {
	server := newTestServer(t)
	release := make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	defer open()
	tracker := &preparedTrackingFixture{block: release}
	server.tracker = tracker
	finish := server.trackDownloadPrepare(t.Context(), nodesessions.SessionInfo{SessionID: "download"})
	receipt := tracker.onlyReceipt(t)
	waitTrackingReceipt(t, receipt.tracked, "download Track did not begin asynchronously")
	finish()
	select {
	case <-receipt.removed:
		t.Fatal("download removal overtook queued Track")
	default:
	}
	open()
	waitTrackingReceipt(t, receipt.removed, "download completion did not retire exact receipt")
	if tracker.legacy.Load() != 0 {
		t.Fatal("download used unscoped tracking callback")
	}
}
func TestPreparedProgressiveTrackingOwnsExactCleanup(t *testing.T) {
	tracker := &preparedTrackingFixture{}
	testShutdownDrainsProgressiveRemux(t, tracker)
	receipt := tracker.onlyReceipt(t)
	waitTrackingReceipt(t, receipt.removed, "progressive shutdown retained tracking receipt")
	if tracker.legacy.Load() != 0 {
		t.Fatal("progressive lifecycle used unscoped tracking callback")
	}
}
