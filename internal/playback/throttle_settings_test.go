package playback_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type throttleSettings map[string]string

func (s throttleSettings) Get(_ context.Context, key string) (string, error) {
	return s[key], nil
}

type failingThrottleSettings struct{}

func (failingThrottleSettings) Get(context.Context, string) (string, error) {
	return "", errors.New("settings store unavailable")
}

type recordingThrottleStarter struct {
	thresholds []int
}

func (s *recordingThrottleStarter) StartThrottler(threshold int) {
	s.thresholds = append(s.thresholds, threshold)
}

func TestStartConfiguredTranscodeThrottler(t *testing.T) {
	tests := []struct {
		name       string
		settings   playback.TranscodeThrottleSettings
		thresholds []int
	}{
		{name: "unset uses the default", settings: throttleSettings{}, thresholds: []int{playback.DefaultTranscodeThrottleSeconds}},
		{name: "unreadable setting disables", settings: failingThrottleSettings{}},
		{name: "disabled", settings: throttleSettings{"enable_transcode_throttle": "false"}},
		{name: "positive value below executor minimum is clamped", settings: throttleSettings{"enable_transcode_throttle": "true", "transcode_throttle_seconds": "30"}, thresholds: []int{60}},
		{name: "configured", settings: throttleSettings{"enable_transcode_throttle": "true", "transcode_throttle_seconds": "180"}, thresholds: []int{180}},
		{name: "invalid threshold uses default", settings: throttleSettings{"enable_transcode_throttle": "true", "transcode_throttle_seconds": "invalid"}, thresholds: []int{300}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			starter := &recordingThrottleStarter{}
			playback.StartConfiguredTranscodeThrottler(context.Background(), tt.settings, starter)
			if len(starter.thresholds) != len(tt.thresholds) {
				t.Fatalf("thresholds = %v, want %v", starter.thresholds, tt.thresholds)
			}
			for i, want := range tt.thresholds {
				if starter.thresholds[i] != want {
					t.Fatalf("thresholds = %v, want %v", starter.thresholds, tt.thresholds)
				}
			}
		})
	}
}
