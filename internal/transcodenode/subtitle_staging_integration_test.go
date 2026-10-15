package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodecontrol/nodecontroltest"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// Text burn-in of a relayed source stages its subtitle on the node, not in
// the start request: these run real FFmpeg on a source served by a core
// whose source reads are throttled, so the staging outlasts a start's
// deadline.

// throttledTransport is a relay's upstream that serves source bytes at
// bytesPerSecond. opens counts the node's source opens at the core, which go
// through the relay's Open rather than this transport.
type throttledTransport struct {
	base           http.RoundTripper
	bytesPerSecond int
	opens          atomic.Int32
}

func (t *throttledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && req.Method == http.MethodGet && req.URL.Path == nodesource.PathSources {
		resp.Body = &slowBody{ReadCloser: resp.Body, bytesPerSecond: t.bytesPerSecond}
	}
	return resp, err
}

type slowBody struct {
	io.ReadCloser
	bytesPerSecond int
}

func (b *slowBody) Read(p []byte) (int, error) {
	const chunk = 8 << 10
	if len(p) > chunk {
		p = p[:chunk]
	}
	n, err := b.ReadCloser.Read(p)
	time.Sleep(time.Duration(n) * time.Second / time.Duration(b.bytesPerSecond))
	return n, err
}

// throttledCoreNode is a node with core source access whose core serves
// files at bytesPerSecond.
func throttledCoreNode(t *testing.T, ffmpeg string, files integrationCatalog, bytesPerSecond int) (integrationNode, *throttledTransport) {
	t.Helper()
	grants := nodesource.NewGrants(func() string { return "integration-grant-secret" })
	coreURL, nodeKey := nodecontroltest.SourceCore(t, integrationNodeID, nodecontrol.Options{Grants: grants, Files: files})
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatal("no HTTP transport")
	}
	upstream := base.Clone()
	upstream.Proxy = nil
	upstream.DisableCompression = true
	transport := &throttledTransport{base: upstream, bytesPerSecond: bytesPerSecond}
	open := openThrough(t, coreURL, nodeKey)
	countedOpen := func(ctx context.Context, grant string) (*nodesource.OpenResponse, error) {
		transport.opens.Add(1)
		return open(ctx, grant)
	}
	relay, err := nodesource.NewRelay(nodesource.RelayOptions{
		Open: countedOpen, CoreURL: coreURL, NodeID: integrationNodeID, NodeKey: nodeKey,
		Mode: func() string { return nodesource.ModeCore }, Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	server.watcher.Config().Playback.FFmpegPath = ffmpeg
	server.watcher.Config().Playback.HWAccel = playback.HWAccelNone
	server.sources = RelaySources(relay)
	return integrationNode{name: nodesource.ModeCore, server: server, relay: relay, grants: grants, files: files}, transport
}

// burnInFixture makes a 4 s H.264 MKV with an ASS track and an attached
// font, rendering only with that font.
func burnInFixture(t *testing.T, ffmpeg string) string {
	t.Helper()
	if !hasEncoder(ffmpeg, "libx264") {
		t.Skip("ffmpeg has no libx264 encoder for the burn-in transcode")
	}
	font, family := attachableFont(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "sub.ass")
	if err := os.WriteFile(script, []byte("[Script Info]\nScriptType: v4.00+\nPlayResX: 320\nPlayResY: 240\n\n[V4+ Styles]\n"+
		"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n"+
		"Style: Default,"+family+",28,&H0000FFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1\n\n"+
		"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"+
		"Dialogue: 0,0:00:00.20,0:00:03.80,Default,,0,0,0,,Staged on the node\n"), 0o600); err != nil { //nolint:misspell // ASS event keyword
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "subtitled.mkv")
	run(t, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4", "-i", script,
		"-attach", font, "-metadata:s:t:0", "mimetype=application/x-truetype-font",
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-g", "25", "-c:a", "aac", "-c:s", "ass", "-shortest", fixture)
	fontconfigWithoutFonts(t)
	return fixture
}

// burnInStart is a start request burning in the fixture's subtitle.
func burnInStart(t *testing.T, node integrationNode, id string, fileID int, requireReady bool) TranscodeStartRequest {
	return TranscodeStartRequest{
		SessionID: id, SourceGrant: node.grant(t, fileID), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		SegmentDuration: 2, TotalDuration: 4, RequireReady: requireReady, HWAccel: playback.HWAccelNone,
		SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "ass",
	}
}

// postTimedStart posts req with a request deadline of timeout and returns the
// recorder and how long the node took to answer.
func postTimedStart(t *testing.T, node integrationNode, req TranscodeStartRequest, timeout time.Duration) (*httptest.ResponseRecorder, time.Duration) {
	t.Helper()
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	rec := httptest.NewRecorder()
	started := time.Now()
	node.server.handleStart(rec, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)).WithContext(ctx))
	return rec, time.Since(started)
}

