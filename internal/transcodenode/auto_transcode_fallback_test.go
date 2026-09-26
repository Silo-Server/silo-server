package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// writeNodeFFmpegFailingOn writes a fake FFmpeg that exits before its first
// manifest when its arguments contain failPattern and otherwise writes a
// playable manifest and keeps running. Every transcode invocation is logged.
func writeNodeFFmpegFailingOn(t *testing.T, failPattern string) (ffmpegPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	ffmpegPath = filepath.Join(dir, "fake-ffmpeg.sh")
	logPath = filepath.Join(dir, "invocations.log")
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \"$last\" in *.m3u8) printf '%s\\n' \"$*\" >> \"" + logPath + "\" ;; esac\n" +
		"case \"$*\" in *'" + failPattern + "'*) echo 'intentional hardware failure' >&2; exit 1 ;; esac\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; " +
		"printf x > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\" ;;\n" +
		"esac\n" +
		"sleep 30\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ffmpegPath, logPath
}

func readNodeFFmpegInvocations(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// useAutoNVENCPipeline stands in for hw_accel=auto resolving to NVENC, which
// the test host cannot probe.
func useAutoNVENCPipeline(t *testing.T, server *Server) {
	server.autoTranscodePipelineFn = func(_ context.Context, opts playback.TranscodeOpts) *playback.AutoTranscodePipeline {
		if opts.HWAccel != "auto" {
			t.Errorf("pipeline built from HWAccel %q, want auto", opts.HWAccel)
		}
		opts.HWAccel = "nvenc"
		return playback.NewResolvedAutoTranscodePipelineForTest(opts)
	}
}

func postAutoTranscodeStart(t *testing.T, server *Server, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	return postAutoTranscodeStartRequest(t, server, TranscodeStartRequest{SessionID: sessionID, RequireReady: true})
}

// postAutoTranscodeStartRequest posts an auto video transcode with the
// readiness fields and session id taken from req.
func postAutoTranscodeStartRequest(t *testing.T, server *Server, req TranscodeStartRequest) *httptest.ResponseRecorder {
	t.Helper()
	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID:         req.SessionID,
		InputPath:         "/media/movie.mkv",
		TargetCodecVideo:  "h264",
		TargetCodecAudio:  "aac",
		TargetResolution:  "720p",
		SegmentDuration:   2,
		HWAccel:           "auto",
		RequireReady:      req.RequireReady,
		AutoFallbackReady: req.AutoFallbackReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	server.handleStart(rr, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody)))
	return rr
}

func TestHandleStartRequireReadyAutoKeepsGPUEncodeWithCPUDecode(t *testing.T) {
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	ffmpegPath, logPath := writeNodeFFmpegFailingOn(t, "-hwaccel cuda")
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	useAutoNVENCPipeline(t, server)

	rr := postAutoTranscodeStart(t, server, "auto-mixed-1")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	session := server.sessions["auto-mixed-1"]
	server.mu.RUnlock()
	if session == nil {
		t.Fatal("ready fallback session was not registered")
	}
	defer func() { _ = session.Close() }()

	var response TranscodeStartResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.HWAccel != "nvenc" || !response.SoftwareVideoDecode {
		t.Fatalf("response = %+v, want NVENC with software_video_decode", response)
	}
	if got := session.Opts().OutputDir; got != server.sessionOutputDir("auto-mixed-1") {
		t.Fatalf("session output dir = %q, want the session directory", got)
	}
	invocations := readNodeFFmpegInvocations(t, logPath)
	if len(invocations) != 2 || strings.Contains(invocations[1], "-hwaccel cuda") || !strings.Contains(invocations[1], "h264_nvenc") {
		t.Fatalf("invocations = %q, want full hardware then CPU decode with NVENC", invocations)
	}
}

