package playback

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type PlannerSettingsV3 struct {
	TranscodeEnabled bool
	Allow4KTranscode bool
}

type PlannerInputV3 struct {
	Request             StartRequestV3
	RequestedFile       *models.MediaFile
	EffectiveFile       *models.MediaFile
	AudioTrackIndex     int
	Settings            PlannerSettingsV3
	Registry            *TransformationRegistryV3
	Now                 time.Time
	AttemptedKeys       []string
	AdditionalSubtitles []SubtitleInventoryEntryV3
}

type PlannerResultV3 struct {
	Plan                        *PlanV3
	Terminal                    *TerminalV3
	PlayMethod                  PlayMethod
	TranscodeAudio              bool
	TargetVideoCodec            string
	TargetAudioCodec            string
	TargetResolution            string
	TargetBitrateKbps           int
	SubtitleTrackIndex          int
	SubtitleTransportTrackIndex int
	SubtitleBurnIn              bool
	SubtitleCodec               string
}

func PlanPlaybackV3(input PlannerInputV3) PlannerResultV3 {
	if input.RequestedFile == nil {
		return terminalPlannerResultV3("source_unavailable", "The requested media source is unavailable.", false)
	}
	file := input.EffectiveFile
	if file == nil {
		file = input.RequestedFile
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	source := SourceDescriptorFromFileV3(file, input.AudioTrackIndex)
	// Subtitle renderability is engine-specific, so every candidate route is
	// validated against the capabilities of the engine that would execute it.
	// The direct engine remains the canonical policy for source-preserving
	// routes and for the up-front terminal decision.
	subtitle := ResolveSubtitlePolicyV3(file, input.Request, input.Settings.TranscodeEnabled, EngineMedia3DirectV3, input.AdditionalSubtitles)
	if subtitle.Terminal != nil {
		return PlannerResultV3{Terminal: subtitle.Terminal, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
	}
	remuxSubtitle := ResolveSubtitlePolicyV3(file, input.Request, input.Settings.TranscodeEnabled, EngineMedia3ProgressiveRemuxV3, input.AdditionalSubtitles)
	hlsSubtitle := ResolveSubtitlePolicyV3(file, input.Request, input.Settings.TranscodeEnabled, EngineMedia3HLSV3, input.AdditionalSubtitles)
	// A remux route cannot burn subtitles, so it is only viable when its own
	// engine can deliver the selected subtitle without one.
	remuxSubtitleOK := remuxSubtitle.Terminal == nil && !remuxSubtitle.RequiresBurn
	hlsRemuxSubtitleOK := hlsSubtitle.Terminal == nil && !hlsSubtitle.RequiresBurn
	quality := ResolveQualityPolicyV3(input.Request, source)
	videoOK := detailedVideoEligibleV3(source, input.Request)
	var high10Quirk *AppliedQuirkV3
	if !videoOK {
		if quirk, ok := high10DecodeOverrideV3(source, input.Request); ok {
			videoOK = true
			high10Quirk = quirk
		}
	}
	rangeOK, videoClaims := outputRangeEligibleV3(source, input.Request)
	audioOK, passthrough, audioClaims := audioEligibilityV3(source, input.Request)
	if !audioOK && source.AudioCodec == "" && (file == nil || len(file.AudioTracks) == 0) {
		// Video-only media has no audio stream to adapt: treating the absence
		// as an unsupported codec would force a pointless AAC conversion — or
		// a terminal when conversion is unavailable — on a playable file. An
		// audio track whose codec merely failed to probe keeps the codec
		// gate: converting unknown audio is safer than copying it.
		audioOK = true
		audioClaims.Reason = "no_audio_track"
	}
	containerOK := containsFoldV3(input.Request.Capabilities.Containers, source.Container)
	dvStripEligible := canStripDolbyVisionToHDR10V3(source, input.Request, input.Registry)
	clientDV81Eligible := canClientTransformDV7ToDV81V3(source, input.Request)
	clientHDR10Eligible := canClientTransformDV7ToHDR10V3(source, input.Request)

	base := PlanV3{
		ProtocolVersion:        ProtocolV3,
		ExpiresAt:              NewPlanExpiryV3(input.Now),
		SelectedTracks:         selectedTracksForPlanV3(file, input.AudioTrackIndex, subtitle),
		EffectiveRecipe:        recipeFromSourceV3(source),
		Claims:                 ValidationClaimsV3{Video: videoClaims, Audio: audioClaims, Subtitles: subtitle.Claims},
		Subtitle:               subtitle.Decision,
		Transformations:        []TransformationV3{},
		AppliedQuirks:          []AppliedQuirkV3{},
		RuntimeCorrections:     []string{},
		DegradationWarnings:    []DegradationWarningV3{},
		RequestedMediaFileID:   input.RequestedFile.ID,
		EffectiveMediaFileID:   file.ID,
		Source:                 source,
		SubtitleFidelityPolicy: subtitlePolicyNameV3(input.Request.SubtitleFidelityPreference),
		Timeline:               TimelineV3{SourceStartSeconds: floatOrZeroV3(input.Request.StartPosition), PlayerStartSeconds: floatOrZeroV3(input.Request.StartPosition), CanSeekAnywhere: true, SeekRestoration: "player_position"},
	}
	base.Claims.Audio.Passthrough = passthrough
	if source.DynamicRange == "hdr_unknown" && rangeOK {
		base.DegradationWarnings = append(base.DegradationWarnings, DegradationWarningV3{
			Code:    "hdr_range_assumed_hdr10",
			Message: "The source is flagged HDR without precise range metadata and is delivered as HDR10.",
		})
	}
	if !detailedVideoEvidenceCompleteV3(source) {
		return terminalPlannerResultV3("source_metadata_incomplete", "The source is missing video metadata required for a validated playback route.", true)
	}

	// Automatic quality reductions (device resolution limit, bandwidth
	// estimate/cap, metered fallback) are best-effort. When the only reason to
	// transcode is such a reduction, a validated source-preserving route
	// exists, and the transcode itself cannot execute (HDR sources have no
	// validated reduced-quality recipe yet, or the client/server lacks the
	// transcode route entirely), deliver the source at original quality with a
	// degradation warning instead of refusing playback. Explicit user-selected
	// rungs keep the existing terminals.
	if quality.RequiresTranscode && !quality.ExplicitRung && !subtitle.RequiresBurn && videoOK &&
		(rangeOK || dvStripEligible || clientDV81Eligible || clientHDR10Eligible) &&
		!videoTranscodeExecutableV3(input, source) {
		warnings := append(quality.Warnings, DegradationWarningV3{
			Code:    "quality_reduction_unavailable",
			Message: "Reduced-quality transcoding is unavailable for this source; it is delivered at original quality.",
		})
		quality = originalQualityResultV3(source)
		quality.Warnings = warnings
	}
	base.DegradationWarnings = append(base.DegradationWarnings, quality.Warnings...)

	if quality.RequiresTranscode || !videoOK ||
		(!rangeOK && !dvStripEligible && !clientDV81Eligible && !clientHDR10Eligible) ||
		(subtitle.RequiresBurn && !remuxSubtitleOK && !hlsRemuxSubtitleOK) {
		return planVideoTranscodeV3(input, base, source, quality, hlsSubtitle, "")
	}

	// Profile 7 is normalized on the client against the original range-capable
	// source. A decoder profile/max-instance claim alone is not proof of native
	// dual-layer output, so the default Android route mirrors Silo Apple: P8.1
	// base-layer Dolby Vision first, then same-file HDR10.
	if source.DVProfile == 7 && quality.PreservesSource && videoOK && containerOK && audioOK && !subtitle.RequiresBurn {
		if clientDV81Eligible {
			plan := base
			plan.Delivery = DeliveryOriginalHTTPV3
			plan.Engine = EngineMedia3DirectV3
			plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: source.Container, MIMEType: MimeFromExtension(file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshSessionV3}
			plan.DecisionReason = "client_dv7_to_dv81"
			plan.EffectiveRecipe.DynamicRange = "dolby_vision"
			plan.Claims.Video = VideoClaimsV3{DolbyVision: true, DolbyVisionReason: "client_profile7_to_profile81"}
			plan.Transformations = append(plan.Transformations, TransformationV3{
				Name: ClientDV7ToDV81V3, Executor: "client", RecipeVersion: ClientDVTransformVersionV3,
				ValidatedClaims: []string{"profile7_rpu_converted_to_profile81", "hdr10_base_layer_preserved", "enhancement_layer_discarded"},
			})
			plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{
				Code:    "dolby_vision_enhancement_layer_discarded",
				Message: "Dolby Vision Profile 7 is played as Profile 8.1 base-layer Dolby Vision; enhancement-layer pixel data is discarded.",
			})
			finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID)
			if !planAttemptedV3(plan, input.Request.OutputRouteGeneration, input.AttemptedKeys) {
				return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: subtitle.SelectedIndex, SubtitleTransportTrackIndex: subtitle.TransportIndex, SubtitleCodec: subtitle.Codec}
			}
		}
		if clientHDR10Eligible {
			plan := base
			plan.Delivery = DeliveryOriginalHTTPV3
			plan.Engine = EngineMedia3DirectV3
			plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: source.Container, MIMEType: MimeFromExtension(file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshSessionV3}
			plan.DecisionReason = "client_dv7_to_hdr10"
			plan.EffectiveRecipe.DynamicRange = "hdr10"
			plan.Claims.Video = VideoClaimsV3{HDR10: true}
			plan.Transformations = append(plan.Transformations, TransformationV3{
				Name: ClientDV7ToHDR10V3, Executor: "client", RecipeVersion: ClientDVTransformVersionV3,
				ValidatedClaims: []string{"dolby_vision_metadata_removed", "hdr10_base_layer_preserved", "enhancement_layer_discarded"},
			})
			plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{
				Code:    "dolby_vision_removed",
				Message: "Dolby Vision Profile 7 is played from the same 4K file as its HDR10 base layer.",
			})
			finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID)
			if !planAttemptedV3(plan, input.Request.OutputRouteGeneration, input.AttemptedKeys) {
				return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: subtitle.SelectedIndex, SubtitleTransportTrackIndex: subtitle.TransportIndex, SubtitleCodec: subtitle.Codec}
			}
		}
	}

	if source.DVProfile != 7 && engineAvailableV3(input.Request, EngineMedia3DirectV3) && containerOK && videoOK && rangeOK && audioOK && quality.PreservesSource && !subtitle.RequiresBurn {
		plan := base
		plan.Delivery = DeliveryOriginalHTTPV3
		plan.Engine = EngineMedia3DirectV3
		plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: source.Container, MIMEType: MimeFromExtension(file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshSessionV3}
		plan.DecisionReason = "validated_original_playback"
		applyCopiedVideoQuirksV3(&plan, source, input.Request, high10Quirk)
		finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID)
		if !planAttemptedV3(plan, input.Request.OutputRouteGeneration, input.AttemptedKeys) {
			return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: subtitle.SelectedIndex, SubtitleTransportTrackIndex: subtitle.TransportIndex, SubtitleCodec: subtitle.Codec}
		}
	}

	// A progressive remux maps only the base-layer video stream, so dual-layer
	// Profile 7 can never ship as native Dolby Vision here regardless of the
	// client's decoder claims; the validated HDR10 strip is the only eligible
	// P7 remux recipe.
	remuxRangeOK := rangeOK && source.DVProfile != 7
	if videoOK && (remuxRangeOK || dvStripEligible) && (remuxSubtitleOK || hlsRemuxSubtitleOK) {
		plan := base
		plan.Delivery = DeliveryRemuxProgressiveV3
		plan.Engine = EngineMedia3ProgressiveRemuxV3
		plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: "mp4", MIMEType: "video/mp4", Headers: map[string]string{}, HeaderRefresh: HeaderRefreshSessionV3}
		plan.DecisionReason = "container_normalization"
		transcodeAudio := !audioOK
		if transcodeAudio {
			if input.Registry == nil || !input.Registry.Available("audio_to_aac") {
				return terminalPlannerResultV3("audio_conversion_unsupported", "The required validated AAC conversion toolchain is unavailable.", true)
			}
			plan.EffectiveRecipe.AudioCodec = "aac"
			plan.EffectiveRecipe.AudioChannels = intPointerV3(2)
			plan.EffectiveRecipe.AudioLayout = "stereo"
			plan.Claims.Audio = AudioClaimsV3{Codec: "aac", Reason: "server_audio_adaptation"}
			plan.Transformations = append(plan.Transformations, TransformationV3{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1", ValidatedClaims: []string{"media3_audio_decode"}})
			plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: "audio_converted", Message: "The selected audio track is converted to AAC stereo."})
			plan.DecisionReason = "audio_adaptation"
		}
		dvStrip := dvStripEligible && (source.DVProfile == 7 || !rangeOK)
		if dvStrip {
			plan.Transformations = append(plan.Transformations, TransformationV3{Name: "server_dv7_to_hdr10", Executor: "server", RecipeVersion: "1", ValidatedClaims: []string{"dolby_vision_metadata_removed", "hdr10_base_layer_preserved", "enhancement_layer_discarded"}})
			plan.EffectiveRecipe.DynamicRange = "hdr10"
			plan.Claims.Video = VideoClaimsV3{HDR10: true}
			plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: "dolby_vision_removed", Message: "Dolby Vision metadata is removed and the validated HDR10 base layer is preserved."})
		}
		if !dvStrip {
			applyCopiedVideoQuirksV3(&plan, source, input.Request, high10Quirk)
		}
		if remuxSubtitleOK {
			plan.Subtitle = remuxSubtitle.Decision
			plan.Claims.Subtitles = remuxSubtitle.Claims
			finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID)
			if engineAvailableV3(input.Request, EngineMedia3ProgressiveRemuxV3) && !planAttemptedV3(plan, input.Request.OutputRouteGeneration, input.AttemptedKeys) {
				return PlannerResultV3{Plan: &plan, PlayMethod: PlayRemux, TranscodeAudio: transcodeAudio, TargetAudioCodec: plan.EffectiveRecipe.AudioCodec, SubtitleTrackIndex: remuxSubtitle.SelectedIndex, SubtitleTransportTrackIndex: remuxSubtitle.TransportIndex, SubtitleCodec: remuxSubtitle.Codec}
			}
		}
		if engineAvailableV3(input.Request, EngineMedia3HLSV3) && hlsRemuxSubtitleOK {
			plan.AppliedQuirks = []AppliedQuirkV3{}
			plan.RuntimeCorrections = []string{}
			plan.Delivery = DeliveryRemuxHLSV3
			plan.Engine = EngineMedia3HLSV3
			plan.Stream = StreamV3{Protocol: StreamHLSV3, Container: "hls", MIMEType: "application/vnd.apple.mpegurl", Headers: map[string]string{}, HeaderRefresh: HeaderRefreshSessionV3}
			hlsTranscodeAudio := transcodeAudio
			if audioQuirk, ok := hlsEAC3AudioCorrectionV3(source, input.Request); ok && !hlsTranscodeAudio {
				if input.Registry == nil || !input.Registry.Available("audio_to_aac") {
					return terminalPlannerResultV3("audio_conversion_unsupported", "The device-specific HLS route requires the validated AAC conversion toolchain.", true)
				}
				hlsTranscodeAudio = true
				plan.EffectiveRecipe.AudioCodec = "aac"
				plan.EffectiveRecipe.AudioChannels = intPointerV3(2)
				plan.EffectiveRecipe.AudioLayout = "stereo"
				plan.Claims.Audio = AudioClaimsV3{Codec: "aac", Reason: "device_hls_audio_adaptation"}
				plan.Transformations = append(plan.Transformations, TransformationV3{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1", ValidatedClaims: []string{"media3_audio_decode"}})
				plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: "audio_converted", Message: "The selected audio track is converted to AAC stereo for this device's HLS route."})
				appendAppliedQuirkV3(&plan, *audioQuirk, "")
			}
			if !dvStrip {
				applyCopiedVideoQuirksV3(&plan, source, input.Request, high10Quirk)
			}
			if hlsTranscodeAudio {
				plan.DecisionReason = "hls_audio_adaptation"
			} else {
				plan.DecisionReason = "hls_packaging_required"
			}
			plan.Subtitle = hlsSubtitle.Decision
			plan.Claims.Subtitles = hlsSubtitle.Claims
			finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID)
			if !planAttemptedV3(plan, input.Request.OutputRouteGeneration, input.AttemptedKeys) {
				targetAudio := "copy"
				if hlsTranscodeAudio {
					targetAudio = "aac"
				}
				return PlannerResultV3{Plan: &plan, PlayMethod: PlayRemux, TranscodeAudio: hlsTranscodeAudio, TargetVideoCodec: "copy", TargetAudioCodec: targetAudio, TargetResolution: resolutionLabelV3(source.Height), TargetBitrateKbps: source.BitrateKbps, SubtitleTrackIndex: hlsSubtitle.SelectedIndex, SubtitleTransportTrackIndex: hlsSubtitle.TransportIndex, SubtitleCodec: hlsSubtitle.Codec}
			}
		}
	}
	if engineAvailableV3(input.Request, EngineMedia3HLSV3) {
		return planVideoTranscodeV3(input, base, source, quality, hlsSubtitle, "copy_routes_exhausted")
	}

	return terminalPlannerResultV3("adaptation_unavailable", "No validated playback route is available for this source and output route.", false)
}

