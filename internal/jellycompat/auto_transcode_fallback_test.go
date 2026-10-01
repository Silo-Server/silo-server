package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// writeCompatFFmpegFailingOn writes a fake FFmpeg that exits before its first
// manifest when a transcode's arguments contain failPattern and otherwise
// writes a playable manifest and keeps running. Every transcode is logged.
func writeCompatFFmpegFailingOn(t *testing.T, failPattern string) (ffmpegPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	ffmpegPath = filepath.Join(dir, "fake-ffmpeg.sh")
	logPath = filepath.Join(dir, "invocations.log")
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \"$last\" in *.m3u8) ;; *) exit 0 ;; esac\n" +
		"printf '%s\\n' \"$*\" >> \"" + logPath + "\"\n" +
		"case \"$*\" in *'" + failPattern + "'*) echo 'intentional hardware failure' >&2; exit 1 ;; esac\n" +
		"out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"\n" +
		"printf x > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"\n" +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\"\n" +
		"sleep 30\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ffmpegPath, logPath
}

func readCompatFFmpegInvocations(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// newAutoCompatLocalHandler builds a local compat handler whose hw_accel=auto
// resolves to NVENC, which the test host cannot probe.
func newAutoCompatLocalHandler(t *testing.T, ffmpegPath string) (*PlaybackHandler, PlaybackMediaSource) {
	t.Helper()
	version := testCompatVersion()
	source := testCompatSource(NewResourceIDCodec(), version)
	filePath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{ID: "play-1", MediaSources: []PlaybackMediaSource{source}})
	handler := &PlaybackHandler{
		playbackStore: playbackStore,
		sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{
			"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode},
		}},
		fileResolver: testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: filePath}},
		TranscodeDir: t.TempDir(),
		FFmpegPath:   ffmpegPath,
		HWAccel:      "auto",
		tm:           playback.NewTranscodeManager(),
	}
	handler.compatAutoTranscodePipeline = func(_ context.Context, opts playback.TranscodeOpts) *playback.AutoTranscodePipeline {
		if opts.HWAccel != "auto" {
			t.Errorf("pipeline built from HWAccel %q, want auto", opts.HWAccel)
		}
		opts.HWAccel = "nvenc"
		return playback.NewResolvedAutoTranscodePipelineForTest(opts)
	}
	return handler, source
}

func TestEnsureTranscodeSessionAutoKeepsGPUEncodeWithCPUDecode(t *testing.T) {
	ffmpegPath, logPath := writeCompatFFmpegFailingOn(t, "-hwaccel cuda")
	handler, source := newAutoCompatLocalHandler(t, ffmpegPath)

	transcodeSession, err := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatalf("ensureTranscodeSession: %v", err)
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })

	if opts := transcodeSession.Opts(); opts.HWAccel != "nvenc" || !opts.SoftwareVideoDecode {
		t.Fatalf("session = %s software decode %v, want NVENC with CPU decode", opts.HWAccel, opts.SoftwareVideoDecode)
	}
	if live := handler.tm.GetTranscodeSession("upstream-1"); live != transcodeSession {
		t.Fatal("fallback session is not the registered runtime")
	}
	persisted, ok := handler.playbackStore.Get("play-1")
	if !ok || persisted.Recipe == nil || !persisted.Recipe.SoftwareVideoDecode {
		t.Fatalf("persisted recipe = %#v, want the executed CPU decode", persisted.Recipe)
	}
	invocations := readCompatFFmpegInvocations(t, logPath)
	if len(invocations) != 2 || strings.Contains(invocations[1], "-hwaccel cuda") || !strings.Contains(invocations[1], "h264_nvenc") {
		t.Fatalf("invocations = %q, want full hardware then CPU decode with NVENC", invocations)
	}
}

// A reused output directory still holds an earlier generation's manifest and
// segments; they must not make an exited full-hardware attempt look ready.
func TestEnsureTranscodeSessionAutoIgnoresPreviousGenerationManifest(t *testing.T) {
	ffmpegPath, logPath := writeCompatFFmpegFailingOn(t, "-hwaccel cuda")
	handler, source := newAutoCompatLocalHandler(t, ffmpegPath)
	outputDir := filepath.Join(handler.TranscodeDir, "upstream-1")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for name, data := range map[string]string{
		"stream.m3u8":  "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\nseg_00000.ts\n",
		"seg_00000.ts": "segment",
	} {
		path := filepath.Join(outputDir, name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	transcodeSession, err := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatalf("ensureTranscodeSession: %v", err)
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })
	if !transcodeSession.IsRunning() {
		t.Fatal("ensureTranscodeSession returned an exited process")
	}
	if opts := transcodeSession.Opts(); opts.HWAccel != "nvenc" || !opts.SoftwareVideoDecode {
		t.Fatalf("session = %s software decode %v, want NVENC with CPU decode", opts.HWAccel, opts.SoftwareVideoDecode)
	}
	if invocations := readCompatFFmpegInvocations(t, logPath); len(invocations) != 2 {
		t.Fatalf("invocations = %q, want full hardware then CPU decode with NVENC", invocations)
	}
}

