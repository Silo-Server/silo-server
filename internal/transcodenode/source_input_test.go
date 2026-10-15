package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// grantPathSources is a test source opener whose core grants any absolute
// path named as the grant itself, as nodecontroltest.PathGrants mints them,
// and refuses anything else. It reads no file: tests run fake FFmpeg on
// paths that do not exist.
type grantPathSources struct {
	mu     sync.Mutex
	opened []*testInput
}

func (g *grantPathSources) OpenSource(_ context.Context, grant string) (SourceInput, error) {
	if !filepath.IsAbs(grant) {
		return nil, &nodesource.StatusError{Op: "open", Status: http.StatusForbidden}
	}
	input := newTestInput(grant)
	g.mu.Lock()
	g.opened = append(g.opened, input)
	g.mu.Unlock()
	return input, nil
}

// openInputs is every input opened so far.
func (g *grantPathSources) openInputs() []*testInput {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*testInput(nil), g.opened...)
}

// testInput is an opened test source.
type testInput struct {
	path   string
	ctx    context.Context
	cancel context.CancelCauseFunc
}

func newTestInput(path string) *testInput {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &testInput{path: path, ctx: ctx, cancel: cancel}
}

func (i *testInput) Input() string            { return i.path }
func (i *testInput) Identity() string         { return `"file-1-0-0"` }
func (i *testInput) Context() context.Context { return i.ctx }
func (i *testInput) Err() error {
	if i.ctx.Err() == nil {
		return nil
	}
	return context.Cause(i.ctx)
}
func (i *testInput) Close() error { i.cancel(nodesource.ErrClosed); return nil }

// withdraw ends the input as the core refusing it would.
func (i *testInput) withdraw() {
	i.cancel(&nodesource.StatusError{Op: "renew", Status: http.StatusGone})
}

// closed reports whether the input's job released it.
func (i *testInput) closed() bool { return errors.Is(i.Err(), nodesource.ErrClosed) }

// A session owns the source its start opened, across its FFmpeg restarts,
// until it is stopped.
func TestTranscodeSessionHoldsItsSourceUntilStopped(t *testing.T) {
	server := newLoopingFFmpegServer(t)
	sources := &grantPathSources{}
	server.sources = sources
	if rr := postStart(t, server, "held-source"); rr.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rr.Code, rr.Body.String())
	}
	opened := sources.openInputs()
	if len(opened) != 1 || opened[0].closed() {
		t.Fatalf("opened = %v, want one open source", opened)
	}
	server.mu.RLock()
	session := server.sessions["held-source"]
	server.mu.RUnlock()
	if session == nil || session.Opts().InputPath != "/media/movie.mkv" {
		t.Fatal("session does not read its opened source")
	}
	stopSession(t, server, "held-source")
	if !opened[0].closed() {
		t.Fatal("stopping the session left its source open")
	}
}

// A replacement start (an audio-track switch) opens its own source and
// releases the predecessor's with the predecessor.
func TestTranscodeReplacementReleasesThePredecessorsSource(t *testing.T) {
	server := newLoopingFFmpegServer(t)
	sources := &grantPathSources{}
	server.sources = sources
	for range 2 {
		if rr := postStart(t, server, "replaced"); rr.Code != http.StatusAccepted {
			t.Fatalf("start = %d %s", rr.Code, rr.Body.String())
		}
	}
	opened := sources.openInputs()
	if len(opened) != 2 || !opened[0].closed() || opened[1].closed() {
		t.Fatalf("predecessor closed %v, replacement closed %v", opened[0].closed(), opened[1].closed())
	}
	stopSession(t, server, "replaced")
}

