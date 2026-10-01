package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPlaybackHandlerTranscodeRuntimeConfigIncludesSegmentRetention(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{SegmentRetentionSeconds: 300}
	}

	if got := handler.tm.Config().SegmentRetentionSeconds; got != 300 {
		t.Fatalf("configured segment retention = %d, want 300", got)
	}
}
