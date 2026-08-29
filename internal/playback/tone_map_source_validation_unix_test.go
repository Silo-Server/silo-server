//go:build !windows

package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// The live tone-map source fixtures drive ffmpeg/ffprobe through POSIX shell
// scripts, so the tests exercising them are gated to non-Windows builds.

func TestStartTranscodeRejectsLivePrimaryVideoMismatchBeforeFFmpeg(t *testing.T) {
	opts, ffmpegMarker := mismatchedToneMapExecutionFixture(t)
	opts.OutputDir = filepath.Join(t.TempDir(), "hls")
	opts.SessionID = "metadata-mismatch"
	opts.SegmentDuration = 2

	session, err := StartTranscode(context.Background(), opts)
	if session != nil {
		_ = session.Close()
	}
	if !errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("StartTranscode() error = %v, want ErrSourceRevisionChanged", err)
	}
	if _, statErr := os.Stat(ffmpegMarker); !os.IsNotExist(statErr) {
		t.Fatalf("FFmpeg ran before live source rejection: %v", statErr)
	}
}

func TestPrepareFileRejectsLivePrimaryVideoMismatchBeforeFFmpegOrPublication(t *testing.T) {
	opts, ffmpegMarker := mismatchedToneMapExecutionFixture(t)
	outputPath := filepath.Join(t.TempDir(), "artifact.mp4")

	err := PrepareFile(context.Background(), opts, outputPath)
	if !errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("PrepareFile() error = %v, want ErrSourceRevisionChanged", err)
	}
	if _, statErr := os.Stat(ffmpegMarker); !os.IsNotExist(statErr) {
		t.Fatalf("FFmpeg ran before live source rejection: %v", statErr)
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("prepared output was published for stale metadata: %v", statErr)
	}
	if _, statErr := os.Stat(outputPath + ".part"); !os.IsNotExist(statErr) {
		t.Fatalf("partial output was created for stale metadata: %v", statErr)
	}
}

func TestReconstructTranscodeRejectsLivePrimaryVideoMismatchBeforeFFmpeg(t *testing.T) {
	opts, ffmpegMarker := mismatchedToneMapExecutionFixture(t)
	opts.SessionID = "metadata-reconstruct"
	opts.SegmentDuration = 2
	manager := NewTranscodeManager()
	manager.Config = func() TranscodeRuntimeConfig {
		return TranscodeRuntimeConfig{TranscodeDir: t.TempDir(), FFmpegPath: opts.FFmpegPath, HWAccel: HWAccelNone}
	}
	manager.resolveToneMapExecutor = func(_ context.Context, reconstructed TranscodeOpts) (TranscodeOpts, error) {
		reconstructed.ToneMapFilter = tonemap.SoftwareFilterBT2390
		reconstructed.HWAccel = HWAccelNone
		return reconstructed, nil
	}
	card := NewRecipeCard(7, "profile-1", opts.ToneMapSourceRevision.MediaFileID, "", opts)

	session, reconstructErr := manager.ReconstructTranscodeWithError(context.Background(), opts.SessionID, -1, card)
	if session != nil {
		_ = session.Close()
		t.Fatal("ReconstructTranscode() started a stale tone-map recipe")
	}
	if !errors.Is(reconstructErr, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("ReconstructTranscodeWithError() error = %v, want ErrSourceRevisionChanged", reconstructErr)
	}
	if _, statErr := os.Stat(ffmpegMarker); !os.IsNotExist(statErr) {
		t.Fatalf("reconstructed FFmpeg ran before live source rejection: %v", statErr)
	}
}