// planVideoTranscodeV3 always executes on the HLS engine, so the caller must
// pass the subtitle policy resolved against EngineMedia3HLSV3.
func planVideoTranscodeV3(input PlannerInputV3, base PlanV3, source SourceDescriptorV3, quality QualityResultV3, subtitle SubtitlePolicyResultV3, reasonOverride string) PlannerResultV3 {
	if !engineAvailableV3(input.Request, EngineMedia3HLSV3) {
		return terminalPlannerResultV3("client_hls_unsupported", "The client cannot execute the required HLS adaptation route.", false)
	}
	if subtitle.Terminal != nil {
		return PlannerResultV3{Terminal: subtitle.Terminal, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
	}
	if !input.Settings.TranscodeEnabled {
		reason := "transcoding_disabled"
		if subtitle.RequiresBurn {
			reason = "subtitle_conversion_unsupported"
		}
		return terminalPlannerResultV3(reason, "The source requires video adaptation, but transcoding is unavailable.", false)
	}
	if is4KSourceV3(input.EffectiveFile, source) && !input.Settings.Allow4KTranscode {
		return terminalPlannerResultV3("no_alternate_version", "A lower-resolution source is required because 4K transcoding is disabled.", false)
	}
	if hdrTranscodeUnavailableV3(source) {
		return terminalPlannerResultV3("hdr_transcode_unsupported", "This HDR source requires video encoding, but no validated HDR-preserving or tone-map recipe is installed.", false)
	}
	if input.Registry == nil || !input.Registry.Available("video_to_h264") || !input.Registry.Available("audio_to_aac") {
		return terminalPlannerResultV3("conversion_tool_unavailable", "The required validated H.264/AAC conversion toolchain is unavailable.", true)
	}
	plan := base
	plan.Delivery = DeliveryTranscodeHLSV3
	plan.Engine = EngineMedia3HLSV3
	plan.Stream = StreamV3{Protocol: StreamHLSV3, Container: "hls", MIMEType: "application/vnd.apple.mpegurl", Headers: map[string]string{}, HeaderRefresh: HeaderRefreshSessionV3}
	plan.EffectiveRecipe.VideoCodec = "h264"
	plan.EffectiveRecipe.AudioCodec = "aac"
	plan.EffectiveRecipe.Width = intPointerV3(quality.Width)
	plan.EffectiveRecipe.Height = intPointerV3(quality.Height)
	plan.EffectiveRecipe.BitrateKbps = intPointerV3(quality.BitrateKbps)
	plan.EffectiveRecipe.AudioChannels = intPointerV3(2)
	plan.EffectiveRecipe.AudioLayout = "stereo"
	plan.Transformations = append(plan.Transformations,
		TransformationV3{Name: "video_to_h264", Executor: "server", RecipeVersion: "1", ValidatedClaims: []string{"media3_h264_decode"}},
		TransformationV3{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1", ValidatedClaims: []string{"media3_audio_decode"}},
	)
	plan.Claims.Audio = AudioClaimsV3{Codec: "aac", Passthrough: false, AtmosPreserved: false, Reason: "server_audio_adaptation"}
	plan.Subtitle = subtitle.Decision
	plan.Claims.Subtitles = subtitle.Claims
	plan.DecisionReason = quality.Reason
	if reasonOverride != "" {
		plan.DecisionReason = reasonOverride
	}
	if subtitle.RequiresBurn {
		plan.DecisionReason = "subtitle_burn_in_required"
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: "subtitle_burn_in", Message: "The selected subtitle is rendered into the video."})
	}
	plan.EffectiveRecipe.DynamicRange = "sdr"
	plan.Claims.Video = VideoClaimsV3{}
	finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID)
	if planAttemptedV3(plan, input.Request.OutputRouteGeneration, input.AttemptedKeys) {
		return terminalPlannerResultV3("adaptation_exhausted", "All compatible playback recipes have already failed for this output route.", false)
	}
	return PlannerResultV3{Plan: &plan, PlayMethod: PlayTranscode, TranscodeAudio: true, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: quality.Label, TargetBitrateKbps: quality.BitrateKbps, SubtitleTrackIndex: subtitle.SelectedIndex, SubtitleTransportTrackIndex: subtitle.TransportIndex, SubtitleBurnIn: subtitle.RequiresBurn, SubtitleCodec: subtitle.Codec}
}

