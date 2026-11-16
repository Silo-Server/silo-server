package playback

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// noVideoFramesManifest is a live (end-list-less) startup window: three
// complete segments FFmpeg wrote and has not finished. It is what a generation
// that muxes audio but decodes no picture leaves on disk.
const noVideoFramesManifest = "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n" +
	"#EXTINF:2.000000,\nseg_00000.ts\n#EXTINF:2.000000,\nseg_00001.ts\n#EXTINF:2.000000,\nseg_00002.ts\n"

// noVideoFramesSession builds a session whose running generation has muxed a
// startup window and reported frames decoded video frames on its -progress
// stream.
func noVideoFramesSession(t *testing.T, opts TranscodeOpts, age time.Duration, frames int64) *TranscodeSession {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"seg_00000.ts", "seg_00001.ts", "seg_00002.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("segment-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(noVideoFramesManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &TranscodeSession{
		opts:                opts,
		outputDir:           dir,
		running:             true,
		stderr:              newBoundedTailBuffer(stderrTailMaxBytes),
		generationStartedAt: time.Now().Add(-age),
		videoFrames:         frames,
	}
	return session
}

// TestVideoEncodeWithoutFramesIsNotReady pins the readiness verdict: muxed
// segments alone never make a video encode ready, a generation that keeps
// muxing past the grace without a frame is the typed verdict, and every case
// that cannot answer the question keeps today's behavior.
func TestVideoEncodeWithoutFramesIsNotReady(t *testing.T) {
	encodeOpts := func() TranscodeOpts {
		return TranscodeOpts{TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2}
	}

	t.Run("inside the grace holds the start", func(t *testing.T) {
		session := noVideoFramesSession(t, encodeOpts(), noVideoFramesGrace/2, 0)
		if _, err := session.GetManifest(); !errors.Is(err, ErrManifestNotReady) {
			t.Fatalf("GetManifest err = %v, want ErrManifestNotReady inside the grace", err)
		}
	})

	t.Run("past the grace is the typed verdict", func(t *testing.T) {
		session := noVideoFramesSession(t, encodeOpts(), 2*noVideoFramesGrace, 0)
		_, err := session.GetManifest()
		if !errors.Is(err, ErrTranscodeNoVideoFrames) {
			t.Fatalf("GetManifest err = %v, want ErrTranscodeNoVideoFrames", err)
		}
		if errors.Is(err, ErrManifestNotReady) {
			t.Fatal("the typed verdict must not also read as a plain not-ready hold")
		}
	})

	t.Run("a decoded frame makes it ready", func(t *testing.T) {
		session := noVideoFramesSession(t, encodeOpts(), 2*noVideoFramesGrace, 1)
		if _, err := session.GetManifest(); err != nil {
			t.Fatalf("GetManifest err = %v, want the playlist for a decoding generation", err)
		}
	})

	t.Run("a copy plan is never gated", func(t *testing.T) {
		// Copy/remux passes no -progress and decodes nothing, so a zero frame
		// count says nothing about it.
		opts := TranscodeOpts{TargetCodecVideo: "copy", SegmentDuration: 2}
		session := noVideoFramesSession(t, opts, 2*noVideoFramesGrace, 0)
		if _, err := session.GetManifest(); err != nil {
			t.Fatalf("GetManifest err = %v, want the playlist for a copy plan", err)
		}
	})

	t.Run("a completed playlist is tolerated", func(t *testing.T) {
		// A very short clip, or a seek that lands in the last fragment, can
		// legitimately finish with an audio-only tail: startupFilesReady already
		// accepts an end-listed window below the live runway, so the frame gate
		// follows it instead of failing content that is already complete.
		session := noVideoFramesSession(t, encodeOpts(), 2*noVideoFramesGrace, 0)
		endListed := noVideoFramesManifest + "#EXT-X-ENDLIST\n"
		if err := os.WriteFile(filepath.Join(session.outputDir, "stream.m3u8"), []byte(endListed), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := session.GetManifest(); err != nil {
			t.Fatalf("GetManifest err = %v, want the completed playlist", err)
		}
	})

	t.Run("a finished generation is judged by its exit", func(t *testing.T) {
		session := noVideoFramesSession(t, encodeOpts(), 2*noVideoFramesGrace, 0)
		session.mu.Lock()
		session.running = false
		session.mu.Unlock()
		_, err := session.GetManifest()
		if errors.Is(err, ErrTranscodeNoVideoFrames) {
			t.Fatal("a finished generation must report its exit, not the frame verdict")
		}
	})

	t.Run("a session with no spawned generation is not gated", func(t *testing.T) {
		session := noVideoFramesSession(t, encodeOpts(), 0, 0)
		session.mu.Lock()
		session.generationStartedAt = time.Time{}
		session.mu.Unlock()
		if _, err := session.GetManifest(); err != nil {
			t.Fatalf("GetManifest err = %v, want the playlist for an unspawned fixture", err)
		}
	})
}

// TestVideoFramesReportedOnSegmentProgress proves the frame counter is part of
// the progress the recovery machinery reads, so a caller can see that a session
// is muxing without decoding.
func TestVideoFramesReportedOnSegmentProgress(t *testing.T) {
	session := noVideoFramesSession(t, TranscodeOpts{TargetCodecVideo: "h264"}, time.Second, 0)
	if got := session.SegmentProgress(time.Now()); got.VideoFrames != 0 || got.ProducedCount != 3 {
		t.Fatalf("progress = {produced %d, frames %d}, want 3 produced with 0 frames", got.ProducedCount, got.VideoFrames)
	}
	session.mu.Lock()
	session.videoFrames = 42
	session.mu.Unlock()
	if got := session.SegmentProgress(time.Now()); got.VideoFrames != 42 {
		t.Fatalf("VideoFrames = %d, want 42", got.VideoFrames)
	}
}

// TestConsumeProgressRecordsFramesAndFencesGenerations proves the -progress
// reader records the frame count and out_time_us, and that a reader belonging
// to a replaced generation cannot lend its frames to the replacement.
func TestConsumeProgressRecordsFramesAndFencesGenerations(t *testing.T) {
	stream := "frame=0\nout_time_us=N/A\nspeed=N/A\nprogress=continue\n" +
		"frame=48\nout_time_us=1920000\nspeed=1.0x\nprogress=continue\n" +
		"frame=96\nout_time_us=3840000\nspeed=1.0x\nprogress=end\n"

	session := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "h264"}}
	session.mu.Lock()
	session.beginGeneration(time.Now())
	generation := session.progressGeneration
	session.mu.Unlock()

	session.consumeProgress(io.NopCloser(strings.NewReader(stream)), generation)
	frames, outTimeUS := session.VideoFrameProgress()
	if frames != 96 || outTimeUS != 3840000 {
		t.Fatalf("progress = (%d frames, %d us), want (96, 3840000)", frames, outTimeUS)
	}

	// A replacement generation supersedes the reader: its lines are discarded.
	session.mu.Lock()
	session.beginGeneration(time.Now())
	stale := generation
	session.mu.Unlock()
	session.consumeProgress(io.NopCloser(strings.NewReader("frame=500\nout_time_us=20000000\n")), stale)
	if frames, _ := session.VideoFrameProgress(); frames != 0 {
		t.Fatalf("stale reader reported %d frames on the replacement", frames)
	}
}