func TestHandleStartAutoReplacementFailureLeavesLiveSession(t *testing.T) {
	server := newTestServer(t)
	ffmpegPath, logPath := writeNodeFFmpegFailingOn(t, "-f hls")
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	useAutoNVENCPipeline(t, server)

	const sessionID = "auto-swap-1"
	liveDir := server.sessionOutputDir(sessionID)
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	liveSegment := filepath.Join(liveDir, "seg_0.m4s")
	if err := os.WriteFile(liveSegment, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := playback.NewTranscodeSessionForTest(liveDir)
	server.sessions[sessionID] = live

	rr := postAutoTranscodeStart(t, server, sessionID)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "did not become ready") {
		t.Fatalf("status = %d, body = %s; want readiness failure", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	registered := server.sessions[sessionID]
	server.mu.RUnlock()
	if registered != live {
		t.Fatal("failed replacement disturbed the live session")
	}
	if _, err := os.Stat(liveSegment); err != nil {
		t.Fatalf("failed replacement touched the live output: %v", err)
	}
	invocations := readNodeFFmpegInvocations(t, logPath)
	if len(invocations) != 3 || !strings.Contains(invocations[2], "libx264") {
		t.Fatalf("invocations = %q, want full hardware, mixed, then software", invocations)
	}
	for _, invocation := range invocations {
		if !strings.Contains(invocation, sessionID+"-replacement-") {
			t.Fatalf("attempt wrote outside the replacement directory: %s", invocation)
		}
	}
	entries, err := os.ReadDir(server.transcodeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "-replacement-") {
			t.Fatalf("failed replacement left %s behind", entry.Name())
		}
	}
}

func TestSpawnReconstructAutoKeepsGPUEncodeWithCPUDecode(t *testing.T) {
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	ffmpegPath, logPath := writeNodeFFmpegFailingOn(t, "-hwaccel cuda")
	cfg := server.watcher.Config()
	cfg.Playback.FFmpegPath = ffmpegPath
	cfg.Playback.HWAccel = "auto"
	useAutoNVENCPipeline(t, server)

	const sessionID = "auto-reconstruct-1"
	card := playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: sessionID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "720p", TargetBitrateKbps: 2000,
		SegmentDuration: 2,
	})
	session, err := server.spawnReconstruct(httptest.NewRequest(http.MethodGet, "/", nil), sessionID, -1, card)
	if err != nil || session == nil {
		t.Fatalf("spawnReconstruct = %v, %v; want the CPU-decode session", session, err)
	}
	defer func() { _ = session.Close() }()
	if opts := session.Opts(); opts.HWAccel != "nvenc" || !opts.SoftwareVideoDecode {
		t.Fatalf("reconstructed = %s software decode %v, want NVENC with CPU decode", opts.HWAccel, opts.SoftwareVideoDecode)
	}
	server.mu.RLock()
	registered := server.sessions[sessionID]
	server.mu.RUnlock()
	if registered != session {
		t.Fatal("reconstructed fallback session was not registered")
	}
	if invocations := readNodeFFmpegInvocations(t, logPath); len(invocations) != 2 {
		t.Fatalf("invocations = %q, want full hardware then CPU decode", invocations)
	}
}

func TestHandleStartRequireReadyKeepsVideoToolboxSoftwareRetry(t *testing.T) {
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	fakePath, logPath := writeNodeFFmpegFailingOn(t, "h264_videotoolbox")
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	// Answer the VideoToolbox capability probes, then hand real transcodes to
	// the fake that fails hardware encodes.
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *-hwaccels*) echo videotoolbox; exit 0 ;;\n" +
		"  *-encoders*) echo ' V..... h264_videotoolbox x'; exit 0 ;;\n" +
		"  *videotoolbox*'-f null'*) exit 0 ;;\n" +
		"esac\n" +
		"exec \"" + fakePath + "\" \"$@\"\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath

	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID: "vt-retry-1", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "720p", TargetBitrateKbps: 2000,
		SegmentDuration: 2, HWAccel: "videotoolbox", RequireReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	server.handleStart(rr, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	session := server.sessions["vt-retry-1"]
	server.mu.RUnlock()
	if session == nil {
		t.Fatal("software retry session was not registered")
	}
	defer func() { _ = session.Close() }()
	invocations := readNodeFFmpegInvocations(t, logPath)
	if len(invocations) != 2 || !strings.Contains(invocations[0], "h264_videotoolbox") || !strings.Contains(invocations[1], "libx264") {
		t.Fatalf("invocations = %q, want VideoToolbox then the legacy software retry", invocations)
	}
}

// auto_fallback_ready lets a node whose live auto pipeline is enabled wait for
// the first manifest and walk the safer paths.
func TestHandleStartAutoFallbackReadyWalksEnabledPipeline(t *testing.T) {
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	ffmpegPath, logPath := writeNodeFFmpegFailingOn(t, "-hwaccel cuda")
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	useAutoNVENCPipeline(t, server)

	rr := postAutoTranscodeStartRequest(t, server, TranscodeStartRequest{SessionID: "auto-fallback-1", AutoFallbackReady: true})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	session := server.sessions["auto-fallback-1"]
	server.mu.RUnlock()
	if session == nil {
		t.Fatal("fallback session was not registered")
	}
	defer func() { _ = session.Close() }()
	if invocations := readNodeFFmpegInvocations(t, logPath); len(invocations) != 2 || strings.Contains(invocations[1], "-hwaccel cuda") {
		t.Fatalf("invocations = %q, want full hardware then CPU decode", invocations)
	}
}

// Without an enabled pipeline, auto_fallback_ready must not wait: a slow
// software encoder would otherwise be closed at the readiness deadline.
func TestHandleStartAutoFallbackReadyWithoutPipelineDoesNotWait(t *testing.T) {
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "http://node", "node", "transcode")
	dir := t.TempDir()
	slowFFmpeg := filepath.Join(dir, "slow-ffmpeg.sh")
	if err := os.WriteFile(slowFFmpeg, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = slowFFmpeg
	server.autoTranscodePipelineFn = func(ctx context.Context, opts playback.TranscodeOpts) *playback.AutoTranscodePipeline {
		opts.HWAccel = playback.HWAccelNone
		return playback.NewAutoTranscodePipeline(ctx, opts)
	}

	rr := postAutoTranscodeStartRequest(t, server, TranscodeStartRequest{SessionID: "auto-software-1", AutoFallbackReady: true})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s; want an unwaited start", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	session := server.sessions["auto-software-1"]
	server.mu.RUnlock()
	if session == nil || !session.IsRunning() {
		t.Fatal("slow software session was not kept running")
	}
	_ = session.Close()
}