// A start that fails releases the source it opened.
func TestFailedStartReleasesItsSource(t *testing.T) {
	server := newTestServer(t)
	sources := &grantPathSources{}
	server.sources = sources
	server.watcher.Config().Playback.FFmpegPath = filepath.Join(t.TempDir(), "missing-ffmpeg")
	body, _ := json.Marshal(TranscodeStartRequest{SessionID: "fails", SourceGrant: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2})
	rr := httptest.NewRecorder()
	server.handleStart(rr, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
	if rr.Code == http.StatusAccepted {
		t.Fatal("start with a missing ffmpeg succeeded")
	}
	if opened := sources.openInputs(); len(opened) != 1 || !opened[0].closed() {
		t.Fatalf("failed start left its source open: %v", opened)
	}
}

// A source the core withdraws at a renewal or a read ends the session that
// reads it.
func TestWithdrawnSourceEndsItsSession(t *testing.T) {
	server := newLoopingFFmpegServer(t)
	sources := &grantPathSources{}
	server.sources = sources
	if rr := postStart(t, server, "withdrawn"); rr.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rr.Code, rr.Body.String())
	}
	sources.openInputs()[0].withdraw()
	deadline := time.Now().Add(10 * time.Second)
	for {
		server.mu.RLock()
		_, live := server.sessions["withdrawn"]
		server.mu.RUnlock()
		if !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a withdrawn source left its session running")
		}
		time.Sleep(time.Millisecond)
	}
}

// countingSources is a source opener that counts its calls and answers each
// with err.
type countingSources struct {
	calls atomic.Int32
	err   error
}

func (c *countingSources) OpenSource(context.Context, string) (SourceInput, error) {
	c.calls.Add(1)
	return nil, c.err
}

// A local-access node whose mount shows another revision than the core
// approved refuses the job with its own reason, so the caller can tell a
// node's mount from a refused grant.
func TestLocalSourceMismatchHasItsOwnRefusal(t *testing.T) {
	server := newTestServer(t)
	server.sources = &countingSources{err: fmt.Errorf("%w: stat: no such file", nodesource.ErrLocalMismatch)}
	body, _ := json.Marshal(TranscodeStartRequest{SessionID: "mismatch", SourceGrant: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2})
	rr := httptest.NewRecorder()
	server.handleStart(rr, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "this node's mount") {
		t.Fatalf("local mismatch = %d %s", rr.Code, rr.Body.String())
	}
}

// Work the node refuses anyway (draining, re-probing its hardware) never asks
// the core for its source.
func TestRefusedWorkDoesNotOpenItsSource(t *testing.T) {
	for name, refuse := range map[string]func(*Server){
		"draining":   func(s *Server) { s.shutdownDrain.Store(true) },
		"re-probing": func(s *Server) { s.gpu.beginReprobe(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			sources := &countingSources{err: errors.New("unexpected open")}
			server.sources = sources
			refuse(server)
			start := `{"session_id":"s","source_grant":"/media/movie.mkv","target_codec_video":"h264","target_codec_audio":"aac","segment_duration":2}`
			for route, test := range map[string]struct {
				handle func(http.ResponseWriter, *http.Request)
				body   string
			}{
				"start":     {server.handleStart, start},
				"chapter":   {server.handleChapterThumbnailExtract, `{"source_grant":"/media/movie.mkv","seek_seconds":1}`},
				"trickplay": {server.handleTrickplayExtract, sheetsRequestJSON(t, "/media/movie.mkv")},
			} {
				handle, body := test.handle, test.body
				rr := httptest.NewRecorder()
				handle(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
				if rr.Code != http.StatusServiceUnavailable {
					t.Fatalf("%s: status %d %s", route, rr.Code, rr.Body.String())
				}
			}
			if calls := sources.calls.Load(); calls != 0 {
				t.Fatalf("refused work opened its source %d times", calls)
			}
		})
	}
}

// Serving a prepared download that already exists needs no source: the node
// neither asks the core nor fails when the source is gone.
func TestDownloadPrepareReusesAnArtifactWithoutItsSource(t *testing.T) {
	server := newTestServer(t)
	sources := &countingSources{err: &nodesource.StatusError{Op: "open", Status: http.StatusGone}}
	server.sources = sources
	if err := os.MkdirAll(server.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	const artifactID = "artifact-reused-without-source"
	if err := os.WriteFile(filepath.Join(server.artifactRoot, artifactID+".mp4"), []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(downloadprepare.Request{ArtifactID: artifactID, SourceGrant: "/media/gone.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac"})
	rr := httptest.NewRecorder()
	server.handleDownloadPrepare(rr, httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(body)))
	var result downloadprepare.Result
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &result) != nil || result.ArtifactID != artifactID || result.FileSize != 8 {
		t.Fatalf("reuse = %d %s", rr.Code, rr.Body.String())
	}
	if calls := sources.calls.Load(); calls != 0 {
		t.Fatalf("reusing an artifact opened its source %d times", calls)
	}
}
