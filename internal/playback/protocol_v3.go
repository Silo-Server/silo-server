package playback

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	ProtocolV3                    = 3
	FeaturePlaybackPlanV3         = "playback_plan_v3"
	FeatureMedia3Only             = "media3_only"
	FeatureDetailedDecodeV3       = "detailed_decode_capabilities"
	FeatureLayoutPassthrough      = "layout_aware_passthrough"
	FeatureClientVideoTransforms  = "client_video_transformations_v1"
	FeatureRouteDiagnostics       = "playback_route_diagnostics"
	FeatureDeviceQuirksV3         = "device_quirks_v1"
	FeatureSeekReanchorV3         = "seek_reanchor_v1"
	PlanRecipeVersionV3           = "v3.2"
	ClientDV7ToDV81V3             = "client_dv7_to_dv81"
	ClientDV7ToHDR10V3            = "client_dv7_to_hdr10"
	ClientDVTransformVersionV3    = "1"
	ClientDV8HDR10PlusSanitizerV3 = "client_dv8_hdr10plus_sanitizer_v1"
	ClientPostResumeRecoveryV3    = "client_post_resume_video_recovery_v1"
	ClientSurfaceRecoveryV3       = "client_surface_recovery_v1"
	DeviceQuirkRegistryRevisionV3 = "2026-07-13.1"
)

type DecisionOutcomeV3 string

const (
	OutcomePlayableV3              DecisionOutcomeV3 = "playable"
	OutcomeAdaptationUnavailableV3 DecisionOutcomeV3 = "adaptation_unavailable"
)

type DeliveryV3 string

const (
	DeliveryOriginalHTTPV3     DeliveryV3 = "original_http"
	DeliveryRemuxHLSV3         DeliveryV3 = "server_remux_hls"
	DeliveryRemuxProgressiveV3 DeliveryV3 = "server_remux_progressive"
	DeliveryTranscodeHLSV3     DeliveryV3 = "server_transcode_hls"
)

func (d DeliveryV3) KotlinName() string {
	switch d {
	case DeliveryOriginalHTTPV3:
		return "ORIGINAL_HTTP"
	case DeliveryRemuxHLSV3:
		return "SERVER_REMUX_HLS"
	case DeliveryRemuxProgressiveV3:
		return "SERVER_REMUX_PROGRESSIVE"
	case DeliveryTranscodeHLSV3:
		return "SERVER_TRANSCODE_HLS"
	default:
		return strings.ToUpper(string(d))
	}
}

type EngineV3 string

const (
	EngineMedia3DirectV3           EngineV3 = "media3_direct"
	EngineMedia3ProgressiveRemuxV3 EngineV3 = "media3_progressive_remux"
	EngineMedia3HLSV3              EngineV3 = "media3_hls"
)

type StreamProtocolV3 string

const (
	StreamHTTPProgressiveV3 StreamProtocolV3 = "http_progressive"
	StreamHLSV3             StreamProtocolV3 = "hls"
)

func (p StreamProtocolV3) KotlinName() string {
	switch p {
	case StreamHTTPProgressiveV3:
		return "HTTP_PROGRESSIVE"
	case StreamHLSV3:
		return "HLS"
	default:
		return strings.ToUpper(string(p))
	}
}

type HeaderRefreshModeV3 string

const (
	HeaderRefreshNoneV3    HeaderRefreshModeV3 = "none"
	HeaderRefreshSessionV3 HeaderRefreshModeV3 = "session"
)

type SubtitleModeV3 string

const (
	SubtitleOffV3     SubtitleModeV3 = "off"
	SubtitleRenderV3  SubtitleModeV3 = "render"
	SubtitleConvertV3 SubtitleModeV3 = "convert"
	SubtitleBurnInV3  SubtitleModeV3 = "burn_in"
)

func (m SubtitleModeV3) KotlinName() string {
	switch m {
	case SubtitleOffV3:
		return "OFF"
	case SubtitleRenderV3:
		return "RENDER"
	case SubtitleConvertV3:
		return "CONVERT"
	case SubtitleBurnInV3:
		return "BURN_IN"
	default:
		return strings.ToUpper(string(m))
	}
}

type SubtitleFidelityV3 string

const (
	SubtitleFidelityPreserveV3   SubtitleFidelityV3 = "preserve"
	SubtitleFidelityCompatibleV3 SubtitleFidelityV3 = "compatible"
)

type EnhancementLayerV3 string

const (
	EnhancementNoneV3    EnhancementLayerV3 = "none"
	EnhancementMELV3     EnhancementLayerV3 = "mel"
	EnhancementFELV3     EnhancementLayerV3 = "fel"
	EnhancementUnknownV3 EnhancementLayerV3 = "unknown"
)

type HDRCapabilitiesV3 struct {
	HDR10               bool  `json:"hdr10"`
	HDR10Plus           bool  `json:"hdr10_plus"`
	HLG                 bool  `json:"hlg"`
	DolbyVisionProfiles []int `json:"dolby_vision_profiles"`
}