func canStripDolbyVisionToHDR10V3(source SourceDescriptorV3, request StartRequestV3, registry *TransformationRegistryV3) bool {
	if source.DynamicRange != "dolby_vision" || !clientSupportsHDR10V3(request) || registry == nil || !registry.Available("server_dv7_to_hdr10") {
		return false
	}
	// Profile 7 always carries an HDR10-viewable base layer. Profile 8 is
	// safe only when the DOVI compatibility id explicitly identifies HDR10.
	return source.DVProfile == 7 || source.DVProfile == 8 && source.DVBLCompatID == 1
}

func canClientTransformDV7ToDV81V3(source SourceDescriptorV3, request StartRequestV3) bool {
	return source.DynamicRange == "dolby_vision" && source.DVProfile == 7 &&
		clientSupportsDVProfileV3(request, 8) &&
		clientTransformationAvailableV3(request, ClientDV7ToDV81V3, ClientDVTransformVersionV3)
}

func canClientTransformDV7ToHDR10V3(source SourceDescriptorV3, request StartRequestV3) bool {
	return source.DynamicRange == "dolby_vision" && source.DVProfile == 7 && clientSupportsHDR10V3(request) &&
		clientTransformationAvailableV3(request, ClientDV7ToHDR10V3, ClientDVTransformVersionV3)
}