func TestStartTranscodeClassifiesLiveProbeTimeoutAsTransientBeforeFFmpeg(t *testing.T) {
	opts, ffmpegMarker := mismatchedToneMapExecutionFixture(t)
	opts.OutputDir = filepath.Join(t.TempDir(), "hls")
	opts.SessionID = "metadata-timeout"
	opts.SegmentDuration = 2
	ffprobePath := filepath.Join(filepath.Dir(opts.FFmpegPath), "ffprobe")
	if err := os.WriteFile(ffprobePath, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	session, err := StartTranscode(ctx, opts)
	if session != nil {
		_ = session.Close()
	}
	if !errors.Is(err, ErrToneMapSourceValidationUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StartTranscode() error = %v, want transient source-validation deadline", err)
	}
	if _, statErr := os.Stat(ffmpegMarker); !os.IsNotExist(statErr) {
		t.Fatalf("FFmpeg ran after live probe timeout: %v", statErr)
	}
}

func TestRestartRejectsChangedToneMapSourceBeforeStoppingCurrentProcess(t *testing.T) {
	opts, ffmpegMarker := mismatchedToneMapExecutionFixture(t)
	dir := filepath.Dir(opts.InputPath)
	done := make(chan struct{})
	close(done)
	canceled := false
	session := &TranscodeSession{
		cancel:    func() { canceled = true },
		done:      done,
		running:   true,
		outputDir: dir,
		opts:      opts,
	}

	if err := session.Restart(context.Background(), 20, 10); !errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("Restart() error = %v, want ErrSourceRevisionChanged", err)
	}
	if canceled {
		t.Fatal("Restart() stopped the current process before validating the frozen source")
	}
	if _, statErr := os.Stat(ffmpegMarker); !os.IsNotExist(statErr) {
		t.Fatalf("restart FFmpeg ran before live source rejection: %v", statErr)
	}
	session.mu.Lock()
	restartCount := session.restartCount
	restarting := session.restarting
	session.mu.Unlock()
	if restarting != nil || restartCount != 0 {
		t.Fatalf("failed validation left restarting=%v restartCount=%d, want nil/0", restarting, restartCount)
	}
}

func TestRestartLeavesCurrentProcessRunningWhenLiveProbeTimesOut(t *testing.T) {
	previousTimeout := restartToneMapValidationTimeout
	restartToneMapValidationTimeout = 30 * time.Millisecond
	t.Cleanup(func() { restartToneMapValidationTimeout = previousTimeout })

	opts, ffmpegMarker := mismatchedToneMapExecutionFixture(t)
	ffprobePath := filepath.Join(filepath.Dir(opts.FFmpegPath), "ffprobe")
	if err := os.WriteFile(ffprobePath, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	canceled := false
	session := &TranscodeSession{
		cancel: func() { canceled = true }, done: done, running: true,
		outputDir: filepath.Dir(opts.InputPath), opts: opts,
	}

	err := session.Restart(context.Background(), 20, 10)
	if !errors.Is(err, ErrToneMapSourceValidationUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Restart() error = %v, want transient source-validation deadline", err)
	}
	if canceled {
		t.Fatal("Restart() stopped the current process after a transient source probe failure")
	}
	if _, statErr := os.Stat(ffmpegMarker); !os.IsNotExist(statErr) {
		t.Fatalf("restart FFmpeg ran after live probe timeout: %v", statErr)
	}
}

func mismatchedToneMapExecutionFixture(t *testing.T) (TranscodeOpts, string) {
	t.Helper()
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	frozenTrack := toneMapValidationTrack("Main 10")
	revision := tonemap.RevisionForFile(&models.MediaFile{
		ID: 42, FileSize: info.Size(), VideoTracks: []models.VideoTrack{frozenTrack},
	})
	ffprobePath := filepath.Join(dir, "ffprobe")
	liveJSON := `{"streams":[{"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main","level":153,"width":3840,"height":2160,"avg_frame_rate":"24000/1001","pix_fmt":"yuv420p10le","color_range":"tv","color_primaries":"bt2020","color_transfer":"smpte2084","color_space":"bt2020nc"}]}`
	if err := os.WriteFile(ffprobePath, []byte("#!/bin/sh\nprintf '%s' '"+liveJSON+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ffmpegMarker := filepath.Join(dir, "ffmpeg-ran")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	ffmpegScript := "#!/bin/sh\ntouch '" + ffmpegMarker + "'\nfor last; do :; done\nprintf output > \"$last\"\n"
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return TranscodeOpts{
		InputPath: inputPath, TargetCodecVideo: "h264", TargetCodecAudio: "aac", FFmpegPath: ffmpegPath,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: tonemap.SoftwareFilterBT2390,
		ToneMapRecipeVersion:  TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: revision,
	}, ffmpegMarker
}

func toneMapValidationTrack(profile string) models.VideoTrack {
	return models.VideoTrack{
		Codec: "hevc", Profile: profile, Level: 153, Width: 3840, Height: 2160, FrameRate: "23.976",
		ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
		BitDepth: 10, PixelFormat: "yuv420p10le",
	}
}