type AudioPassthroughV3 struct {
	PassthroughCodecs  []string                  `json:"passthrough_codecs"`
	SpatializerEnabled bool                      `json:"spatializer_enabled"`
	MaxChannels        int                       `json:"max_channels"`
	Entries            []AudioPassthroughEntryV3 `json:"entries,omitempty"`
}

type AudioPassthroughEntryV3 struct {
	Codec         string   `json:"codec"`
	ChannelCounts []int    `json:"channel_counts,omitempty"`
	Layouts       []string `json:"layouts,omitempty"`
}

type VideoDecodeCapabilityV3 struct {
	Codec          string   `json:"codec"`
	DecoderName    string   `json:"decoder_name,omitempty"`
	Profiles       []string `json:"profiles,omitempty"`
	Levels         []int    `json:"levels,omitempty"`
	BitDepths      []int    `json:"bit_depths,omitempty"`
	MaxWidth       int      `json:"max_width,omitempty"`
	MaxHeight      int      `json:"max_height,omitempty"`
	MaxFrameRate   float64  `json:"max_frame_rate,omitempty"`
	MaxBitrateKbps int      `json:"max_bitrate_kbps,omitempty"`
	Hardware       bool     `json:"hardware"`
}

type ClientCodecCapabilitiesV3 struct {
	CodecsVideo         []string                  `json:"codecs_video"`
	CodecsVideoHardware []string                  `json:"codecs_video_hardware"`
	CodecsAudio         []string                  `json:"codecs_audio"`
	Containers          []string                  `json:"containers"`
	MaxResolution       string                    `json:"max_resolution,omitempty"`
	HDR                 bool                      `json:"hdr"`
	HDRDetails          *HDRCapabilitiesV3        `json:"hdr_details,omitempty"`
	AudioPassthrough    *AudioPassthroughV3       `json:"audio_passthrough,omitempty"`
	VideoDecode         []VideoDecodeCapabilityV3 `json:"video_decode,omitempty"`
}

type DeviceContextV3 struct {
	Manufacturer    string   `json:"manufacturer,omitempty"`
	Model           string   `json:"model,omitempty"`
	Brand           string   `json:"brand,omitempty"`
	Device          string   `json:"device,omitempty"`
	Product         string   `json:"product,omitempty"`
	SoCManufacturer string   `json:"soc_manufacturer,omitempty"`
	SoCModel        string   `json:"soc_model,omitempty"`
	BuildID         string   `json:"build_id,omitempty"`
	BuildDisplay    string   `json:"build_display,omitempty"`
	SecurityPatch   string   `json:"security_patch,omitempty"`
	SDKInt          int      `json:"sdk_int,omitempty"`
	ABIs            []string `json:"abis,omitempty"`
}

type OutputContextV3 struct {
	HDRDetails            *HDRCapabilitiesV3  `json:"hdr_details,omitempty"`
	AudioPassthrough      *AudioPassthroughV3 `json:"audio_passthrough,omitempty"`
	CurrentSink           string              `json:"current_sink,omitempty"`
	SinkType              string              `json:"sink_type,omitempty"`
	OutputRouteGeneration int64               `json:"output_route_generation"`
}

type EngineSubtitleCapabilitiesV3 struct {
	EmbeddedText    bool `json:"embedded_text"`
	SidecarText     bool `json:"sidecar_text"`
	ASSStyling      bool `json:"ass_styling"`
	EmbeddedBitmap  bool `json:"embedded_bitmap"`
	SidecarBitmap   bool `json:"sidecar_bitmap"`
	FontAttachments bool `json:"font_attachments"`
}

type EngineCapabilityV3 struct {
	Enabled                bool                         `json:"enabled"`
	SupportedOnDevice      bool                         `json:"supported_on_device"`
	FailureReason          string                       `json:"failure_reason,omitempty"`
	Containers             []string                     `json:"containers"`
	VideoCodecs            []string                     `json:"video_codecs"`
	AudioDecodeCodecs      []string                     `json:"audio_decode_codecs"`
	AudioPassthroughCodecs []string                     `json:"audio_passthrough_codecs"`
	MaxChannels            *int                         `json:"max_channels,omitempty"`
	HDRDetails             *HDRCapabilitiesV3           `json:"hdr_details,omitempty"`
	Subtitles              EngineSubtitleCapabilitiesV3 `json:"subtitles"`
	Features               []string                     `json:"features"`
	AuthHeaderRefresh      bool                         `json:"auth_header_refresh"`
	ValidatedClaims        []string                     `json:"validated_claims"`
	Transformations        []TransformationV3           `json:"transformations"`
}