func clientSupportsDVProfileV3(request StartRequestV3, profile int) bool {
	hdr := request.ClientPlaybackContext.Output.HDRDetails
	if hdr == nil {
		hdr = request.Capabilities.HDRDetails
	}
	return hdr != nil && containsIntV3(hdr.DolbyVisionProfiles, profile)
}

func clientTransformationAvailableV3(request StartRequestV3, name, version string) bool {
	if !HasFeatureV3(request.ClientFeatures, FeatureClientVideoTransforms) &&
		!HasFeatureV3(request.ClientPlaybackContext.Features, FeatureClientVideoTransforms) {
		return false
	}
	engine, ok := request.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)]
	if !ok || !engine.Enabled || !engine.SupportedOnDevice {
		return false
	}
	for _, transformation := range engine.Transformations {
		if transformation.Executor == "client" && transformation.Name == name && transformation.RecipeVersion == version {
			return true
		}
	}
	return false
}

func is4KSourceV3(file *models.MediaFile, source SourceDescriptorV3) bool {
	resolution := ""
	if file != nil {
		resolution = strings.ToLower(strings.TrimSpace(file.Resolution))
	}
	return resolution == "2160p" || resolution == "4k" || resolution == "uhd" || source.Width >= 3840 || source.Height >= 2160
}