func liveSession(node integrationNode, id string) *playback.TranscodeSession {
	node.server.mu.RLock()
	defer node.server.mu.RUnlock()
	return node.server.sessions[id]
}

// waitFinished waits for session's FFmpeg to finish and returns how long
// that took.
func waitFinished(t *testing.T, session *playback.TranscodeSession) time.Duration {
	t.Helper()
	started := time.Now()
	deadline := started.Add(2 * time.Minute)
	for session.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("the session never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := session.WaitError(); err != nil {
		t.Fatalf("session: %v", err)
	}
	return time.Since(started)
}

// A start whose staging takes longer than its request may last answers at
// once and launches FFmpeg once the subtitle is staged: the session serves
// FFmpeg's manifest and renders the subtitle as the node's own mount does.
func TestCoreSourceBurnInStagingOutlivesTheStartRequest(t *testing.T) {
	ffmpeg, _ := requireFFmpeg(t)
	fixture := burnInFixture(t, ffmpeg)
	const fileID = 61
	local, _ := integrationNodes(t, ffmpeg, integrationCatalog{fileID: fixture})
	info, err := os.Stat(fixture)
	if err != nil {
		t.Fatal(err)
	}
	// About a second to read the whole source once: the staging reads it
	// whole, then the font dump its header.
	rate := int(info.Size())
	core, _ := throttledCoreNode(t, ffmpeg, integrationCatalog{fileID: fixture}, rate)

	const deadline = 300 * time.Millisecond
	rec, answered := postTimedStart(t, core, burnInStart(t, core, "staged-core", fileID, false), deadline)
	if rec.Code != http.StatusAccepted || answered >= deadline {
		t.Fatalf("start = %d %s after %s; want 202 within the %s request", rec.Code, rec.Body.String(), answered, deadline)
	}
	t.Cleanup(func() { stopSession(t, core.server, "staged-core") })
	session := liveSession(core, "staged-core")
	if session == nil || !session.IsRunning() || !session.SegmentProgress(time.Now()).Preparing {
		t.Fatal("the started session is not preparing while its subtitle stages")
	}

	// The client's first requests wait for the session as for a slow
	// FFmpeg start.
	manifest := httptest.NewRecorder()
	core.server.Handler().ServeHTTP(manifest, signed(t, httptest.NewRequest(http.MethodGet, "/transcode/staged-core/master.m3u8", nil)))
	if manifest.Code != http.StatusOK || !strings.Contains(manifest.Body.String(), "#EXTM3U") {
		t.Fatalf("served manifest = %d %s", manifest.Code, manifest.Body.String())
	}
	first := ""
	for _, line := range strings.Split(manifest.Body.String(), "\n") {
		if strings.HasPrefix(line, "segment/") {
			first, _, _ = strings.Cut(line, "?")
			break
		}
	}
	segmentStarted := time.Now()
	segment := httptest.NewRecorder()
	core.server.Handler().ServeHTTP(segment, signed(t, httptest.NewRequest(http.MethodGet, "/transcode/staged-core/"+first, nil)))
	if segment.Code != http.StatusOK || segment.Body.Len() == 0 {
		t.Fatalf("first segment %s = %d %s", first, segment.Code, segment.Body.String())
	}
	t.Logf("source %d bytes at %d B/s: start answered in %s; its first segment, requested while preparing, served %s later",
		info.Size(), rate, answered.Round(time.Millisecond), time.Since(segmentStarted).Round(time.Millisecond))
	waitFinished(t, session)
	got := hlsVideo(t, ffmpeg, session.Opts().OutputDir)
	localDir, _ := startSession(t, local, burnInStart(t, local, "staged-local", fileID, true))
	if want := hlsVideo(t, ffmpeg, localDir); got != want || got == "" {
		t.Fatalf("staged burn-in differs from the node's own mount:\nlocal:\n%.800s\ncore:\n%.800s", want, got)
	}
}

