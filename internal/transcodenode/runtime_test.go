package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
)

// remoteCore answers a Remote's reads like the core's config endpoint: the
// current document, or "not modified" when the caller names its revision.
type remoteCore struct {
	mu       sync.Mutex
	revision int
	doc      nodecontrol.NodeConfig
	err      error
}

func (c *remoteCore) set(body nodecontrol.NodeConfigBody) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revision++
	c.doc = nodecontrol.NodeConfig{Revision: "rev-" + strconv.Itoa(c.revision), NodeConfigBody: body}
}

func (c *remoteCore) FetchConfig(_ context.Context, etag string) (*nodecontrol.NodeConfig, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, false, c.err
	}
	if etag == `"`+c.doc.Revision+`"` {
		return nil, true, nil
	}
	doc := c.doc
	return &doc, false, nil
}

// A transcode node's server runs on the configuration and identity the core
// sends (nodeconfig.Remote), not the database watcher: its directories, its
// row id and URL, the drain request, and reloads all come from the document.
func TestServerRunsOnTheCoresConfiguration(t *testing.T) {
	transcodeDir := t.TempDir()
	body := nodecontrol.NodeConfigBody{
		Node:         nodecontrol.NodeIdentity{ID: 31, Name: "gpu-31", URL: "http://gpu-31:8080"},
		SourceAccess: "local",
		Playback:     nodecontrol.PlaybackConfig{FFmpegPath: "ffmpeg", HWAccel: "none", TranscodeDir: transcodeDir},
		Download:     nodecontrol.DownloadConfig{ArtifactDir: filepath.Join(transcodeDir, "artifacts")},
	}
	core := &remoteCore{}
	core.set(body)
	remote := nodeconfig.NewRemote(core, ":8080")
	if err := remote.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	tracker := nodesessions.NewLocalTracker(func() (string, string) {
		url, _ := remote.NodeRegisteredURL()
		return url, remote.NodeName()
	})
	srv := NewServer(remote, tracker)

	if srv.transcodeDir != transcodeDir {
		t.Fatalf("transcode directory = %q, want the document's %q", srv.transcodeDir, transcodeDir)
	}
	if id, ok := srv.nodeRowID(); !ok || id != 31 {
		t.Fatalf("row id = %d %v, want 31", id, ok)
	}
	if tracker.NodeURL() != "http://gpu-31:8080" || tracker.NodeName() != "gpu-31" {
		t.Fatalf("tracker identity = %q %q", tracker.NodeURL(), tracker.NodeName())
	}
	announced := make(chan struct{}, 4)
	srv.SetDrainAnnouncer(func() { announced <- struct{}{} })
	if status := srv.healthStatus(); status != HealthStatusOK {
		t.Fatalf("health before a drain request = %q", status)
	}
	cfg := srv.watcher.Config()
	reload := func() {
		t.Helper()
		rr := httptest.NewRecorder()
		srv.handleReloadConfig(rr, httptest.NewRequest(http.MethodPost, "/admin/reload-config", nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("reload-config: %d %s", rr.Code, rr.Body.String())
		}
	}

	body.DrainRequested = true
	core.set(body)
	reload()
	select {
	case <-announced:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain request the core sent was not announced")
	}
	rr := httptest.NewRecorder()
	srv.handleHealth(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	var health HealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != HealthStatusDrained {
		t.Fatalf("health of an idle node asked to drain = %q, want %q", health.Status, HealthStatusDrained)
	}
	if srv.watcher.Config() != cfg {
		t.Fatal("a drain-only change swapped the config pointer; handlers would refuse work")
	}

	body.Playback.HWAccel = "vaapi"
	core.set(body)
	reload()
	if updated := srv.watcher.Config(); updated == cfg || updated.Playback.HWAccel != "vaapi" {
		t.Fatalf("a playback change did not reach the server: %+v", updated.Playback)
	}
}

// gatedTracker is a LocalTracker whose Track, once started, waits until the
// test releases it: a slow tracking write. done closes when it has finished.
type gatedTracker struct {
	*nodesessions.LocalTracker
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (g *gatedTracker) Track(ctx context.Context, info nodesessions.SessionInfo) {
	close(g.started)
	<-g.release
	g.LocalTracker.Track(ctx, info)
	close(g.done)
}

// A stop that arrives while a start's tracking write is still in progress
// must leave the session untracked: the node reports its tracker every
// second, and a stale entry would be listed until the node restarts.
func TestStopRacingASlowTrackLeavesTheSessionUntracked(t *testing.T) {
	for _, reconstruct := range []bool{false, true} {
		name := "start"
		if reconstruct {
			name = "reconstruct"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			tracker := &gatedTracker{
				LocalTracker: nodesessions.NewLocalTracker(nil),
				started:      make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}),
			}
			server.tracker = tracker
			ffmpegPath := filepath.Join(t.TempDir(), "looping-ffmpeg.sh")
			if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nwhile :; do sleep 0.1; done\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			server.watcher.Config().Playback.FFmpegPath = ffmpegPath
			const id = "tracked-session"

			started := make(chan struct{})
			go func() {
				defer close(started)
				if reconstruct {
					card := nativeTransportCard("viewer", id)
					if _, err := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), id, -1, resolutionOf(*card)); err != nil {
						t.Errorf("spawnReconstruct: %v", err)
					}
					return
				}
				body, _ := json.Marshal(TranscodeStartRequest{
					SessionID: id, SourceGrant: "/media/movie.mkv", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
				})
				rr := httptest.NewRecorder()
				server.handleStart(rr, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
				if rr.Code != http.StatusAccepted {
					t.Errorf("start: %d %s", rr.Code, rr.Body.String())
				}
			}()
			select {
			case <-tracker.started:
			case <-time.After(5 * time.Second):
				close(tracker.release)
				t.Fatal("the session was never tracked")
			}

			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				req := httptest.NewRequest(http.MethodPost, "/transcode/"+id+"/stop", nil)
				routeContext := chi.NewRouteContext()
				routeContext.URLParams.Add("session_id", id)
				server.handleStop(httptest.NewRecorder(), req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)))
			}()
			// The stop has to wait for the start that holds the session; give
			// a stop that does not wait the time to finish first.
			select {
			case <-stopped:
			case <-time.After(250 * time.Millisecond):
			}
			close(tracker.release)
			<-started
			<-stopped
			<-tracker.done

			if left := tracker.Snapshot(); len(left) != 0 {
				t.Fatalf("a stopped session is still tracked: %+v", left)
			}
		})
	}
}