type ClientPlaybackContextV3 struct {
	ProtocolVersion int                           `json:"protocol_version"`
	Features        []string                      `json:"features"`
	Platform        string                        `json:"platform"`
	FormFactor      string                        `json:"form_factor"`
	AppVersion      string                        `json:"app_version"`
	Device          DeviceContextV3               `json:"device"`
	Output          OutputContextV3               `json:"output"`
	Engines         map[string]EngineCapabilityV3 `json:"engines"`
}

type StartRequestV3 struct {
	ProtocolVersion            int                       `json:"protocol_version"`
	ClientFeatures             []string                  `json:"client_features"`
	FileID                     int                       `json:"file_id"`
	ProfileID                  string                    `json:"profile_id"`
	PlaybackAttemptID          string                    `json:"playback_attempt_id"`
	QualityPreference          string                    `json:"quality_preference"`
	SubtitleFidelityPreference SubtitleFidelityV3        `json:"subtitle_fidelity_preference"`
	StartPosition              *float64                  `json:"start_position,omitempty"`
	AudioTrackID               string                    `json:"audio_track_id,omitempty"`
	AudioTrackIndex            *int                      `json:"audio_track_index,omitempty"`
	SubtitleTrackID            string                    `json:"subtitle_track_id,omitempty"`
	SubtitleTrackIndex         *int                      `json:"subtitle_track_index,omitempty"`
	OutputRouteGeneration      int64                     `json:"output_route_generation"`
	Metered                    bool                      `json:"metered"`
	BandwidthEstimateKbps      *int                      `json:"bandwidth_estimate_kbps,omitempty"`
	BandwidthCapKbps           *int                      `json:"bandwidth_cap_kbps,omitempty"`
	Capabilities               ClientCodecCapabilitiesV3 `json:"client_capabilities"`
	ClientPlaybackContext      ClientPlaybackContextV3   `json:"client_playback_context"`
}

type TrackIdentityV3 struct {
	ID    string `json:"id"`
	Index *int   `json:"index,omitempty"`
}

type SelectedTracksV3 struct {
	Audio    *TrackIdentityV3 `json:"audio,omitempty"`
	Subtitle *TrackIdentityV3 `json:"subtitle,omitempty"`
}

type FailureV3 struct {
	Classification string `json:"classification"`
	Message        string `json:"message,omitempty"`
	DecoderName    string `json:"decoder_name,omitempty"`
}

type ReplanOperationV3 string

const (
	ReplanOperationFailureRecoveryV3     ReplanOperationV3 = "failure_recovery"
	ReplanOperationSeekReanchorV3        ReplanOperationV3 = "seek_reanchor"
	ReplanOperationSeekFailureRecoveryV3 ReplanOperationV3 = "seek_failure_recovery"
)

type ReplanRequestV3 struct {
	ProtocolVersion       int                       `json:"protocol_version"`
	Operation             ReplanOperationV3         `json:"operation,omitempty"`
	PlaybackAttemptID     string                    `json:"playback_attempt_id"`
	ReplanRequestID       string                    `json:"replan_request_id"`
	FailedPlanID          string                    `json:"failed_plan_id"`
	PlanAttemptID         string                    `json:"plan_attempt_id"`
	PlanAttemptKey        string                    `json:"plan_attempt_key"`
	AttemptedPlanKeys     []string                  `json:"attempted_plan_keys"`
	AttemptCount          int                       `json:"attempt_count"`
	QualityPreference     string                    `json:"quality_preference"`
	PositionSeconds       float64                   `json:"position_seconds"`
	OutputRouteGeneration int64                     `json:"output_route_generation"`
	Metered               bool                      `json:"metered"`
	BandwidthEstimateKbps *int                      `json:"bandwidth_estimate_kbps,omitempty"`
	BandwidthCapKbps      *int                      `json:"bandwidth_cap_kbps,omitempty"`
	SelectedTracks        SelectedTracksV3          `json:"selected_tracks"`
	Failure               FailureV3                 `json:"failure"`
	Capabilities          ClientCodecCapabilitiesV3 `json:"client_capabilities"`
	ClientPlaybackContext ClientPlaybackContextV3   `json:"client_playback_context"`
}

const (
	RouteEventPlanSelectedV3               = "plan_selected"
	RouteEventPlanInvalidatedV3            = "plan_invalidated"
	RouteEventPlanFailedV3                 = "plan_failed"
	RouteEventFirstFrameV3                 = "first_frame"
	RouteEventTerminalV3                   = "terminal"
	RouteEventStoppedV3                    = "stopped"
	RouteEventRuntimeCorrectionAppliedV3   = "runtime_correction_applied"
	RouteEventRuntimeCorrectionSucceededV3 = "runtime_correction_succeeded"
	RouteEventRuntimeCorrectionFailedV3    = "runtime_correction_failed"
	RouteEventSeekReanchorRequestedV3      = "seek_reanchor_requested"
	RouteEventSeekReanchoredV3             = "seek_reanchored"
)

