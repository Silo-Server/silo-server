package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestToneMapPathHashFailureIsTransient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short-source.mkv")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := validateToneMapSource(context.Background(), TranscodeOpts{
		InputPath: path, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1, FileSize: 5, FileHash: "0123456789abcdef"},
	})
	if !errors.Is(err, ErrToneMapSourceValidationUnavailable) || errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("validateToneMapSource() error = %v, want transient validation unavailable", err)
	}
}

func TestResolveToneMapExecutorClassifiesCanceledProbeAsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveToneMapExecutor(ctx, TranscodeOpts{
		ToneMapPolicy:        tonemap.PolicySoftwareOnly,
		ToneMapMode:          tonemap.ModeSoftware,
		ToneMapSourceKind:    tonemap.SourcePQ,
		ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{
			MediaFileID: 1,
			FileSize:    5,
		},
	})
	if !errors.Is(err, ErrToneMapExecutorUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveToneMapExecutor() error = %v, want executor unavailable + canceled", err)
	}
}

func TestClassifyToneMapPreflightErrorPreservesTransientIdentity(t *testing.T) {
	transient := fmt.Errorf("%w: %w", tonemap.ErrSourcePreflightUnavailable, context.DeadlineExceeded)
	err := classifyToneMapPreflightError(transient)
	if !errors.Is(err, ErrToneMapSourceValidationUnavailable) ||
		!errors.Is(err, tonemap.ErrSourcePreflightUnavailable) ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transient preflight = %v, want playback, preflight, and deadline identities", err)
	}

	rejected := fmt.Errorf("%w: mismatched decoded frame", tonemap.ErrSourcePreflightRejected)
	err = classifyToneMapPreflightError(rejected)
	if !errors.Is(err, tonemap.ErrSourcePreflightRejected) || errors.Is(err, ErrToneMapSourceValidationUnavailable) {
		t.Fatalf("deterministic preflight = %v, want rejection only", err)
	}
}

func TestNonToneMapStartDoesNotProbeLiveMetadata(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	probeMarker := filepath.Join(dir, "ffprobe-ran")
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\ntouch '"+probeMarker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	session, err := StartTranscode(context.Background(), TranscodeOpts{
		SessionID: "ordinary", InputPath: inputPath, OutputDir: filepath.Join(dir, "hls"),
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, FFmpegPath: ffmpegPath,
	})
	if err != nil {
		t.Fatalf("StartTranscode() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, statErr := os.Stat(probeMarker); !os.IsNotExist(statErr) {
		t.Fatalf("non-tone-map start invoked FFprobe: %v", statErr)
	}
}