// A node whose key the core refuses (rotated, or the node deleted) says so on
// /health, which the core's sweep reads as unhealthy, and stops saying so once
// a configuration read succeeds again.
func TestHealthReportsARejectedKeyUntilTheCoreAcceptsIt(t *testing.T) {
	transcodeDir := t.TempDir()
	core := &remoteCore{}
	core.set(nodecontrol.NodeConfigBody{
		Node:     nodecontrol.NodeIdentity{ID: 32, Name: "gpu-32", URL: "http://gpu-32:8080"},
		Playback: nodecontrol.PlaybackConfig{FFmpegPath: "ffmpeg", HWAccel: "none", TranscodeDir: transcodeDir},
		Download: nodecontrol.DownloadConfig{ArtifactDir: filepath.Join(transcodeDir, "artifacts")},
	})
	remote := nodeconfig.NewRemote(core, ":8080")
	if err := remote.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(remote, nodesessions.NewLocalTracker(func() (string, string) { return "", "" }))
	srv.SetKeyRejected(remote.KeyRejected)
	if healthStatusOf(t, srv).KeyRejected {
		t.Fatal("a node the core accepts reports a rejected key")
	}

	core.mu.Lock()
	core.err = &nodecontrol.StatusError{Path: nodecontrol.PathConfig, Status: http.StatusUnauthorized}
	core.mu.Unlock()
	_ = remote.ForceReload(t.Context())
	if health := healthStatusOf(t, srv); !health.KeyRejected || health.Status != HealthStatusOK {
		t.Fatalf("after a 401 config read health = %+v, want key_rejected with the drain status kept", health)
	}

	core.mu.Lock()
	core.err = nil
	core.mu.Unlock()
	if err := remote.ForceReload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if healthStatusOf(t, srv).KeyRejected {
		t.Fatal("health still reports a rejected key after the core accepted a read")
	}
}
