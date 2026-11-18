package transcodenode

import (
	"context"
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
	return writeNodeFFmpegFailingWith(t, failPattern, "intentional hardware failure")
}

// writeNodeFFmpegFailingWith is writeNodeFFmpegFailingOn with the stderr line
// the failing attempt prints.
func writeNodeFFmpegFailingWith(t *testing.T, failPattern, stderr string) (ffmpegPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	ffmpegPath = filepath.Join(dir, "fake-ffmpeg.sh")
	logPath = filepath.Join(dir, "invocations.log")
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \"$last\" in *.m3u8) printf '%s\\n' \"$*\" >> \"" + logPath + "\" ;; esac\n" +
		"case \"$*\" in *'" + failPattern + "'*) echo '" + stderr + "' >&2; exit 1 ;; esac\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; " +
		"printf x > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\" ;;\n" +
		"esac\n" +
		"exec sleep 30\n"
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
	return postStartRequest(t, server, autoTranscodeStartRequest(TranscodeStartRequest{SessionID: sessionID, RequireReady: true}))
}

// autoTranscodeStartRequest is an auto video transcode with the readiness
// fields and session id taken from req.
func autoTranscodeStartRequest(req TranscodeStartRequest) TranscodeStartRequest {
	return TranscodeStartRequest{
		SessionID:         req.SessionID,
		InputPath:         "/media/movie.mkv",
		TargetCodecVideo:  "h264",
		TargetCodecAudio:  "aac",
		TargetResolution:  "720p",
		SegmentDuration:   2,
		HWAccel:           "auto",
		RequireReady:      req.RequireReady,
		AutoFallbackReady: req.AutoFallbackReady,
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

// A node rebuild is shared by every request waiting on it, so once it is
// admitted the request that triggered it can end without failing the others.
// StartTranscode returns an ended request's error before FFmpeg runs for an
// HEVC target, among other recipes.
func TestSpawnReconstructOutlivesTriggeringRequest(t *testing.T) {
	for _, test := range []struct{ name, hwAccel string }{
		// StartupRetryHWAccel changes VideoToolbox, so the node starts it directly.
		{name: "backend with a software retry", hwAccel: "videotoolbox"},
		{name: "start once", hwAccel: playback.HWAccelNone},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			server.tracker = nodesessions.NewLocalTracker()
			fakePath, _ := writeNodeFFmpegFailingOn(t, "no attempt fails")
			// Answer the capability probes, then hand transcodes to the fake.
			useFakeFFmpeg(t, server, "#!/bin/sh\n"+
				"case \"$*\" in\n"+
				"  *-hwaccels*) echo videotoolbox; exit 0 ;;\n"+
				"  *-encoders*) echo ' V..... h264_videotoolbox x'; exit 0 ;;\n"+
				"  *'-f null'*) exit 0 ;;\n"+
				"esac\n"+
				"exec \""+fakePath+"\" \"$@\"\n")
			server.watcher.Config().Playback.HWAccel = test.hwAccel
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Called after admission, right before the start: the request ends here.
			server.autoTranscodePipelineFn = func(stepCtx context.Context, opts playback.TranscodeOpts) *playback.AutoTranscodePipeline {
				cancel()
				if stepCtx.Err() != nil {
					t.Error("fallback order resolved on the ended request's context")
				}
				return playback.NewAutoTranscodePipeline(stepCtx, opts)
			}

			const sessionID = "outlives-request-1"
			card := playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{
				SessionID: sessionID, InputPath: "/media/movie.mkv",
				TargetCodecVideo: "hevc", TargetCodecAudio: "aac", TargetResolution: "720p", SegmentDuration: 2,
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			session, err := server.spawnReconstruct(request, sessionID, -1, resolutionOf(card))
			if err != nil || session == nil {
				t.Fatalf("spawnReconstruct = %v, %v; want the session the other requests wait for", session, err)
			}
			_ = session.Close()
		})
	}
}

func TestHandleStartRequireReadyKeepsVideoToolboxSoftwareRetry(t *testing.T) {
	server := newTestServer(t)
	server.tracker = nodesessions.NewLocalTracker()
	fakePath, logPath := writeNodeFFmpegFailingOn(t, "h264_videotoolbox")
	// Answer the VideoToolbox capability probes, then hand real transcodes to
	// the fake that fails hardware encodes.
	useFakeFFmpeg(t, server, "#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"  *-hwaccels*) echo videotoolbox; exit 0 ;;\n"+
		"  *-encoders*) echo ' V..... h264_videotoolbox x'; exit 0 ;;\n"+
		"  *videotoolbox*'-f null'*) exit 0 ;;\n"+
		"esac\n"+
		"exec \""+fakePath+"\" \"$@\"\n")

	startRegisteredSession(t, server, TranscodeStartRequest{
		SessionID: "vt-retry-1", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "720p", TargetBitrateKbps: 2000,
		SegmentDuration: 2, HWAccel: "videotoolbox", RequireReady: true,
	})
	invocations := readNodeFFmpegInvocations(t, logPath)
	if len(invocations) != 2 || !strings.Contains(invocations[0], "h264_videotoolbox") || !strings.Contains(invocations[1], "libx264") {
		t.Fatalf("invocations = %q, want VideoToolbox then the legacy software retry", invocations)
	}
}