type QualityResultV3 struct {
	Label             string
	Width             int
	Height            int
	BitrateKbps       int
	PreservesSource   bool
	RequiresTranscode bool
	// ExplicitRung marks a user-selected fixed rung, as opposed to an
	// automatic reduction from device limits, bandwidth evidence, or caps.
	ExplicitRung bool
	Reason       string
	Warnings     []DegradationWarningV3
}

// ResolveQualityPolicyV3 selects the delivery quality for a plan.
//
// bandwidth_cap_kbps is a hard delivery ceiling and is honored in every
// quality mode: "original" delivery is degraded when the source bitrate
// exceeds the cap, fixed rungs are lowered when their ladder bitrate exceeds
// it, and "auto" folds the cap into bandwidth-based rung selection. A metered
// connection with neither a cap nor a bandwidth estimate limits auto
// selection to the conservative 720p rung — the rung auto would pick for a
// mid-range bandwidth estimate — instead of assuming the link can sustain the
// original stream.
func ResolveQualityPolicyV3(request StartRequestV3, source SourceDescriptorV3) QualityResultV3 {
	quality, changed := NormalizeQualityV3(request.QualityPreference)
	var warnings []DegradationWarningV3
	if changed {
		warnings = append(warnings, DegradationWarningV3{Code: "quality_preference_normalized", Message: "Unknown quality preference was normalized to auto."})
	}
	capKbps := optionalValueV3(request.BandwidthCapKbps)
	capExceededBySource := capKbps > 0 && source.BitrateKbps > capKbps
	if quality == "original" && !capExceededBySource {
		result := originalQualityResultV3(source)
		result.Warnings = warnings
		return result
	}
	targetHeight := source.Height
	reason := "quality_auto_source"
	explicitRung := false
	capApplied := false
	switch {
	case quality == "original":
		// Only reached when the source bitrate exceeds the cap: the cap is a
		// hard ceiling and outranks the original preference.
		targetHeight = ladderHeightForBandwidthV3(int(float64(capKbps) * 0.8))
		capApplied = true
	case quality != "auto":
		targetHeight, _ = strconv.Atoi(strings.TrimSuffix(quality, "p"))
		reason = "quality_fixed_rung"
		explicitRung = true
	default:
		maxHeight := resolutionHeightV3(request.Capabilities.MaxResolution)
		if maxHeight > 0 && (targetHeight == 0 || maxHeight < targetHeight) {
			targetHeight = maxHeight
			reason = "quality_device_limit"
		}
		bandwidth := optionalValueV3(request.BandwidthEstimateKbps)
		if capKbps > 0 && (bandwidth == 0 || capKbps < bandwidth) {
			bandwidth = capKbps
		}
		if bandwidth > 0 {
			targetHeight = minPositiveV3(targetHeight, ladderHeightForBandwidthV3(int(float64(bandwidth)*0.8)))
			reason = "quality_bandwidth_limit"
		} else if request.Metered {
			if capped := minPositiveV3(targetHeight, 720); capped != targetHeight {
				targetHeight = capped
				reason = "quality_metered_limit"
			}
		}
	}
	if targetHeight <= 0 {
		targetHeight = 1080
	}
	if source.Height > 0 && targetHeight > source.Height {
		targetHeight = source.Height
	}
	// The cap also constrains the rung chosen above: a rung that would
	// preserve the source is forced down when the source bitrate exceeds the
	// cap, and a transcode rung whose ladder bitrate exceeds the cap drops to
	// the cap's rung.
	if capKbps > 0 && !capApplied {
		wouldPreserve := source.Height > 0 && targetHeight >= source.Height
		if (wouldPreserve && capExceededBySource) || (!wouldPreserve && ladderBitrateKbpsV3(targetHeight) > capKbps) {
			capApplied = true
			if capHeight := ladderHeightForBandwidthV3(int(float64(capKbps) * 0.8)); capHeight < targetHeight {
				targetHeight = capHeight
			}
		}
	}
	if capApplied {
		reason = "quality_bandwidth_cap"
		warnings = append(warnings, DegradationWarningV3{Code: "bandwidth_cap_applied", Message: "Delivery quality is limited by the configured bandwidth cap."})
	}
	if source.Height > 0 && targetHeight >= source.Height && !capApplied {
		return QualityResultV3{
			Label:           strconv.Itoa(source.Height) + "p",
			Width:           source.Width,
			Height:          source.Height,
			BitrateKbps:     source.BitrateKbps,
			PreservesSource: true,
			ExplicitRung:    explicitRung,
			Reason:          reason,
			Warnings:        warnings,
		}
	}
	label := resolutionLabelV3(targetHeight)
	effectiveHeight := resolutionHeightV3(label)
	if source.Height > 0 && effectiveHeight > source.Height {
		effectiveHeight = source.Height
		label = resolutionLabelV3(effectiveHeight)
	}
	width, bitrate := qualityDimensionsV3(effectiveHeight, source.Width, source.Height)
	if capKbps > 0 && bitrate > capKbps {
		// The ladder has no rung below 480p, so a cap under the lowest rung's
		// bitrate is honored by lowering the encode target directly: the cap
		// is a hard delivery ceiling, never advisory.
		bitrate = capKbps
	}
	result := QualityResultV3{Label: label, Width: width, Height: effectiveHeight, BitrateKbps: bitrate, PreservesSource: !capApplied && source.Height > 0 && effectiveHeight >= source.Height, ExplicitRung: explicitRung, Reason: reason, Warnings: warnings}
	result.RequiresTranscode = !result.PreservesSource
	return result
}

