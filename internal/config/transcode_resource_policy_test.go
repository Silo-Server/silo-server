package config

import (
	"context"
	"errors"
	"testing"
)

type transcodePolicySettings map[string]string

func (settings transcodePolicySettings) Get(_ context.Context, key string) (string, error) {
	value, found := settings[key]
	if !found {
		return "", errors.New("setting unavailable")
	}
	return value, nil
}

func TestResolveTranscodeThrottleSettings(t *testing.T) {
	tests := []struct {
		name          string
		settings      transcodePolicySettings
		wantEnabled   bool
		wantThreshold int
	}{
		{
			name:          "defaults",
			wantEnabled:   DefaultTranscodeThrottleEnabled,
			wantThreshold: DefaultTranscodeThrottleSeconds,
		},
		{
			name: "explicit values",
			settings: transcodePolicySettings{
				TranscodeThrottleEnabledSettingKey: "false",
				TranscodeThrottleSecondsSettingKey: "240",
			},
			wantEnabled: false, wantThreshold: 240,
		},
		{
			name: "invalid values",
			settings: transcodePolicySettings{
				TranscodeThrottleEnabledSettingKey: "sometimes",
				TranscodeThrottleSecondsSettingKey: "-1",
			},
			wantEnabled:   DefaultTranscodeThrottleEnabled,
			wantThreshold: DefaultTranscodeThrottleSeconds,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enabled, threshold := ResolveTranscodeThrottleSettings(context.Background(), test.settings)
			if enabled != test.wantEnabled || threshold != test.wantThreshold {
				t.Fatalf("policy = (%v, %d), want (%v, %d)", enabled, threshold, test.wantEnabled, test.wantThreshold)
			}
		})
	}
}