var routeEventNamesV3 = []string{
	RouteEventPlanSelectedV3,
	RouteEventPlanInvalidatedV3,
	RouteEventPlanFailedV3,
	RouteEventFirstFrameV3,
	RouteEventTerminalV3,
	RouteEventStoppedV3,
	RouteEventRuntimeCorrectionAppliedV3,
	RouteEventRuntimeCorrectionSucceededV3,
	RouteEventRuntimeCorrectionFailedV3,
	RouteEventSeekReanchorRequestedV3,
	RouteEventSeekReanchoredV3,
}

// RouteEventNamesV3 returns the complete protocol-v3 telemetry event contract.
func RouteEventNamesV3() []string {
	return append([]string(nil), routeEventNamesV3...)
}

// ValidRouteEventNameV3 reports whether name is part of the protocol-v3
// telemetry contract shared by handlers, persistence, and clients.
func ValidRouteEventNameV3(name string) bool {
	return slices.Contains(routeEventNamesV3, name)
}

// EffectiveOperation keeps clients which predate the explicit operation field
// on the ordinary failure-recovery path. Seek operations are deliberately
// opt-in because both pin the current media version and user intent; an exact
// reanchor also preserves the current route instead of walking the ladder.
func (r ReplanRequestV3) EffectiveOperation() ReplanOperationV3 {
	if r.Operation == "" {
		return ReplanOperationFailureRecoveryV3
	}
	return r.Operation
}

type RouteEventV3 struct {
	ProtocolVersion       int               `json:"protocol_version"`
	PlaybackAttemptID     string            `json:"playback_attempt_id"`
	SessionID             string            `json:"session_id,omitempty"`
	PlanID                string            `json:"plan_id,omitempty"`
	PlanAttemptID         string            `json:"plan_attempt_id,omitempty"`
	PlanAttemptKey        string            `json:"plan_attempt_key,omitempty"`
	Event                 string            `json:"event"`
	FailureClassification string            `json:"failure_classification,omitempty"`
	FallbackReason        string            `json:"fallback_reason,omitempty"`
	AppliedQuirkIDs       []string          `json:"applied_quirk_ids,omitempty"`
	QuirkRegistryRevision string            `json:"quirk_registry_revision,omitempty"`
	OutputRouteGeneration int64             `json:"output_route_generation"`
	Diagnostics           map[string]string `json:"diagnostics"`
}

type StreamV3 struct {
	URL              string              `json:"url"`
	Protocol         StreamProtocolV3    `json:"protocol"`
	Container        string              `json:"container,omitempty"`
	MIMEType         string              `json:"mime_type,omitempty"`
	Headers          map[string]string   `json:"headers"`
	HeaderRefresh    HeaderRefreshModeV3 `json:"header_refresh"`
	HeaderRefreshURL string              `json:"header_refresh_url,omitempty"`
}

type TimelineV3 struct {
	SourceStartSeconds     float64  `json:"source_start_seconds"`
	StreamOriginSeconds    float64  `json:"stream_origin_seconds"`
	PlayerStartSeconds     float64  `json:"player_start_seconds"`
	TimelineOffsetSeconds  float64  `json:"timeline_offset_seconds"`
	SeekWindowStartSeconds *float64 `json:"seek_window_start_seconds,omitempty"`
	SeekWindowEndSeconds   *float64 `json:"seek_window_end_seconds,omitempty"`
	CanSeekAnywhere        bool     `json:"can_seek_anywhere"`
	SeekRestoration        string   `json:"seek_restoration"`
}

type EffectiveRecipeV3 struct {
	VideoCodec    string   `json:"video_codec,omitempty"`
	AudioCodec    string   `json:"audio_codec,omitempty"`
	Width         *int     `json:"width,omitempty"`
	Height        *int     `json:"height,omitempty"`
	FrameRate     *float64 `json:"frame_rate,omitempty"`
	BitrateKbps   *int     `json:"bitrate_kbps,omitempty"`
	DynamicRange  string   `json:"dynamic_range,omitempty"`
	AudioChannels *int     `json:"audio_channels,omitempty"`
	AudioLayout   string   `json:"audio_layout,omitempty"`
}

type SourceDescriptorV3 struct {
	MediaFileID        int                `json:"media_file_id"`
	Container          string             `json:"container,omitempty"`
	VideoCodec         string             `json:"video_codec,omitempty"`
	VideoProfile       string             `json:"video_profile,omitempty"`
	VideoLevel         int                `json:"video_level,omitempty"`
	BitDepth           int                `json:"bit_depth,omitempty"`
	Width              int                `json:"width,omitempty"`
	Height             int                `json:"height,omitempty"`
	FrameRate          float64            `json:"frame_rate,omitempty"`
	BitrateKbps        int                `json:"bitrate_kbps,omitempty"`
	DynamicRange       string             `json:"dynamic_range,omitempty"`
	HDR10Plus          bool               `json:"hdr10_plus"`
	DVProfile          int                `json:"dolby_vision_profile,omitempty"`
	DVBLCompatID       int                `json:"dv_bl_compat_id,omitempty"`
	DVEnhancementLayer EnhancementLayerV3 `json:"dv_enhancement_layer"`
	AudioCodec         string             `json:"audio_codec,omitempty"`
	AudioChannels      int                `json:"audio_channels,omitempty"`
	AudioLayout        string             `json:"audio_layout,omitempty"`
}

