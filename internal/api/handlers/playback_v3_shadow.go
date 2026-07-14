package handlers

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const playbackV3ShadowSetting = "playback.protocol_v3_shadow_enabled"

func (h *PlaybackHandler) protocolV3ShadowEnabled(ctx context.Context) bool {
	return h.settingFlagCachedV3(ctx, playbackV3ShadowSetting)
}

// shadowLegacyPlaybackV3 compares the production legacy route with a v3 plan
// without advertising or executing that plan. Legacy capabilities lack the
// detailed v3 fields, so exact source facts are used only as an explicitly
// labelled validation inference; this is never a production compatibility
// claim and cannot enable v3 playback.
func (h *PlaybackHandler) shadowLegacyPlaybackV3(ctx context.Context, req startPlaybackRequest, requestedFile, effectiveFile *models.MediaFile, audioIndex int, productionMethod playback.PlayMethod, productionTranscodeAudio bool, sessionID string) {
	// The shadow planner runs on a bare goroutine off the production start
	// path; an escaped panic here would take down the whole process for a
	// telemetry-only comparison, so it must fail closed instead.
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(ctx, "playback v3 shadow planner panicked",
				"component", "playback", "playback_session_id", sessionID,
				"panic", recovered, "stack", string(debug.Stack()))
		}
	}()
	if !h.protocolV3ShadowEnabled(ctx) || requestedFile == nil || effectiveFile == nil {
		return
	}
	request := legacyShadowRequestV3(req, effectiveFile, audioIndex, sessionID)
	result := playback.PlanPlaybackV3(playback.PlannerInputV3{
		Request:         request,
		RequestedFile:   requestedFile,
		EffectiveFile:   effectiveFile,
		AudioTrackIndex: audioIndex,
		Settings:        h.plannerSettingsV3(ctx),
		Registry:        h.transformationRegistryV3(ctx),
		HLSRegistry:     h.lazyHLSPlanningRegistryV3(ctx),
		Now:             time.Now(),
	})
	attrs := []any{
		"component", "playback",
		"playback_session_id", sessionID,
		"requested_file_id", requestedFile.ID,
		"effective_file_id", effectiveFile.ID,
		"production_method", productionMethod,
		"production_transcode_audio", productionTranscodeAudio,
		"capability_basis", "legacy_source_exact_inference",
	}
	if result.Plan != nil {
		attrs = append(attrs,
			"shadow_plan_id", result.Plan.PlanID,
			"shadow_delivery", result.Plan.Delivery,
			"shadow_method", result.PlayMethod,
			"shadow_transcode_audio", result.TranscodeAudio,
			"shadow_reason", result.Plan.DecisionReason,
			"route_match", result.PlayMethod == productionMethod && result.TranscodeAudio == productionTranscodeAudio,
		)
	} else if result.Terminal != nil {
		attrs = append(attrs, "shadow_terminal", result.Terminal.Reason, "route_match", false)
	}
	slog.InfoContext(ctx, "playback v3 shadow decision", attrs...)
}

func legacyShadowRequestV3(req startPlaybackRequest, file *models.MediaFile, audioIndex int, sessionID string) playback.StartRequestV3 {
	source := playback.SourceDescriptorFromFileV3(file, audioIndex)
	hdr := legacyShadowHDRV3(req)
	features := []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureDetailedDecodeV3}
	capabilities := playback.ClientCodecCapabilitiesV3{
		CodecsVideo:   append([]string(nil), req.CodecsVideo...),
		CodecsAudio:   append([]string(nil), req.CodecsAudio...),
		Containers:    append([]string(nil), req.Containers...),
		MaxResolution: req.MaxResolution,
		HDR:           req.HDR,
		HDRDetails:    hdr,
	}
	if containsStringFoldV3(req.CodecsVideo, source.VideoCodec) {
		capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{{
			Codec: source.VideoCodec, Profiles: []string{source.VideoProfile}, Levels: []int{source.VideoLevel}, BitDepths: []int{source.BitDepth},
			MaxWidth: source.Width, MaxHeight: source.Height, MaxFrameRate: source.FrameRate, MaxBitrateKbps: source.BitrateKbps, Hardware: true,
		}}
	}
	var passthrough *playback.AudioPassthroughV3
	if req.AudioPassthrough != nil {
		passthrough = &playback.AudioPassthroughV3{PassthroughCodecs: append([]string(nil), req.AudioPassthrough.PassthroughCodecs...), SpatializerEnabled: req.AudioPassthrough.SpatializerEnabled, MaxChannels: req.AudioPassthrough.MaxChannels}
		if containsStringFoldV3(req.AudioPassthrough.PassthroughCodecs, source.AudioCodec) && source.AudioChannels > 0 && source.AudioLayout != "" && source.AudioChannels <= req.AudioPassthrough.MaxChannels {
			passthrough.Entries = []playback.AudioPassthroughEntryV3{{Codec: source.AudioCodec, ChannelCounts: []int{source.AudioChannels}, Layouts: []string{source.AudioLayout}}}
			features = append(features, playback.FeatureLayoutPassthrough)
		}
		capabilities.AudioPassthrough = passthrough
	}
	engines := map[string]playback.EngineCapabilityV3{
		string(playback.EngineMedia3DirectV3):           {Enabled: true, SupportedOnDevice: true, Subtitles: playback.EngineSubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true, EmbeddedBitmap: true, SidecarBitmap: true}},
		string(playback.EngineMedia3ProgressiveRemuxV3): {Enabled: true, SupportedOnDevice: true},
		string(playback.EngineMedia3HLSV3):              {Enabled: true, SupportedOnDevice: true},
	}
	return playback.StartRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: features, FileID: file.ID, ProfileID: req.ProfileID,
		PlaybackAttemptID: "shadow-" + sessionID, QualityPreference: "original", SubtitleFidelityPreference: playback.SubtitleFidelityCompatibleV3,
		StartPosition: req.StartPosition, AudioTrackIndex: &audioIndex, Capabilities: capabilities,
		ClientPlaybackContext: playback.ClientPlaybackContextV3{ProtocolVersion: playback.ProtocolV3, Features: features, Platform: "legacy-shadow", Output: playback.OutputContextV3{HDRDetails: hdr, AudioPassthrough: passthrough}, Engines: engines},
	}
}

func legacyShadowHDRV3(req startPlaybackRequest) *playback.HDRCapabilitiesV3 {
	if req.HdrDetails != nil {
		return &playback.HDRCapabilitiesV3{HDR10: req.HdrDetails.HDR10, HDR10Plus: req.HdrDetails.HDR10Plus, HLG: req.HdrDetails.HLG, DolbyVisionProfiles: append([]int(nil), req.HdrDetails.DolbyVisionProfiles...)}
	}
	if req.HDR {
		return &playback.HDRCapabilitiesV3{HDR10: true}
	}
	return nil
}
