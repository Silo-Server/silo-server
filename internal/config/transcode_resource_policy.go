package config

import (
	"context"
	"strconv"
)

// SettingReader is the settings-store capability needed to resolve the live
// transcode resource policy.
type SettingReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// ResolveTranscodeThrottleSettings returns the live forward-buffer policy.
// Missing, invalid, or unavailable settings retain the protective defaults.
func ResolveTranscodeThrottleSettings(ctx context.Context, reader SettingReader) (bool, int) {
	enabled := DefaultTranscodeThrottleEnabled
	thresholdSeconds := DefaultTranscodeThrottleSeconds
	if reader == nil {
		return enabled, thresholdSeconds
	}

	if rawEnabled, err := reader.Get(ctx, TranscodeThrottleEnabledSettingKey); err == nil && rawEnabled != "" {
		if configured, parseErr := strconv.ParseBool(rawEnabled); parseErr == nil {
			enabled = configured
		}
	}
	if rawThreshold, err := reader.Get(ctx, TranscodeThrottleSecondsSettingKey); err == nil && rawThreshold != "" {
		if configured, parseErr := strconv.Atoi(rawThreshold); parseErr == nil && configured > 0 {
			thresholdSeconds = configured
		}
	}

	return enabled, thresholdSeconds
}
