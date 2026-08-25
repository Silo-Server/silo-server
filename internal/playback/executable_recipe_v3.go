package playback

import "github.com/Silo-Server/silo-server/internal/tonemap"

// ExecutableRecipeV3 is the frozen operational half of a protocol-v3 plan.
// PlanV3 describes the client-visible route identity; these fields are the
// additional inputs needed to open another transport for that same route.
// Keeping them with the durable attempt prevents seek reanchoring from
// reverse-engineering execution details from presentation fields or mutable
// planner inputs.
type ExecutableRecipeV3 struct {
	Version                     int                    `json:"version"`
	PlanID                      string                 `json:"plan_id"`
	PlayMethod                  PlayMethod             `json:"play_method"`
	TranscodeAudio              bool                   `json:"transcode_audio"`
	TargetVideoCodec            string                 `json:"target_video_codec,omitempty"`
	TargetAudioCodec            string                 `json:"target_audio_codec,omitempty"`
	TargetAudioChannels         int                    `json:"target_audio_channels,omitempty"`
	TargetAudioBitrateKbps      int                    `json:"target_audio_bitrate_kbps,omitempty"`
	TargetResolution            string                 `json:"target_resolution,omitempty"`
	TargetBitrateKbps           int                    `json:"target_bitrate_kbps,omitempty"`
	SourceVideoCodec            string                 `json:"source_video_codec,omitempty"`
	SourceVideoProfile          string                 `json:"source_video_profile,omitempty"`
	SourceVideoBitDepth         int                    `json:"source_video_bit_depth,omitempty"`
	SoftwareVideoDecode         bool                   `json:"software_video_decode,omitempty"`
	SourceDurationSeconds       float64                `json:"source_duration_seconds,omitempty"`
	ToneMapPolicy               tonemap.Policy         `json:"tone_map_policy,omitempty"`
	ToneMapMode                 tonemap.Mode           `json:"tone_map_mode,omitempty"`
	ToneMapSourceKind           tonemap.SourceKind     `json:"tone_map_source_kind,omitempty"`
	ToneMapRecipeVersion        string                 `json:"tone_map_recipe_version,omitempty"`
	ToneMapPreflightRequired    bool                   `json:"tone_map_preflight_required,omitempty"`
	ToneMapSourceRevision       tonemap.SourceRevision `json:"tone_map_source_revision,omitzero"`
	ToneMapDVConfigPresent      bool                   `json:"tone_map_dv_config_present,omitempty"`
	ToneMapDVBLCompatIDPresent  bool                   `json:"tone_map_dv_bl_compat_id_present,omitempty"`
	ToneMapDVBLPresent          bool                   `json:"tone_map_dv_bl_present,omitempty"`
	ToneMapDVRPUPresent         bool                   `json:"tone_map_dv_rpu_present,omitempty"`
	SubtitleTrackIndex          int                    `json:"subtitle_track_index"`
	SubtitleTransportTrackIndex int                    `json:"subtitle_transport_track_index"`
	SubtitleBurnIn              bool                   `json:"subtitle_burn_in"`
	SubtitleCodec               string                 `json:"subtitle_codec,omitempty"`
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

const (
	executableRecipeVersionLegacyV3 = 1
	executableRecipeVersionV3       = 2
)

// FreezeExecutableRecipeV3 captures the byte-affecting facts from a planner result.
func FreezeExecutableRecipeV3(result PlannerResultV3) ExecutableRecipeV3 {
	planID := ""
	if result.Plan != nil {
		planID = result.Plan.PlanID
	}
	sourceMetadata := SourceExecutionMetadataV3{}
	if result.FrozenSourceMetadata != nil {
		sourceMetadata = *result.FrozenSourceMetadata
	}
	recipe := ExecutableRecipeV3{
		Version:                     executableRecipeVersionLegacyV3,
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
		SourceVideoProfile:          sourceMetadata.VideoProfile,
		SourceVideoBitDepth:         sourceMetadata.VideoBitDepth,
		SoftwareVideoDecode:         sourceMetadata.SoftwareVideoDecode,
		SourceDurationSeconds:       sourceMetadata.DurationSeconds,
		ToneMapPolicy:               result.ToneMapPolicy,
		ToneMapMode:                 result.ToneMapMode,
		ToneMapSourceKind:           result.ToneMapSourceKind,
		ToneMapRecipeVersion:        result.ToneMapRecipeVersion,
		ToneMapPreflightRequired:    result.ToneMapPreflightRequired,
		ToneMapSourceRevision:       result.ToneMapSourceRevision,
		ToneMapDVConfigPresent:      sourceMetadata.ToneMapDVConfigPresent,
		ToneMapDVBLCompatIDPresent:  sourceMetadata.ToneMapDVBLCompatIDPresent,
		ToneMapDVBLPresent:          sourceMetadata.ToneMapDVBLPresent,
		ToneMapDVRPUPresent:         sourceMetadata.ToneMapDVRPUPresent,
		SubtitleTrackIndex:          result.SubtitleTrackIndex,
		SubtitleTransportTrackIndex: result.SubtitleTransportTrackIndex,
		SubtitleBurnIn:              result.SubtitleBurnIn,
		SubtitleCodec:               result.SubtitleCodec,
		DownloadedSubtitleID:        result.DownloadedSubtitleID,
	}
	if recipe.hasVersion2Fields() {
		recipe.Version = executableRecipeVersionV3
	}
	return recipe
}

func (r ExecutableRecipeV3) hasVersion2Fields() bool {
	return r.ToneMapPolicy != "" ||
		r.ToneMapMode != "" ||
		r.ToneMapSourceKind != "" ||
		r.ToneMapRecipeVersion != "" ||
		r.ToneMapPreflightRequired ||
		!r.ToneMapSourceRevision.IsZero() ||
		r.ToneMapDVConfigPresent ||
		r.ToneMapDVBLCompatIDPresent ||
		r.ToneMapDVBLPresent ||
		r.ToneMapDVRPUPresent
}

// Valid reports whether an executable recipe is complete and internally consistent.
func (r ExecutableRecipeV3) Valid() bool {
	if (r.Version != executableRecipeVersionLegacyV3 && r.Version != executableRecipeVersionV3) || r.PlanID == "" {
		return false
	}
	if r.Version == executableRecipeVersionLegacyV3 && r.hasVersion2Fields() {
		return false
	}
	hasToneMapField := r.ToneMapPolicy != "" || r.ToneMapMode != "" || r.ToneMapSourceKind != "" || r.ToneMapRecipeVersion != "" || r.ToneMapPreflightRequired || !r.ToneMapSourceRevision.IsZero()
	validToneMapSource := tonemap.ValidSourceKind(r.ToneMapSourceKind)
	if hasToneMapField && (r.PlayMethod != PlayTranscode || !r.ToneMapPolicy.Allows(r.ToneMapMode) || !validToneMapSource || r.ToneMapRecipeVersion != TransformationHDRToSDRToneMapRecipeVersionV3 || r.ToneMapSourceRevision.IsZero()) {
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

// PlannerResult restores a planner result from the frozen executable recipe.
func (r ExecutableRecipeV3) PlannerResult(plan *PlanV3) PlannerResultV3 {
	return PlannerResultV3{
		Plan:                     plan,
		PlayMethod:               r.PlayMethod,
		TranscodeAudio:           r.TranscodeAudio,
		TargetVideoCodec:         r.TargetVideoCodec,
		TargetAudioCodec:         r.TargetAudioCodec,
		TargetAudioChannels:      r.TargetAudioChannels,
		TargetAudioBitrateKbps:   r.TargetAudioBitrateKbps,
		TargetResolution:         r.TargetResolution,
		TargetBitrateKbps:        r.TargetBitrateKbps,
		ToneMapPolicy:            r.ToneMapPolicy,
		ToneMapMode:              r.ToneMapMode,
		ToneMapSourceKind:        r.ToneMapSourceKind,
		ToneMapRecipeVersion:     r.ToneMapRecipeVersion,
		ToneMapPreflightRequired: r.ToneMapPreflightRequired,
		ToneMapSourceRevision:    r.ToneMapSourceRevision,
		FrozenSourceMetadata: &SourceExecutionMetadataV3{
			VideoCodec:                 r.SourceVideoCodec,
			VideoProfile:               r.SourceVideoProfile,
			VideoBitDepth:              r.SourceVideoBitDepth,
			SoftwareVideoDecode:        r.SoftwareVideoDecode,
			DurationSeconds:            r.SourceDurationSeconds,
			ToneMapSourceKind:          r.ToneMapSourceKind,
			ToneMapPreflightRequired:   r.ToneMapPreflightRequired,
			ToneMapSourceRevision:      r.ToneMapSourceRevision,
			ToneMapDVConfigPresent:     r.ToneMapDVConfigPresent,
			ToneMapDVBLCompatIDPresent: r.ToneMapDVBLCompatIDPresent,
			ToneMapDVBLPresent:         r.ToneMapDVBLPresent,
			ToneMapDVRPUPresent:        r.ToneMapDVRPUPresent,
		},
		SubtitleTrackIndex:          r.SubtitleTrackIndex,
		SubtitleTransportTrackIndex: r.SubtitleTransportTrackIndex,
		SubtitleBurnIn:              r.SubtitleBurnIn,
		SubtitleCodec:               r.SubtitleCodec,
		DownloadedSubtitleID:        r.DownloadedSubtitleID,
	}
}
