package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartReadyTranscodePipelineAvoidsFailedGPU(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	base := TranscodeOpts{
		SessionID:           "playback-1",
		InputPath:           "/media/movie.mkv",
		HWAccel:             transcodeHWNVENC,
		HWDevice:            "gpu-0,gpu-1",
		SourceVideoCodec:    "hevc",
		TargetCodecVideo:    "h264",
		TargetCodecAudio:    "aac",
		SegmentDuration:     2,
		TargetBitrateKbps:   2_000,
		SourceVideoBitDepth: 10,
	}
	pipeline := newAutoTranscodePipeline(base, true, cache)
	attempts := make([]TranscodeOpts, 0, 2)
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts = append(attempts, opts)
		selectedDevice := "gpu-0"
		if opts.AvoidHWDevice == selectedDevice {
			selectedDevice = "gpu-1"
		}
		opts.HWDevice = selectedDevice
		outputDir := t.TempDir()
		if len(attempts) == 2 {
			manifest := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2,\nseg_00000.ts\n#EXT-X-ENDLIST\n")
			if err := os.WriteFile(filepath.Join(outputDir, "stream.m3u8"), manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			return &TranscodeSession{opts: opts, outputDir: outputDir, stderr: newBoundedTailBuffer(stderrTailMaxBytes)}, nil
		}
		return &TranscodeSession{opts: opts, outputDir: outputDir, stderr: newBoundedTailBuffer(stderrTailMaxBytes), waitErr: errors.New("gpu failed")}, nil
	}

	session, err := startReadyTranscodePipeline(context.Background(), pipeline, time.Millisecond, start)
	if err != nil {
		t.Fatalf("startReadyTranscodePipeline: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(attempts))
	}
	if attempts[1].AvoidHWDevice != "gpu-0" || !attempts[1].SoftwareVideoDecode {
		t.Fatalf("fallback = %+v, want hybrid path avoiding gpu-0", attempts[1])
	}
	if got := session.Opts().HWDevice; got != "gpu-1" {
		t.Fatalf("selected device = %q, want gpu-1", got)
	}
	_ = session.Close()
}

func TestStartReadyTranscodePipelineDoesNotDuplicateSlowProcess(t *testing.T) {
	base := TranscodeOpts{
		SessionID:        "playback-1",
		InputPath:        "/media/movie.mkv",
		HWAccel:          transcodeHWNVENC,
		SourceVideoCodec: "hevc",
		TargetCodecVideo: "h264",
	}
	pipeline := newAutoTranscodePipeline(base, true, newAutoTranscodePipelineCache())
	attempts := 0
	start := func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		attempts++
		return &TranscodeSession{opts: opts, outputDir: t.TempDir(), running: true, stderr: newBoundedTailBuffer(stderrTailMaxBytes)}, nil
	}

	if _, err := startReadyTranscodePipeline(context.Background(), pipeline, time.Millisecond, start); err == nil {
		t.Fatal("expected readiness timeout")
	}
	if attempts != 1 {
		t.Fatalf("attempt count = %d, want 1", attempts)
	}
}
