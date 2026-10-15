package transcodenode

import (
	"bytes"
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
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Text burn-in of a relayed source fetches its subtitle bundle from the core,
// which builds it from its own copy of the source: these run real FFmpeg on
// both sides, with the core's bundle build held behind a gate so it outlasts
// a start's deadline.

// gatedCoreFFmpeg returns an FFmpeg for the core that logs each run's
// arguments to runs and, for the subtitle extraction, waits for gate to exist
// before it runs ffmpeg; ffprobe beside it is the real one.
func gatedCoreFFmpeg(t *testing.T, ffmpeg, ffprobe string) (bin, gate, runs string) {
	t.Helper()
	dir := t.TempDir()
	bin, gate, runs = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "gate"), filepath.Join(dir, "runs")
	script := "#!/bin/sh\necho \"$*\" >> '" + runs + "'\n" +
		"case \"$*\" in *-copyts*) while [ ! -e '" + gate + "' ]; do sleep 0.01; done ;; esac\n" +
		"exec '" + ffmpeg + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ffprobe, filepath.Join(dir, "ffprobe")); err != nil {
		t.Fatal(err)
	}
	return bin, gate, runs
}

func openGate(t *testing.T, gate string) {
	t.Helper()
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// extractions counts the core's subtitle extractions in runs.
func extractions(t *testing.T, runs string) int {
	t.Helper()
	data, err := os.ReadFile(runs)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "-copyts")
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
		"Dialogue: 0,0:00:00.20,0:00:03.80,Default,,0,0,0,,Staged by the core\n"), 0o600); err != nil { //nolint:misspell // ASS event keyword
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

// waitFinished waits for session's FFmpeg to finish.
func waitFinished(t *testing.T, session *playback.TranscodeSession) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for session.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("the session never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := session.WaitError(); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// A start whose bundle the core is still building answers at once and
// launches FFmpeg once the bundle is fetched: the client's first requests
// wait for it as for a slow FFmpeg start, and the subtitle renders as it does
// from the node's own mount.
func TestCoreSourceBurnInWaitsForTheCoresBundle(t *testing.T) {
	ffmpeg, ffprobe := requireFFmpeg(t)
	fixture := burnInFixture(t, ffmpeg)
	const fileID = 61
	coreFFmpeg, gate, _ := gatedCoreFFmpeg(t, ffmpeg, ffprobe)
	local, core := integrationNodesWithCoreFFmpeg(t, ffmpeg, coreFFmpeg, integrationCatalog{fileID: fixture})

	const deadline = 300 * time.Millisecond
	rec, answered := postTimedStart(t, core, burnInStart(t, core, "staged-core", fileID, false), deadline)
	if rec.Code != http.StatusAccepted || answered >= deadline {
		t.Fatalf("start = %d %s after %s; want 202 within the %s request", rec.Code, rec.Body.String(), answered, deadline)
	}
	t.Cleanup(func() { stopSession(t, core.server, "staged-core") })
	session := liveSession(core, "staged-core")
	if session == nil || !session.IsRunning() || !session.SegmentProgress(time.Now()).Preparing {
		t.Fatal("the started session is not preparing while the core builds its bundle")
	}
	time.AfterFunc(500*time.Millisecond, func() { openGate(t, gate) })

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
	segment := httptest.NewRecorder()
	core.server.Handler().ServeHTTP(segment, signed(t, httptest.NewRequest(http.MethodGet, "/transcode/staged-core/"+first, nil)))
	if segment.Code != http.StatusOK || segment.Body.Len() == 0 {
		t.Fatalf("first segment %s = %d %s", first, segment.Code, segment.Body.String())
	}
	waitFinished(t, session)
	got := hlsVideo(t, ffmpeg, session.Opts().OutputDir)
	localDir, _ := startSession(t, local, burnInStart(t, local, "staged-local", fileID, true))
	if want := hlsVideo(t, ffmpeg, localDir); got != want || got == "" {
		t.Fatalf("burn-in from the core's bundle differs from the node's own mount:\nlocal:\n%.800s\ncore:\n%.800s", want, got)
	}
}

// Starts on the same source and track share the core's one extraction: a
// readiness-gated start that gives up waiting leaves the build running, a
// second start gets the bundle it built, and a third is ready from the
// core's cache.
func TestCoreSourceBurnInStartsShareOneExtraction(t *testing.T) {
	ffmpeg, ffprobe := requireFFmpeg(t)
	fixture := burnInFixture(t, ffmpeg)
	const fileID = 62
	coreFFmpeg, gate, runs := gatedCoreFFmpeg(t, ffmpeg, ffprobe)
	_, core := integrationNodesWithCoreFFmpeg(t, ffmpeg, coreFFmpeg, integrationCatalog{fileID: fixture})

	if rec, _ := postTimedStart(t, core, burnInStart(t, core, "shared-1", fileID, true), 300*time.Millisecond); rec.Code == http.StatusAccepted {
		t.Fatalf("a readiness-gated start answered before its subtitle was staged: %d", rec.Code)
	}
	rec, _ := postTimedStart(t, core, burnInStart(t, core, "shared-2", fileID, false), time.Minute)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second start = %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { stopSession(t, core.server, "shared-2") })
	openGate(t, gate)
	waitFinished(t, liveSession(core, "shared-2"))

	rec, answered := postTimedStart(t, core, burnInStart(t, core, "shared-3", fileID, true), time.Minute)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("third start = %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { stopSession(t, core.server, "shared-3") })
	if n := extractions(t, runs); n != 1 {
		t.Fatalf("%d subtitle extractions on the core for three starts, want 1", n)
	}
	t.Logf("a start served from the core's cache was ready in %s", answered.Round(time.Millisecond))
}

// A stop while the bundle is fetched cancels the pending launch and the
// fetch: the session ends without starting FFmpeg, nothing waits on for it,
// and the node holds no source handle. The core's build goes on, so the
// next start is served from its cache.
func TestCoreSourceStopDuringStagingCancelsTheLaunch(t *testing.T) {
	ffmpeg, ffprobe := requireFFmpeg(t)
	fixture := burnInFixture(t, ffmpeg)
	const fileID = 63
	coreFFmpeg, gate, runs := gatedCoreFFmpeg(t, ffmpeg, ffprobe)
	_, core := integrationNodesWithCoreFFmpeg(t, ffmpeg, coreFFmpeg, integrationCatalog{fileID: fixture})

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
	deadline := time.Now().Add(10 * time.Second)
	for goroutinesIn("launchWhenStaged")+goroutinesIn("fetchSubtitleBundle")+goroutinesIn("renewLoop") != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("after the stop: %d launches, %d fetches, %d source handles", goroutinesIn("launchWhenStaged"),
				goroutinesIn("fetchSubtitleBundle"), goroutinesIn("renewLoop"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(core.server.sessionOutputDir("stopped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stopped session's output exists: %v", err)
	}

	openGate(t, gate)
	rec, _ = postTimedStart(t, core, burnInStart(t, core, "after-stop", fileID, true), time.Minute)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start after the stop = %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { stopSession(t, core.server, "after-stop") })
	if n := extractions(t, runs); n != 1 {
		t.Fatalf("%d subtitle extractions on the core, want 1", n)
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