func originalQualityResultV3(source SourceDescriptorV3) QualityResultV3 {
	return QualityResultV3{Label: resolutionLabelV3(source.Height), Width: source.Width, Height: source.Height, BitrateKbps: source.BitrateKbps, PreservesSource: true, Reason: "quality_original"}
}

// hdrTranscodeUnavailableV3 mirrors planVideoTranscodeV3's terminal
// condition: no validated HDR-preserving or tone-map transcode recipe exists.
func hdrTranscodeUnavailableV3(source SourceDescriptorV3) bool {
	return source.DynamicRange != "" && source.DynamicRange != "sdr"
}

// videoTranscodeExecutableV3 mirrors planVideoTranscodeV3's terminal
// preconditions: it reports whether a validated video transcode of this
// source could actually run for this client and configuration.
func videoTranscodeExecutableV3(input PlannerInputV3, source SourceDescriptorV3) bool {
	if !engineAvailableV3(input.Request, EngineMedia3HLSV3) || !input.Settings.TranscodeEnabled {
		return false
	}
	if is4KSourceV3(input.EffectiveFile, source) && !input.Settings.Allow4KTranscode {
		return false
	}
	if hdrTranscodeUnavailableV3(source) {
		return false
	}
	return input.Registry != nil && input.Registry.Available("video_to_h264") && input.Registry.Available("audio_to_aac")
}

