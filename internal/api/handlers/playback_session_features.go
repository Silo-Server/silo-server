package handlers

// The effective_play_method buckets the bridge read model reports; the native
// API calls effectivePlayMethodAudio "direct_stream".
const (
	effectivePlayMethodDirect    = "direct"
	effectivePlayMethodRemux     = "remux"
	effectivePlayMethodTranscode = "transcode"
	effectivePlayMethodAudio     = "audio"
)

// playbackSessionsCapabilitiesResponse advertises the additive fields of the
// live admin session payload so independently deployed clients (Android,
// Apple) can feature-detect them. The advertised fields are omitempty on the
// wire, so absence on a row is otherwise indistinguishable from an older
// server.
type playbackSessionsCapabilitiesResponse struct {
	// EffectivePlayMethod reports that rows carry effective_play_method.
	EffectivePlayMethod bool `json:"effective_play_method"`
	// EffectivePlayMethodValues is the closed bucket vocabulary a supported
	// server emits (absent field = unknown).
	EffectivePlayMethodValues []string `json:"effective_play_method_values"`
	// IsJellyfinClient reports that rows carry is_jellyfin_client.
	IsJellyfinClient bool `json:"is_jellyfin_client"`
	// TranscodeHWAccel reports that rows carry the confirmed transcode executor.
	TranscodeHWAccel bool `json:"transcode_hw_accel"`
	// ToneMapMode reports that rows carry the confirmed tone-map executor.
	ToneMapMode bool `json:"tone_map_mode"`
	// ToneMapModeValues is the closed vocabulary for recognized tone-map modes.
	ToneMapModeValues []string `json:"tone_map_mode_values"`
	// ClientBuild reports that rows carry client_build (and the exact-version
	// client_label_full derived from it).
	ClientBuild bool `json:"client_build"`
	// ClientChannel reports that rows carry client_channel.
	ClientChannel bool `json:"client_channel"`
	// TargetAudioChannels reports that rows carry target_audio_channels;
	// absent on a row then means the reporting node did not know the encoded
	// layout.
	TargetAudioChannels bool `json:"target_audio_channels"`
	// NodeRouting reports that rows may carry workload/execution/egress route
	// assignment fields when the active session has resolved them.
	NodeRouting bool `json:"node_routing"`
}

// AdminPlaybackSessionFeatures is shared by the frozen bridge and native projection.
func AdminPlaybackSessionFeatures() playbackSessionsCapabilitiesResponse {
	return playbackSessionsCapabilitiesResponse{
		EffectivePlayMethod:       true,
		EffectivePlayMethodValues: []string{effectivePlayMethodDirect, effectivePlayMethodRemux, effectivePlayMethodTranscode, effectivePlayMethodAudio},
		IsJellyfinClient:          true,
		TranscodeHWAccel:          true,
		ToneMapMode:               true,
		ToneMapModeValues:         []string{"hardware", "software"},
		ClientBuild:               true,
		ClientChannel:             true,
		TargetAudioChannels:       true,
		NodeRouting:               true,
	}
}
