package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A departed request must not hold the session lifecycle lock for the rest of
// ManifestStartupTimeout while FFmpeg works toward a manifest nobody will read.
func TestStartReadyLocalPlaybackTransportStopsWaitingWhenRequestEnds(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	opts := playback.TranscodeOpts{
		InputPath:          "/media/slow.mkv",
		OutputDir:          t.TempDir(),
		SessionID:          "departed-startup",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		FFmpegPath:         writePlaybackTestFFmpegNeverReady(t),
		HWAccel:            "none",
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)

	begin := time.Now()
	ts, failure := handler.startReadyLocalPlaybackTransportV3(ctx, opts)
	if elapsed := time.Since(begin); elapsed > 10*time.Second {
		t.Fatalf("startup waited %v after the request ended", elapsed)
	}
	if ts != nil || failure == nil || failure.failedToStart {
		t.Fatalf("startup = (%v, %+v), want a readiness failure", ts, failure)
	}
	if !errors.Is(failure.cause, context.Canceled) {
		t.Fatalf("failure cause = %v, want context.Canceled", failure.cause)
	}
}