func recipeFromSourceV3(source SourceDescriptorV3) EffectiveRecipeV3 {
	return EffectiveRecipeV3{VideoCodec: source.VideoCodec, AudioCodec: source.AudioCodec, Width: intPointerV3(source.Width), Height: intPointerV3(source.Height), FrameRate: floatPointerV3(source.FrameRate), BitrateKbps: intPointerV3(source.BitrateKbps), DynamicRange: source.DynamicRange, AudioChannels: intPointerV3(source.AudioChannels), AudioLayout: source.AudioLayout}
}

func selectedTracksForPlanV3(file *models.MediaFile, audioIndex int, subtitle SubtitlePolicyResultV3) SelectedTracksV3 {
	selected := SelectedTracksV3{}
	if file != nil && audioIndex >= 0 && audioIndex < len(file.AudioTracks) {
		index := audioIndex
		selected.Audio = &TrackIdentityV3{ID: TrackIDV3(file.ID, "audio", audioIndex), Index: &index}
	}
	if file != nil && subtitle.SelectedIndex >= 0 {
		index := subtitle.SelectedIndex
		selected.Subtitle = &TrackIdentityV3{ID: TrackIDV3(file.ID, "subtitle", index), Index: &index}
	}
	return selected
}

func finalizePlanIdentityV3(plan *PlanV3, attemptID string) {
	plan.PlanID = DeterministicPlanIDV3(attemptID, plan.RequestedMediaFileID, plan.EffectiveMediaFileID, *plan)
}