// TestVideoEncodeArgsReportProgressAndDropFastseek pins both halves of Fix 1's
// observability and Fix 2's seek: an encode asks for the -progress stream and
// seeks accurately, while a copy/remux plan keeps its +fastseek and its stdout
// untouched.
func TestVideoEncodeArgsReportProgressAndDropFastseek(t *testing.T) {
	encode := strings.Join(buildFFmpegArgs(TranscodeOpts{
		InputPath: "/media/movie.mkv", OutputDir: t.TempDir(),
		SourceVideoCodec: "hevc", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		SeekSeconds: 90, SegmentDuration: 2,
	}), " ")
	if !strings.Contains(encode, "-progress pipe:1") || !strings.Contains(encode, "-nostats") {
		t.Fatalf("encode args do not request a progress stream: %s", encode)
	}
	if !strings.Contains(encode, "-fflags +genpts ") {
		t.Fatalf("encode args do not seek accurately: %s", encode)
	}
	if strings.Contains(encode, "+fastseek") {
		t.Fatalf("encode args still fastseek past the keyframe: %s", encode)
	}

	remote := strings.Join(buildFFmpegArgs(TranscodeOpts{
		InputPath: "https://provider.example/movie.mkv", OutputDir: t.TempDir(),
		SourceVideoCodec: "hevc", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		SeekSeconds: 90, SegmentDuration: 2,
	}), " ")
	if !strings.Contains(remote, "-fflags +genpts+nobuffer ") {
		t.Fatalf("remote encode lost its buffering flags: %s", remote)
	}
	if strings.Contains(remote, "+fastseek") {
		t.Fatalf("remote encode still fastseeks past the keyframe: %s", remote)
	}

	copyPlan := strings.Join(buildFFmpegArgs(TranscodeOpts{
		InputPath: "/media/movie.mkv", OutputDir: t.TempDir(),
		SourceVideoCodec: "hevc", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
		SeekSeconds: 90, SegmentDuration: 2,
	}), " ")
	if strings.Contains(copyPlan, "-progress") {
		t.Fatalf("copy plan gained a progress stream: %s", copyPlan)
	}
	if !strings.Contains(copyPlan, "-fflags +genpts+fastseek ") {
		t.Fatalf("copy plan lost its fastseek: %s", copyPlan)
	}
}