type VideoClaimsV3 struct {
	HDR10             bool   `json:"hdr10"`
	HDR10Plus         bool   `json:"hdr10_plus"`
	HLG               bool   `json:"hlg"`
	DolbyVision       bool   `json:"dolby_vision"`
	DolbyVisionReason string `json:"dolby_vision_reason,omitempty"`
}

type AudioClaimsV3 struct {
	Codec          string `json:"codec,omitempty"`
	Passthrough    bool   `json:"passthrough"`
	AtmosPreserved bool   `json:"atmos_preserved"`
	DTSVariant     string `json:"dts_variant,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type SubtitleClaimsV3 struct {
	ASSStylingPreserved bool   `json:"ass_styling_preserved"`
	BitmapOverlay       bool   `json:"bitmap_overlay"`
	BitmapSidecar       bool   `json:"bitmap_sidecar"`
	Reason              string `json:"reason,omitempty"`
}

type ValidationClaimsV3 struct {
	Video     VideoClaimsV3    `json:"video"`
	Audio     AudioClaimsV3    `json:"audio"`
	Subtitles SubtitleClaimsV3 `json:"subtitles"`
}

type SubtitleArtifactV3 struct {
	URL                 string  `json:"url"`
	MIMEType            string  `json:"mime_type"`
	Format              string  `json:"format"`
	TimingOriginSeconds float64 `json:"timing_origin_seconds"`
}

type SubtitleDecisionV3 struct {
	Mode     SubtitleModeV3      `json:"mode"`
	TrackID  string              `json:"track_id,omitempty"`
	Artifact *SubtitleArtifactV3 `json:"artifact,omitempty"`
}

type TransformationV3 struct {
	Name            string   `json:"name"`
	Executor        string   `json:"executor"`
	RecipeVersion   string   `json:"recipe_version"`
	ValidatedClaims []string `json:"validated_claims"`
}

type AppliedQuirkV3 struct {
	ID               string `json:"id"`
	RegistryRevision string `json:"registry_revision"`
	Action           string `json:"action"`
	Reason           string `json:"reason,omitempty"`
}

type DegradationWarningV3 struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PlanV3 struct {
	ProtocolVersion        int                    `json:"protocol_version"`
	PlanID                 string                 `json:"plan_id"`
	SessionID              string                 `json:"session_id,omitempty"`
	ExpiresAt              string                 `json:"expires_at,omitempty"`
	Delivery               DeliveryV3             `json:"delivery"`
	Engine                 EngineV3               `json:"engine"`
	Stream                 StreamV3               `json:"stream"`
	Timeline               TimelineV3             `json:"timeline"`
	SelectedTracks         SelectedTracksV3       `json:"selected_tracks"`
	EffectiveRecipe        EffectiveRecipeV3      `json:"effective_recipe"`
	Claims                 ValidationClaimsV3     `json:"claims"`
	Subtitle               SubtitleDecisionV3     `json:"subtitle"`
	Transformations        []TransformationV3     `json:"transformations"`
	AppliedQuirks          []AppliedQuirkV3       `json:"applied_quirks"`
	RuntimeCorrections     []string               `json:"runtime_corrections"`
	DegradationWarnings    []DegradationWarningV3 `json:"degradation_warnings"`
	DecisionReason         string                 `json:"decision_reason"`
	RequestedMediaFileID   int                    `json:"requested_media_file_id"`
	EffectiveMediaFileID   int                    `json:"effective_media_file_id"`
	Source                 SourceDescriptorV3     `json:"source"`
	SubtitleFidelityPolicy string                 `json:"subtitle_fidelity_policy"`
}

type TerminalV3 struct {
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type DecisionResponseV3 struct {
	ProtocolVersion int               `json:"protocol_version,omitempty"`
	ServerFeatures  []string          `json:"server_features"`
	Outcome         DecisionOutcomeV3 `json:"outcome,omitempty"`
	SessionID       string            `json:"session_id,omitempty"`
	PlaybackPlan    *PlanV3           `json:"playback_plan,omitempty"`
	Terminal        *TerminalV3       `json:"terminal,omitempty"`
}

type CapabilityResponseV3 struct {
	Enabled          bool               `json:"enabled"`
	ProtocolVersions []int              `json:"protocol_versions"`
	Features         []string           `json:"features"`
	Deliveries       []DeliveryV3       `json:"deliveries"`
	Transformations  []TransformationV3 `json:"transformations"`
	Reason           string             `json:"reason,omitempty"`
}

var boundedIdentifierV3 = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)

func (r *StartRequestV3) NormalizeAndValidate() ([]DegradationWarningV3, error) {
	if r.ProtocolVersion != ProtocolV3 {
		return nil, fmt.Errorf("protocol_version must be %d", ProtocolV3)
	}
	if r.FileID <= 0 || strings.TrimSpace(r.ProfileID) == "" {
		return nil, errors.New("file_id and profile_id are required")
	}
	if !boundedIdentifierV3.MatchString(r.PlaybackAttemptID) {
		return nil, errors.New("playback_attempt_id is invalid")
	}
	if r.OutputRouteGeneration < 0 || r.ClientPlaybackContext.Output.OutputRouteGeneration != r.OutputRouteGeneration {
		return nil, errors.New("output_route_generation is invalid or inconsistent")
	}
	if r.ClientPlaybackContext.ProtocolVersion != ProtocolV3 {
		return nil, errors.New("client_playback_context.protocol_version must be 3")
	}
	if r.StartPosition != nil && (!isFiniteV3(*r.StartPosition) || *r.StartPosition < 0 || *r.StartPosition > 31_536_000) {
		return nil, errors.New("start_position is outside the supported range")
	}
	if err := validateOptionalBoundedIntV3(r.BandwidthEstimateKbps, 100, 1_000_000, "bandwidth_estimate_kbps"); err != nil {
		return nil, err
	}
	if err := validateOptionalBoundedIntV3(r.BandwidthCapKbps, 100, 1_000_000, "bandwidth_cap_kbps"); err != nil {
		return nil, err
	}
	if r.SubtitleFidelityPreference != SubtitleFidelityPreserveV3 && r.SubtitleFidelityPreference != SubtitleFidelityCompatibleV3 {
		return nil, errors.New("subtitle_fidelity_preference is invalid")
	}
	if len(r.ClientFeatures) > 64 {
		return nil, errors.New("client_features exceeds supported size")
	}
	for _, feature := range r.ClientFeatures {
		if len(feature) > 128 {
			return nil, errors.New("client feature exceeds supported size")
		}
	}
	if err := validateCapabilitiesV3(&r.Capabilities, &r.ClientPlaybackContext, r.ClientFeatures); err != nil {
		return nil, err
	}
	if err := validateTrackPairV3(r.FileID, "audio", r.AudioTrackID, r.AudioTrackIndex); err != nil {
		return nil, err
	}
	if err := validateTrackPairV3(r.FileID, "subtitle", r.SubtitleTrackID, r.SubtitleTrackIndex); err != nil {
		return nil, err
	}
	quality, changed := NormalizeQualityV3(r.QualityPreference)
	r.QualityPreference = quality
	if changed {
		return []DegradationWarningV3{{Code: "quality_preference_normalized", Message: "Unknown quality preference was normalized to auto."}}, nil
	}
	return nil, nil
}

func NormalizeQualityV3(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "auto", false
	case "original", "source", "max":
		return "original", false
	case "2160p", "4k", "uhd":
		return "2160p", false
	case "1080p", "fhd":
		return "1080p", false
	case "720p", "hd":
		return "720p", false
	case "480p", "sd":
		return "480p", false
	default:
		return "auto", true
	}
}

func (r ReplanRequestV3) Validate() error {
	if r.ProtocolVersion != ProtocolV3 || !boundedIdentifierV3.MatchString(r.PlaybackAttemptID) || !boundedIdentifierV3.MatchString(r.ReplanRequestID) {
		return errors.New("invalid replan identity")
	}
	if len(r.FailedPlanID) < 8 || len(r.FailedPlanID) > 128 || len(r.PlanAttemptID) < 8 || len(r.PlanAttemptID) > 128 || len(r.PlanAttemptKey) < 8 || len(r.PlanAttemptKey) > 128 || !strings.HasPrefix(r.PlanAttemptKey, "v3:") || r.AttemptCount < 1 || r.AttemptCount > 8 {
		return errors.New("invalid replan attempt")
	}
	if r.OutputRouteGeneration < 0 || r.ClientPlaybackContext.Output.OutputRouteGeneration != r.OutputRouteGeneration {
		return errors.New("invalid output route generation")
	}
	if len(r.AttemptedPlanKeys) > 16 || len(r.Failure.Classification) > 64 || len(r.Failure.Message) > 512 || len(r.Failure.DecoderName) > 128 || !isFiniteV3(r.PositionSeconds) || r.PositionSeconds < 0 || r.PositionSeconds > 31_536_000 {
		return errors.New("replan bounds exceeded")
	}
	if err := validateOptionalBoundedIntV3(r.BandwidthEstimateKbps, 100, 1_000_000, "bandwidth_estimate_kbps"); err != nil {
		return err
	}
	if err := validateOptionalBoundedIntV3(r.BandwidthCapKbps, 100, 1_000_000, "bandwidth_cap_kbps"); err != nil {
		return err
	}
	if err := validateSelectedTrackIdentityV3("audio", r.SelectedTracks.Audio); err != nil {
		return err
	}
	if err := validateSelectedTrackIdentityV3("subtitle", r.SelectedTracks.Subtitle); err != nil {
		return err
	}
	switch r.EffectiveOperation() {
	case ReplanOperationFailureRecoveryV3, ReplanOperationSeekFailureRecoveryV3:
		if r.Failure.Classification == "" {
			return errors.New("failure recovery requires a failure classification")
		}
	case ReplanOperationSeekReanchorV3:
		// An exact seek reanchor is a timeline operation, not a failed recipe.
		// Classification remains accepted for older callers but is not required
		// and never selects seek semantics.
	default:
		return errors.New("invalid replan operation")
	}
	for _, key := range r.AttemptedPlanKeys {
		if len(key) > 128 || !strings.HasPrefix(key, "v3:") {
			return errors.New("invalid attempted plan key")
		}
	}
	return validateCapabilitiesV3(&r.Capabilities, &r.ClientPlaybackContext, nil)
}

func validateSelectedTrackIdentityV3(kind string, track *TrackIdentityV3) error {
	if track == nil {
		return nil
	}
	if len(track.ID) > 128 {
		return fmt.Errorf("%s track id exceeds supported size", kind)
	}
	if track.Index != nil && (*track.Index < 0 || *track.Index > 10_000) {
		return fmt.Errorf("%s track index is invalid", kind)
	}
	return nil
}

// validateCapabilitiesV3 validates and normalizes the shared capability
// payload. topLevelFeatures carries the request's client_features when the
// request type has one (replans do not); feature checks accept either
// location, matching the planner's dual-source reads.
func validateCapabilitiesV3(c *ClientCodecCapabilitiesV3, ctx *ClientPlaybackContextV3, topLevelFeatures []string) error {
	if len(c.CodecsVideo) > 64 || len(c.CodecsVideoHardware) > 64 || len(c.CodecsAudio) > 64 || len(c.Containers) > 64 || len(c.VideoDecode) > 64 || len(ctx.Features) > 64 || len(ctx.Engines) > 16 || len(ctx.Device.ABIs) > 16 || len(ctx.Platform) > 32 || len(ctx.FormFactor) > 32 || len(ctx.AppVersion) > 64 {
		return errors.New("capability list exceeds supported size")
	}
	deviceValues := []string{
		ctx.Device.Manufacturer, ctx.Device.Model, ctx.Device.Brand, ctx.Device.Device,
		ctx.Device.Product, ctx.Device.SoCManufacturer, ctx.Device.SoCModel, ctx.Device.BuildID,
		ctx.Device.BuildDisplay, ctx.Device.SecurityPatch, ctx.Output.CurrentSink, ctx.Output.SinkType,
	}
	for _, value := range deviceValues {
		if len(value) > 128 {
			return errors.New("device capability value exceeds supported size")
		}
	}
	for _, values := range [][]string{c.CodecsVideo, c.CodecsVideoHardware, c.CodecsAudio, c.Containers, ctx.Features} {
		for i := range values {
			values[i] = strings.ToLower(strings.TrimSpace(values[i]))
			if len(values[i]) > 128 {
				return errors.New("capability value exceeds supported size")
			}
		}
	}
	for i := range c.VideoDecode {
		c.VideoDecode[i].Codec = strings.ToLower(strings.TrimSpace(c.VideoDecode[i].Codec))
		if c.VideoDecode[i].Codec == "" || len(c.VideoDecode[i].DecoderName) > 128 || c.VideoDecode[i].MaxWidth < 0 || c.VideoDecode[i].MaxHeight < 0 || c.VideoDecode[i].MaxFrameRate < 0 || c.VideoDecode[i].MaxBitrateKbps < 0 {
			return errors.New("invalid detailed video capability")
		}
		if len(c.VideoDecode[i].Profiles) > 64 || len(c.VideoDecode[i].Levels) > 64 || len(c.VideoDecode[i].BitDepths) > 64 {
			return errors.New("detailed video capability exceeds supported size")
		}
		for _, profile := range c.VideoDecode[i].Profiles {
			if len(profile) > 64 {
				return errors.New("detailed video capability value exceeds supported size")
			}
		}
	}
	for _, hdr := range []*HDRCapabilitiesV3{c.HDRDetails, ctx.Output.HDRDetails} {
		if hdr != nil && len(hdr.DolbyVisionProfiles) > 16 {
			return errors.New("dolby vision profile list exceeds supported size")
		}
	}
	for name, engine := range ctx.Engines {
		if len(name) > 64 || len(engine.Containers) > 64 || len(engine.VideoCodecs) > 64 || len(engine.AudioDecodeCodecs) > 64 || len(engine.AudioPassthroughCodecs) > 64 || len(engine.Features) > 64 || len(engine.ValidatedClaims) > 64 || len(engine.Transformations) > 16 {
			return errors.New("engine capability exceeds supported size")
		}
		if engine.HDRDetails != nil && len(engine.HDRDetails.DolbyVisionProfiles) > 16 {
			return errors.New("dolby vision profile list exceeds supported size")
		}
		for _, values := range [][]string{engine.Containers, engine.VideoCodecs, engine.AudioDecodeCodecs, engine.AudioPassthroughCodecs, engine.Features, engine.ValidatedClaims} {
			for _, value := range values {
				if len(value) > 64 {
					return errors.New("engine capability value exceeds supported size")
				}
			}
		}
		seenTransformations := make(map[string]struct{}, len(engine.Transformations))
		for i := range engine.Transformations {
			transformation := &engine.Transformations[i]
			transformation.Name = strings.ToLower(strings.TrimSpace(transformation.Name))
			transformation.Executor = strings.ToLower(strings.TrimSpace(transformation.Executor))
			transformation.RecipeVersion = strings.TrimSpace(transformation.RecipeVersion)
			if transformation.Name == "" || len(transformation.Name) > 64 ||
				(transformation.Executor != "client" && transformation.Executor != "server") ||
				transformation.RecipeVersion == "" || len(transformation.RecipeVersion) > 32 ||
				len(transformation.ValidatedClaims) > 32 {
				return errors.New("invalid engine transformation capability")
			}
			if transformation.Executor == "client" {
				if !engine.Enabled || !engine.SupportedOnDevice ||
					(!HasFeatureV3(ctx.Features, FeatureClientVideoTransforms) && !HasFeatureV3(topLevelFeatures, FeatureClientVideoTransforms)) {
					return errors.New("client transformation capability is not enabled")
				}
			}
			key := transformation.Executor + ":" + transformation.Name + ":" + transformation.RecipeVersion
			if _, exists := seenTransformations[key]; exists {
				return errors.New("duplicate engine transformation capability")
			}
			seenTransformations[key] = struct{}{}
			for _, claim := range transformation.ValidatedClaims {
				if len(claim) > 128 {
					return errors.New("transformation claim exceeds supported size")
				}
			}
		}
		ctx.Engines[name] = engine
	}
	for _, abi := range ctx.Device.ABIs {
		if len(abi) > 64 {
			return errors.New("device ABI exceeds supported size")
		}
	}
	for _, passthrough := range []*AudioPassthroughV3{c.AudioPassthrough, ctx.Output.AudioPassthrough} {
		if passthrough == nil {
			continue
		}
		if len(passthrough.PassthroughCodecs) > 64 || len(passthrough.Entries) > 64 || passthrough.MaxChannels < 0 || passthrough.MaxChannels > 64 {
			return errors.New("audio passthrough capability exceeds supported size")
		}
		for _, entry := range passthrough.Entries {
			if len(entry.Codec) > 64 || len(entry.ChannelCounts) > 32 || len(entry.Layouts) > 32 {
				return errors.New("audio passthrough entry exceeds supported size")
			}
		}
	}
	return nil
}

func validateTrackPairV3(fileID int, kind, id string, index *int) error {
	if len(id) > 128 {
		return fmt.Errorf("%s_track_id exceeds supported size", kind)
	}
	if index != nil && (*index < 0 || *index > 10_000) {
		return fmt.Errorf("%s_track_index is invalid", kind)
	}
	if id == "" || index == nil {
		return nil
	}
	want := TrackIDV3(fileID, kind, *index)
	if id != want {
		return fmt.Errorf("%s track id and index disagree", kind)
	}
	return nil
}

func validateOptionalBoundedIntV3(v *int, min, max int, name string) error {
	if v != nil && (*v < min || *v > max) {
		return fmt.Errorf("%s is outside the supported range", name)
	}
	return nil
}

func isFiniteV3(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func HasFeatureV3(features []string, wanted string) bool {
	return slices.ContainsFunc(features, func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), wanted) })
}

func NewTerminalResponseV3(reason, message string, retryable bool) DecisionResponseV3 {
	return DecisionResponseV3{
		ProtocolVersion: ProtocolV3,
		ServerFeatures:  []string{FeaturePlaybackPlanV3, FeatureMedia3Only, FeatureDeviceQuirksV3, FeatureSeekReanchorV3},
		Outcome:         OutcomeAdaptationUnavailableV3,
		Terminal:        &TerminalV3{Reason: reason, Message: message, Retryable: retryable},
	}
}

func DisabledResponseV3() DecisionResponseV3 {
	return DecisionResponseV3{ProtocolVersion: ProtocolV3, ServerFeatures: []string{}}
}

func NewPlanExpiryV3(now time.Time) string { return now.Add(MaxTokenTTL).UTC().Format(time.RFC3339) }