// A second start on the same source and track joins the staging the first
// began, even after the first gave up waiting for it: the core sees one
// staging read of the source, and a third start reuses the staging done.
func TestCoreSourceBurnInStartsShareOneStaging(t *testing.T) {
	ffmpeg, _ := requireFFmpeg(t)
	fixture := burnInFixture(t, ffmpeg)
	const fileID = 62
	info, err := os.Stat(fixture)
	if err != nil {
		t.Fatal(err)
	}
	core, transport := throttledCoreNode(t, ffmpeg, integrationCatalog{fileID: fixture}, int(info.Size()))

	// A readiness-gated start whose request ends first fails, as any start
	// that is not ready in time does; its staging goes on.
	if rec, _ := postTimedStart(t, core, burnInStart(t, core, "shared-1", fileID, true), 300*time.Millisecond); rec.Code == http.StatusAccepted {
		t.Fatalf("a readiness-gated start answered before its subtitle was staged: %d", rec.Code)
	}
	rec, _ := postTimedStart(t, core, burnInStart(t, core, "shared-2", fileID, false), time.Minute)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second start = %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { stopSession(t, core.server, "shared-2") })
	waitFinished(t, liveSession(core, "shared-2"))
	// Each start opens its own input; one staging opened one more.
	if opens := transport.opens.Load(); opens != 3 {
		t.Fatalf("%d source opens at the core for two starts, want 3 (one staging)", opens)
	}

	rec, answered := postTimedStart(t, core, burnInStart(t, core, "shared-3", fileID, true), time.Minute)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("third start = %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { stopSession(t, core.server, "shared-3") })
	if opens := transport.opens.Load(); opens != 4 {
		t.Fatalf("%d source opens after a third start, want 4 (no new staging)", opens)
	}
	t.Logf("a start reusing the staging was ready in %s", answered.Round(time.Millisecond))
	staged, _ := filepath.Glob(filepath.Join(core.server.transcodeDir, playback.SubtitleStagingDirName, "*", "fonts", "*"))
	if len(staged) != 1 {
		t.Fatalf("staged fonts %q, want one staging's font", staged)
	}
}

// A stop while the subtitle stages cancels the pending launch: the session
// ends without starting FFmpeg, no goroutine waits on for it, and once the
// staging, which runs on, is done, the node holds no source handle.
func TestCoreSourceStopDuringStagingCancelsTheLaunch(t *testing.T) {
	ffmpeg, _ := requireFFmpeg(t)
	fixture := burnInFixture(t, ffmpeg)
	const fileID = 63
	info, err := os.Stat(fixture)
	if err != nil {
		t.Fatal(err)
	}
	core, _ := throttledCoreNode(t, ffmpeg, integrationCatalog{fileID: fixture}, int(info.Size()))

	rec, _ := postTimedStart(t, core, burnInStart(t, core, "stopped", fileID, false), time.Minute)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	session := liveSession(core, "stopped")
	if !session.SegmentProgress(time.Now()).Preparing {
		t.Fatal("the session is not preparing")
	}
	stop := httptest.NewRecorder()
	core.server.Handler().ServeHTTP(stop, signed(t, httptest.NewRequest(http.MethodDelete, "/transcode/stopped", nil)))
	if stop.Code != http.StatusNoContent {
		t.Fatalf("stop = %d", stop.Code)
	}
	select {
	case <-session.Done():
	default:
		t.Fatal("the stopped session's generation has not ended")
	}
	if session.IsRunning() || !errors.Is(session.WaitError(), context.Canceled) {
		t.Fatalf("stopped session: running %t, %v", session.IsRunning(), session.WaitError())
	}
	if launching := goroutinesIn("launchWhenStaged"); launching != 0 {
		t.Fatalf("%d pending launches left after the stop", launching)
	}

	// The staging is the node's: it completes, then releases its handle.
	deadline := time.Now().Add(time.Minute)
	for {
		staged, _ := filepath.Glob(filepath.Join(core.server.transcodeDir, playback.SubtitleStagingDirName, "*", "selected.ass"))
		if len(staged) == 1 && goroutinesIn("renewLoop") == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the stop: staged %q, %d source handles open", staged, goroutinesIn("renewLoop"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(core.server.sessionOutputDir("stopped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stopped session's output exists: %v", err)
	}
	if launching := goroutinesIn("launchWhenStaged"); launching != 0 {
		t.Fatalf("%d pending launches after the staging", launching)
	}
}

// goroutinesIn counts the goroutines whose stack runs fn.
func goroutinesIn(fn string) int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	count := 0
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(stack, "."+fn+"(") {
			count++
		}
	}
	return count
}