func TestEnsureTranscodeSessionAutoFinalFailureUnregisters(t *testing.T) {
	ffmpegPath, logPath := writeCompatFFmpegFailingOn(t, "-f hls")
	handler, source := newAutoCompatLocalHandler(t, ffmpegPath)

	transcodeSession, err := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", source)
	if err == nil || transcodeSession != nil {
		t.Fatalf("ensureTranscodeSession = %v, %v; want the final startup failure", transcodeSession, err)
	}
	if live := handler.tm.GetTranscodeSession("upstream-1"); live != nil {
		t.Fatal("failed automatic startup left a registered runtime")
	}
	invocations := readCompatFFmpegInvocations(t, logPath)
	if len(invocations) != 3 || !strings.Contains(invocations[2], "libx264") {
		t.Fatalf("invocations = %q, want full hardware, mixed, then software", invocations)
	}
}

// startCompatRemoteAutoTranscode dispatches one auto video transcode to a fake
// node described by report and returns the request the node received.
func startCompatRemoteAutoTranscode(t *testing.T, report string, response transcodenode.TranscodeStartResponse) (transcodenode.TranscodeStartRequest, *PlaybackSessionStore) {
	t.Helper()
	version := testCompatVersion()
	source := testCompatSource(NewResourceIDCodec(), version)
	var remoteReq transcodenode.TranscodeStartRequest
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/transcode/start" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&remoteReq); err != nil {
			t.Errorf("decode remote request: %v", err)
		}
		response.SessionID = remoteReq.SessionID
		writeJSON(w, http.StatusAccepted, response)
	}))
	defer node.Close()

	pooled := &nodepool.Node{URL: node.URL}
	if report != "" {
		pooled.Capabilities = json.RawMessage(report)
	}
	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	handler := &PlaybackHandler{
		JWTSecret:     "secret",
		HWAccel:       "auto",
		NodePlanner:   &nodeLookupPlannerStub{node: pooled},
		playbackStore: playbackStore,
		sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{
			"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode},
		}},
		tm: playback.NewTranscodeManager(),
	}
	file := &models.MediaFile{ID: version.FileID, FilePath: "/media/movie.mkv"}
	if err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", source, file, 0, node.URL); err != nil {
		t.Fatalf("startRemoteTranscode: %v", err)
	}
	return remoteReq, playbackStore
}

func TestStartRemoteTranscodeAutoRequestsFallbackReadinessAndRecordsDecodePath(t *testing.T) {
	remoteReq, playbackStore := startCompatRemoteAutoTranscode(t, `{"resolved":"nvenc"}`, transcodenode.TranscodeStartResponse{
		Status: "started", HWAccel: "nvenc", SoftwareVideoDecode: true,
	})
	if !remoteReq.AutoFallbackReady || remoteReq.RequireReady {
		t.Fatalf("request = %+v, want auto_fallback_ready without require_ready", remoteReq)
	}
	persisted, ok := playbackStore.Get("play-1")
	if !ok || persisted.Recipe == nil || !persisted.Recipe.SoftwareVideoDecode || persisted.Recipe.HWAccel != "nvenc" {
		t.Fatalf("persisted recipe = %#v, want the node's NVENC with CPU decode", persisted.Recipe)
	}
}

// The node resolves auto against its live hardware, so a missing or software
// stored report must not keep the fallback from the node.
func TestStartRemoteTranscodeAutoLeavesFallbackDecisionToNode(t *testing.T) {
	for _, report := range []string{"", `{"resolved":"none"}`} {
		remoteReq, _ := startCompatRemoteAutoTranscode(t, report, transcodenode.TranscodeStartResponse{
			Status: "started", HWAccel: "none",
		})
		if !remoteReq.AutoFallbackReady || remoteReq.RequireReady {
			t.Errorf("report %q: request = %+v, want auto_fallback_ready without require_ready", report, remoteReq)
		}
	}
}

func TestCompatRemoteAutoFallbackEligible(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request transcodenode.TranscodeStartRequest
		want    bool
	}{
		{"auto video", transcodenode.TranscodeStartRequest{TargetCodecVideo: "h264", HWAccel: " AUTO "}, true},
		{"node default", transcodenode.TranscodeStartRequest{TargetCodecVideo: "h264"}, false},
		{"explicit accel", transcodenode.TranscodeStartRequest{TargetCodecVideo: "h264", HWAccel: "qsv"}, false},
		{"video copy", transcodenode.TranscodeStartRequest{TargetCodecVideo: "copy", HWAccel: "auto"}, false},
		{"tone map", transcodenode.TranscodeStartRequest{TargetCodecVideo: "h264", HWAccel: "auto", ToneMapMode: "software"}, false},
	} {
		if got := compatRemoteAutoFallbackEligible(tc.request); got != tc.want {
			t.Errorf("%s: eligible = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRemoteTranscodeStartTimeoutCoversNodeReadiness(t *testing.T) {
	handler := &PlaybackHandler{}
	if got := handler.remoteTranscodeStartTimeout(transcodenode.TranscodeStartRequest{}, 0); got != 20*time.Second {
		t.Fatalf("unready timeout = %v, want 20s", got)
	}
	// The node may wait on one manifest per automatic execution path.
	want := 20*time.Second + transcodenode.TranscodeStartReadyMaxDuration
	if got := handler.remoteTranscodeStartTimeout(transcodenode.TranscodeStartRequest{RequireReady: true}, 0); got != want {
		t.Fatalf("ready timeout = %v, want %v", got, want)
	}
	if got := handler.remoteTranscodeStartTimeout(transcodenode.TranscodeStartRequest{AutoFallbackReady: true}, 0); got != want {
		t.Fatalf("auto fallback timeout = %v, want %v", got, want)
	}
	if min := time.Duration(playback.MaxAutoTranscodeStartupAttempts) * transcodenode.TranscodeStartReadinessTimeout; want < min {
		t.Fatalf("ready timeout = %v, want at least %v", want, min)
	}
}
