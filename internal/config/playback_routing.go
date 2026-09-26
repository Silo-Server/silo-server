package config

import "fmt"

const (
	PlaybackRoutingDirectPlayEgressSettingKey        = "playback.routing.direct_play_egress"
	PlaybackRoutingRemuxExecutionSettingKey          = "playback.routing.remux_execution"
	PlaybackRoutingRemuxEgressSettingKey             = "playback.routing.remux_egress"
	PlaybackRoutingVideoTranscodeExecutionSettingKey = "playback.routing.video_transcode_execution"
	PlaybackRoutingVideoTranscodeEgressSettingKey    = "playback.routing.video_transcode_egress"
)

// PlaybackExecutionPreference controls whether server-side media work should
// run on a worker or on the integrated API process. A worker is either a proxy
// node or a transcode node, depending on the delivery's legal route shapes. A
// preference permits fallback; an "only" value is a hard routing constraint.
type PlaybackExecutionPreference string

const (
	PlaybackExecutionPreferWorker    PlaybackExecutionPreference = "prefer_worker"
	PlaybackExecutionPreferTranscode PlaybackExecutionPreference = "prefer_transcode"
	PlaybackExecutionWorkerOnly      PlaybackExecutionPreference = "worker_only"
	PlaybackExecutionPreferAPI       PlaybackExecutionPreference = "prefer_api"
	PlaybackExecutionAPIOnly         PlaybackExecutionPreference = "api_only"
)

// PlaybackEgressPreference controls which Silo process is the client-facing
// media origin. Transcode workers are never client-facing origins.
type PlaybackEgressPreference string

const (
	PlaybackEgressPreferProxy PlaybackEgressPreference = "prefer_proxy"
	PlaybackEgressProxyOnly   PlaybackEgressPreference = "proxy_only"
	PlaybackEgressPreferAPI   PlaybackEgressPreference = "prefer_api"
	PlaybackEgressAPIOnly     PlaybackEgressPreference = "api_only"
)

// PlaybackRoutingPolicy is the immutable routing snapshot consumed by one
// playback start or replan. Callers must read the containing PlaybackConfig
// once per attempt so a route cannot combine values from different reloads.
type PlaybackRoutingPolicy struct {
	DirectPlayEgress        PlaybackEgressPreference
	RemuxExecution          PlaybackExecutionPreference
	RemuxEgress             PlaybackEgressPreference
	VideoTranscodeExecution PlaybackExecutionPreference
	VideoTranscodeEgress    PlaybackEgressPreference
}

func DefaultPlaybackRoutingPolicy() PlaybackRoutingPolicy {
	return PlaybackRoutingPolicy{
		DirectPlayEgress:        PlaybackEgressPreferProxy,
		RemuxExecution:          PlaybackExecutionPreferTranscode,
		RemuxEgress:             PlaybackEgressPreferProxy,
		VideoTranscodeExecution: PlaybackExecutionPreferTranscode,
		VideoTranscodeEgress:    PlaybackEgressPreferProxy,
	}
}

// EffectivePlaybackRoutingPolicy fills zero-valued fields with the runtime
// defaults. This keeps minimally constructed Config values (notably tests and
// embedded users) aligned with a config loaded from server_settings.
func EffectivePlaybackRoutingPolicy(policy PlaybackRoutingPolicy) PlaybackRoutingPolicy {
	defaults := DefaultPlaybackRoutingPolicy()
	if policy.DirectPlayEgress == "" {
		policy.DirectPlayEgress = defaults.DirectPlayEgress
	}
	if policy.RemuxExecution == "" {
		policy.RemuxExecution = defaults.RemuxExecution
	}
	if policy.RemuxEgress == "" {
		policy.RemuxEgress = defaults.RemuxEgress
	}
	if policy.VideoTranscodeExecution == "" {
		policy.VideoTranscodeExecution = defaults.VideoTranscodeExecution
	}
	if policy.VideoTranscodeEgress == "" {
		policy.VideoTranscodeEgress = defaults.VideoTranscodeEgress
	}
	return policy
}

func playbackRoutingPolicyFromSettings(values map[string]string) PlaybackRoutingPolicy {
	defaults := DefaultPlaybackRoutingPolicy()
	return PlaybackRoutingPolicy{
		DirectPlayEgress: PlaybackEgressPreference(stringOr(values,
			PlaybackRoutingDirectPlayEgressSettingKey, string(defaults.DirectPlayEgress))),
		RemuxExecution: PlaybackExecutionPreference(stringOr(values,
			PlaybackRoutingRemuxExecutionSettingKey, string(defaults.RemuxExecution))),
		RemuxEgress: PlaybackEgressPreference(stringOr(values,
			PlaybackRoutingRemuxEgressSettingKey, string(defaults.RemuxEgress))),
		VideoTranscodeExecution: PlaybackExecutionPreference(stringOr(values,
			PlaybackRoutingVideoTranscodeExecutionSettingKey, string(defaults.VideoTranscodeExecution))),
		VideoTranscodeEgress: PlaybackEgressPreference(stringOr(values,
			PlaybackRoutingVideoTranscodeEgressSettingKey, string(defaults.VideoTranscodeEgress))),
	}
}

func validatePlaybackRoutingPolicy(policy PlaybackRoutingPolicy) error {
	policy = EffectivePlaybackRoutingPolicy(policy)
	for _, setting := range []struct {
		key   string
		value PlaybackExecutionPreference
	}{
		{PlaybackRoutingRemuxExecutionSettingKey, policy.RemuxExecution},
		{PlaybackRoutingVideoTranscodeExecutionSettingKey, policy.VideoTranscodeExecution},
	} {
		switch setting.value {
		case PlaybackExecutionPreferWorker, PlaybackExecutionPreferTranscode, PlaybackExecutionWorkerOnly,
			PlaybackExecutionPreferAPI, PlaybackExecutionAPIOnly:
		default:
			return fmt.Errorf("%s has invalid execution preference %q", setting.key, setting.value)
		}
	}
	for _, setting := range []struct {
		key   string
		value PlaybackEgressPreference
	}{
		{PlaybackRoutingDirectPlayEgressSettingKey, policy.DirectPlayEgress},
		{PlaybackRoutingRemuxEgressSettingKey, policy.RemuxEgress},
		{PlaybackRoutingVideoTranscodeEgressSettingKey, policy.VideoTranscodeEgress},
	} {
		switch setting.value {
		case PlaybackEgressPreferProxy, PlaybackEgressProxyOnly,
			PlaybackEgressPreferAPI, PlaybackEgressAPIOnly:
		default:
			return fmt.Errorf("%s has invalid egress preference %q", setting.key, setting.value)
		}
	}
	if policy.RemuxExecution == PlaybackExecutionAPIOnly && policy.RemuxEgress == PlaybackEgressProxyOnly {
		return fmt.Errorf("%s=api_only cannot be combined with %s=proxy_only: API-produced remuxes cannot use proxy egress",
			PlaybackRoutingRemuxExecutionSettingKey, PlaybackRoutingRemuxEgressSettingKey)
	}
	if policy.VideoTranscodeExecution == PlaybackExecutionAPIOnly && policy.VideoTranscodeEgress == PlaybackEgressProxyOnly {
		return fmt.Errorf("%s=api_only cannot be combined with %s=proxy_only: API-produced transcodes cannot use proxy egress",
			PlaybackRoutingVideoTranscodeExecutionSettingKey, PlaybackRoutingVideoTranscodeEgressSettingKey)
	}
	return nil
}