// TestStartReadyTranscodeAdvancesWhenNoVideoFrames proves the typed verdict
// feeds the existing recovery machinery: a running generation that muxes
// without decoding is stopped and the hw_accel=auto walk moves to its next
// decode path instead of returning the failure.
func TestStartReadyTranscodeAdvancesWhenNoVideoFrames(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	pipeline := newResolvedAutoTranscodePipeline(startupTestOpts(), cache)
	var attempts []TranscodeOpts
	var frameLessDir string
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts = append(attempts, opts)
		if len(attempts) == 1 {
			frameLess := noVideoFramesSession(t, opts, 2*noVideoFramesGrace, 0)
			frameLessDir = frameLess.outputDir
			return frameLess, nil
		}
		return fakeStartupSession(t, opts, t.TempDir(), true, false), nil
	}

	session, err := StartReadyTranscode(context.Background(), pipeline, TranscodeStartup{
		Timeout: 2 * time.Second,
		Start:   start,
	})
	if err != nil || session == nil {
		t.Fatalf("StartReadyTranscode = (%v, %v), want the fallback to succeed", session, err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want the frame-less generation replaced", len(attempts))
	}
	if attempts[1].HWAccel != transcodeHWNVENC || !attempts[1].SoftwareVideoDecode {
		t.Fatalf("fallback = %+v, want the mixed CPU-decode path", attempts[1])
	}
	if _, statErr := os.Stat(frameLessDir); !os.IsNotExist(statErr) {
		t.Fatalf("frame-less fresh attempt kept its output dir: %v", statErr)
	}
	if preferred, found := cache.get(pipeline.cacheKey); !found || preferred.stage != autoTranscodeMixed {
		t.Fatalf("cache = %+v (found %v), want the mixed stage remembered", preferred, found)
	}
}

// TestStartReadyTranscodeReportsNoVideoFramesWithoutAnotherPath proves the
// verdict is a readiness failure carrying the typed cause when no safer path
// exists, so a caller can classify it instead of reading a generic timeout.
func TestStartReadyTranscodeReportsNoVideoFramesWithoutAnotherPath(t *testing.T) {
	pipeline := NewAutoTranscodePipeline(context.Background(), TranscodeOpts{
		HWAccel:          HWAccelNone,
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		InputPath:        "/media/movie.mkv",
		SessionID:        "playback-1",
	})
	if pipeline.Enabled() {
		t.Fatal("fixture pipeline must be disabled for a fixed acceleration")
	}
	var frameLess *TranscodeSession
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		frameLess = noVideoFramesSession(t, opts, 2*noVideoFramesGrace, 0)
		return frameLess, nil
	}

	_, err := StartReadyTranscode(context.Background(), pipeline, TranscodeStartup{Timeout: 2 * time.Second, Start: start})
	var startupErr *TranscodeStartupError
	if !errors.As(err, &startupErr) {
		t.Fatalf("error = %v, want a readiness failure", err)
	}
	if !errors.Is(err, ErrTranscodeNoVideoFrames) {
		t.Fatalf("error = %v, want the typed no-frames cause", err)
	}
	if !startupErr.WasRunning {
		t.Fatal("the frame-less generation was running when its verdict was reported")
	}
	if frameLess.IsRunning() {
		t.Fatal("the frame-less generation was left running")
	}
}
