package playback

// ExecutableRecipeV3 is the frozen operational half of a protocol-v3 plan.
// PlanV3 describes the client-visible route identity; these fields are the
// additional inputs needed to open another transport for that same route.
// Keeping them with the durable attempt prevents seek reanchoring from
// reverse-engineering execution details from presentation fields or mutable
// planner inputs.
type ExecutableRecipeV3 struct {
	Version                     int        `json:"version"`
	PlanID                      string     `json:"plan_id"`
	PlayMethod                  PlayMethod `json:"play_method"`
	TranscodeAudio              bool       `json:"transcode_audio"`
	TargetVideoCodec            string     `json:"target_video_codec,omitempty"`
	TargetAudioCodec            string     `json:"target_audio_codec,omitempty"`
	TargetAudioChannels         int        `json:"target_audio_channels,omitempty"`
	TargetAudioBitrateKbps      int        `json:"target_audio_bitrate_kbps,omitempty"`
	TargetResolution            string     `json:"target_resolution,omitempty"`
	TargetBitrateKbps           int        `json:"target_bitrate_kbps,omitempty"`
	SourceVideoCodec            string     `json:"source_video_codec,omitempty"`
	SoftwareVideoDecode         bool       `json:"software_video_decode,omitempty"`
	SourceDurationSeconds       float64    `json:"source_duration_seconds,omitempty"`
	SubtitleTrackIndex          int        `json:"subtitle_track_index"`
	SubtitleTransportTrackIndex int        `json:"subtitle_transport_track_index"`
	SubtitleBurnIn              bool       `json:"subtitle_burn_in"`
	SubtitleCodec               string     `json:"subtitle_codec,omitempty"`
	// SubtitleSource pins which sidecar inventory segment SubtitleTrackIndex
	// pointed into when the plan was accepted, and the identity fields below
	// pin the exact entry. The combined index space (externals, then embedded,
	// then downloaded) shifts when any segment grows or shrinks; a seek
	// reanchor must detect that drift and fail rather than silently resolve
	// the frozen index to a different artifact.
	SubtitleSource       string `json:"subtitle_source,omitempty"`
	ExternalSubtitlePath string `json:"external_subtitle_path,omitempty"`
	EmbeddedStreamIndex  int    `json:"embedded_stream_index,omitempty"`
	DownloadedSubtitleID int    `json:"downloaded_subtitle_id,omitempty"`
}

const executableRecipeVersionV3 = 1

func FreezeExecutableRecipeV3(result PlannerResultV3) ExecutableRecipeV3 {
	planID := ""
	if result.Plan != nil {
		planID = result.Plan.PlanID
	}
	sourceMetadata := SourceExecutionMetadataV3{}
	if result.FrozenSourceMetadata != nil {
		sourceMetadata = *result.FrozenSourceMetadata
	}
	return ExecutableRecipeV3{
		Version:                     executableRecipeVersionV3,
		PlanID:                      planID,
		PlayMethod:                  result.PlayMethod,
		TranscodeAudio:              result.TranscodeAudio,
		TargetVideoCodec:            result.TargetVideoCodec,
		TargetAudioCodec:            result.TargetAudioCodec,
		TargetAudioChannels:         result.TargetAudioChannels,
		TargetAudioBitrateKbps:      result.TargetAudioBitrateKbps,
		TargetResolution:            result.TargetResolution,
		TargetBitrateKbps:           result.TargetBitrateKbps,
		SourceVideoCodec:            sourceMetadata.VideoCodec,
		SoftwareVideoDecode:         sourceMetadata.SoftwareVideoDecode,
		SourceDurationSeconds:       sourceMetadata.DurationSeconds,
		SubtitleTrackIndex:          result.SubtitleTrackIndex,
		SubtitleTransportTrackIndex: result.SubtitleTransportTrackIndex,
		SubtitleBurnIn:              result.SubtitleBurnIn,
		SubtitleCodec:               result.SubtitleCodec,
		DownloadedSubtitleID:        result.DownloadedSubtitleID,
	}
}

func (r ExecutableRecipeV3) Valid() bool {
	if r.Version != executableRecipeVersionV3 || r.PlanID == "" {
		return false
	}
	switch r.PlayMethod {
	case PlayDirect, PlayRemux, PlayTranscode:
		return true
	default:
		return false
	}
}

func (r ExecutableRecipeV3) ValidFor(plan PlanV3) bool {
	return r.Valid() && r.PlanID == plan.PlanID
}

func (r ExecutableRecipeV3) PlannerResult(plan *PlanV3) PlannerResultV3 {
	return PlannerResultV3{
		Plan:                   plan,
		PlayMethod:             r.PlayMethod,
		TranscodeAudio:         r.TranscodeAudio,
		TargetVideoCodec:       r.TargetVideoCodec,
		TargetAudioCodec:       r.TargetAudioCodec,
		TargetAudioChannels:    r.TargetAudioChannels,
		TargetAudioBitrateKbps: r.TargetAudioBitrateKbps,
		TargetResolution:       r.TargetResolution,
		TargetBitrateKbps:      r.TargetBitrateKbps,
		FrozenSourceMetadata: &SourceExecutionMetadataV3{
			VideoCodec:          r.SourceVideoCodec,
			SoftwareVideoDecode: r.SoftwareVideoDecode,
			DurationSeconds:     r.SourceDurationSeconds,
		},
		SubtitleTrackIndex:          r.SubtitleTrackIndex,
		SubtitleTransportTrackIndex: r.SubtitleTransportTrackIndex,
		SubtitleBurnIn:              r.SubtitleBurnIn,
		SubtitleCodec:               r.SubtitleCodec,
		DownloadedSubtitleID:        r.DownloadedSubtitleID,
	}
}