// planAttemptedV3 compares FNV-hex attempt keys exactly after trimming
// whitespace; the keys are case-sensitive hashes, not free-form labels.
func planAttemptedV3(plan PlanV3, generation int64, attempted []string) bool {
	wanted := PlanAttemptKeyV3(plan, generation, nil)
	for _, key := range attempted {
		if strings.TrimSpace(key) == wanted {
			return true
		}
	}
	return false
}

func terminalPlannerResultV3(reason, message string, retryable bool) PlannerResultV3 {
	return PlannerResultV3{Terminal: &TerminalV3{Reason: reason, Message: message, Retryable: retryable}, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
}
func subtitlePolicyNameV3(f SubtitleFidelityV3) string {
	if f == SubtitleFidelityPreserveV3 {
		return "require_authored_fidelity"
	}
	return "allow_simplified_rendering"
}
func floatOrZeroV3(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
func intPointerV3(v int) *int {
	if v <= 0 {
		return nil
	}
	value := v
	return &value
}
func floatPointerV3(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	value := v
	return &value
}
func optionalValueV3(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
func resolutionHeightV3(v string) int {
	value, _ := strconv.Atoi(strings.TrimSuffix(strings.ToLower(v), "p"))
	if strings.EqualFold(v, "4k") {
		return 2160
	}
	return value
}
func resolutionLabelV3(h int) string {
	switch {
	case h >= 2160:
		return "2160p"
	case h >= 1080:
		return "1080p"
	case h >= 720:
		return "720p"
	default:
		return "480p"
	}
}
func ladderHeightForBandwidthV3(kbps int) int {
	switch {
	case kbps >= 20_000:
		return 2160
	case kbps >= 8_000:
		return 1080
	case kbps >= 4_000:
		return 720
	default:
		return 480
	}
}
func minPositiveV3(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

// ladderBitrateKbpsV3 matches the established web ladder's standard shared
// rungs; 2160p is the v3-only extension until the web menu exposes a 4K
// transcode tier.
func ladderBitrateKbpsV3(height int) int {
	bitrates := map[int]int{480: 1_500, 720: 2_000, 1080: 6_000, 2160: 20_000}
	return bitrates[resolutionHeightV3(resolutionLabelV3(height))]
}

func qualityDimensionsV3(height, sourceWidth, sourceHeight int) (int, int) {
	rung := resolutionHeightV3(resolutionLabelV3(height))
	width := 0
	if sourceWidth > 0 && sourceHeight > 0 {
		width = sourceWidth * rung / sourceHeight
		width -= width % 2
	}
	if width == 0 {
		width, _ = dimensionsFromResolutionV3(resolutionLabelV3(rung))
	}
	return width, ladderBitrateKbpsV3(rung)
}

func SortedTransformationNamesV3(values []TransformationV3) []string {
	result := make([]string, 0, len(values))
	for _, v := range values {
		result = append(result, v.Name)
	}
	sort.Strings(result)
	return result
}

func engineAvailableV3(request StartRequestV3, engine EngineV3) bool {
	capability, ok := request.ClientPlaybackContext.Engines[string(engine)]
	if !ok {
		return false
	}
	return capability.Enabled && capability.SupportedOnDevice
}
func ExplainPlannerResultV3(result PlannerResultV3) string {
	if result.Plan != nil {
		return fmt.Sprintf("%s:%s", result.Plan.Delivery, result.Plan.DecisionReason)
	}
	if result.Terminal != nil {
		return "terminal:" + result.Terminal.Reason
	}
	return "invalid"
}
