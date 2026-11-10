package playback

import (
	"fmt"
	"strings"
)

func (p *candidatePlannerV3) originalCandidate(c candidateV3) (PlannerResultV3, bool) {
	f, request := &p.video, p.input.Request
	plan := cloneRemuxPlanCandidateV3(f.base)
	plan.Delivery = DeliveryOriginalHTTPV3
	plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: p.source.Container, MIMEType: MimeFromExtension(p.file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
	plan.DecisionReason = decisionReasonValidatedOriginalV3
	switch c.video {
	case candidateClientDV81V3:
		plan.DecisionReason = ClientDV7ToDV81V3 // named after its transformation
		plan.EffectiveRecipe.DynamicRange = DynamicRangeDolbyVisionV3
		plan.Claims.Video = VideoClaimsV3{DolbyVision: true, DolbyVisionReason: "client_profile7_to_profile81"}
		plan.Transformations = append(plan.Transformations, TransformationV3{Name: ClientDV7ToDV81V3, Executor: ExecutorClientV3, RecipeVersion: ClientDVTransformVersionV3, ValidatedClaims: DV7ToDV81ClaimsV3()})
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationDVELDiscardedV3, Message: "Dolby Vision Profile 7 is played as Profile 8.1 base-layer Dolby Vision; enhancement-layer pixel data is discarded."})
	case candidateClientHDR10V3:
		plan.DecisionReason = ClientDV7ToHDR10V3
		plan.EffectiveRecipe.DynamicRange = DynamicRangeHDR10V3
		plan.Claims.Video = VideoClaimsV3{HDR10: true}
		plan.Transformations = append(plan.Transformations, TransformationV3{Name: ClientDV7ToHDR10V3, Executor: ExecutorClientV3, RecipeVersion: ClientDVTransformVersionV3, ValidatedClaims: DV7ToHDR10ClaimsV3()})
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationDVRemovedV3, Message: "Dolby Vision Profile 7 is played from the same 4K file as its HDR10 base layer."})
	case candidateClientManagedV3:
		plan.DecisionReason = decisionReasonClientManagedDynamicRangeV3
	case candidateDV8BaseLayerV3:
		// The same bytes through an ordinary HEVC decoder: the recipe names the
		// base range that reaches the output, and never claims Dolby Vision.
		baseRange := f.dv8BaseRange
		plan.DecisionReason = decisionReasonClientDV8BaseLayerV3
		plan.EffectiveRecipe.DynamicRange = baseRange
		plan.Claims.Video = VideoClaimsV3{HDR10: baseRange == DynamicRangeHDR10V3, HLG: baseRange == DynamicRangeHLGV3, DolbyVisionReason: dvBaseLayerReasonV3}
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{
			Code:    degradationDVBaseLayerOnlyV3,
			Message: "This output does not carry Dolby Vision; the file plays unchanged through an HEVC decoder as its " + strings.ToUpper(baseRange) + " base layer and the Dolby Vision metadata is not presented.",
		})
	}
	if p.source.DVProfile != 7 {
		applyCopiedVideoQuirksV3(&plan, p.source, request, f.high10)
	}
	p.finalizeV3(&plan)
	sub := f.subtitle
	return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: sub.SelectedIndex, SubtitleTransportTrackIndex: sub.TransportIndex, SubtitleCodec: sub.Codec, DownloadedSubtitleID: sub.DownloadedSubtitleID},
		deliverySupportsPlanV3(request, DeliveryClassOriginalHTTPV3, plan)
}

// remuxBase is the copy-remux plan for one video recipe, before a delivery.
func (p *candidatePlannerV3) remuxBase(video candidateVideoV3) PlanV3 {
	plan := cloneRemuxPlanCandidateV3(p.video.base)
	switch video {
	case candidateServerDV81V3:
		plan.Transformations = append(plan.Transformations, TransformationV3{Name: TransformationServerDV7DV81V3, Executor: ExecutorServerV3, RecipeVersion: TransformationServerDV7DV81RecipeVersionV3, ValidatedClaims: DV7ToDV81ClaimsV3()})
		plan.EffectiveRecipe.DynamicRange = DynamicRangeDolbyVisionV3
		plan.Claims.Video = VideoClaimsV3{DolbyVision: true, DolbyVisionReason: "server_profile7_to_profile81"}
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationDVELDiscardedV3, Message: "Dolby Vision Profile 7 is converted to Profile 8.1 base-layer Dolby Vision; enhancement-layer pixel data is discarded."})
	case candidateServerStripV3:
		plan.Transformations = append(plan.Transformations, TransformationV3{Name: TransformationServerDV7HDR10V3, Executor: ExecutorServerV3, RecipeVersion: TransformationServerDV7HDR10RecipeVersionV3, ValidatedClaims: DV7ToHDR10ClaimsV3()})
		plan.EffectiveRecipe.DynamicRange = DynamicRangeHDR10V3
		plan.Claims.Video = VideoClaimsV3{HDR10: true}
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationDVRemovedV3, Message: "Dolby Vision metadata is removed and the validated HDR10 base layer is preserved."})
	}
	return plan
}

func convertRemuxAudioV3(plan *PlanV3, channels int, reason, message string) {
	plan.EffectiveRecipe.AudioCodec = audioCodecAACV3
	plan.EffectiveRecipe.AudioChannels = intPointerV3(channels)
	plan.EffectiveRecipe.AudioLayout = audioLayoutForChannelsV3(channels)
	plan.Claims.Audio = AudioClaimsV3{Codec: audioCodecAACV3, Reason: reason}
	plan.Transformations = append(plan.Transformations, TransformationV3{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{ClaimAudioDecodeV3}})
	plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationAudioConvertedV3, Message: message})
}

func (p *candidatePlannerV3) progressiveCandidate(c candidateV3) (PlannerResultV3, bool) {
	f, a, request, source := &p.video, &p.video.remuxAudio, p.input.Request, p.source
	if !f.remuxSubtitleOK || c.video == candidateServerStripV3 && !f.stripProgressive {
		return PlannerResultV3{}, false
	}
	channels := 0
	if a.progressiveConverts {
		if !p.progressiveAACOK() {
			return PlannerResultV3{}, false
		}
		channels = aacOutputChannelsV3(request, DeliveryClassProgressiveV3, source.AudioChannels, a.progressiveChannelLimited)
		if !audioRemuxFitsServerCapV3(p.input, p.file, channels) {
			return PlannerResultV3{}, false
		}
	}
	plan := p.remuxBase(c.video)
	plan.Delivery = DeliveryRemuxProgressiveV3
	plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: containerMP4V3, MIMEType: mimeVideoMP4V3, Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
	plan.DecisionReason = decisionReasonContainerNormalizationV3
	if a.progressiveConverts {
		convertRemuxAudioV3(&plan, channels, serverAudioAdaptationReasonV3, fmt.Sprintf("The selected audio track is converted to AAC %s.", audioLayoutForChannelsV3(channels)))
		plan.DecisionReason = decisionReasonAudioAdaptationV3
	}
	if a.firefoxAAC != nil {
		appendAppliedQuirkV3(&plan, *a.firefoxAAC, "")
	}
	if c.video == candidateCopyV3 {
		applyCopiedVideoQuirksV3(&plan, source, request, f.high10)
	}
	applySubtitleDecisionV3(&plan, f.remuxSubtitle.Decision)
	plan.Claims.Subtitles = f.remuxSubtitle.Claims
	p.finalizeV3(&plan)
	sub := f.remuxSubtitle
	return PlannerResultV3{Plan: &plan, PlayMethod: PlayRemux, TranscodeAudio: a.progressiveConverts, RemuxResumeLeadingPictureDrop: firefoxMacOSHEVCResumeLeadingPictureDropV3(source, request), TargetAudioCodec: plan.EffectiveRecipe.AudioCodec, SourceAudioChannels: stereoDownmixSourceChannelsV3(source.AudioChannels, channels, a.progressiveConverts), TargetAudioChannels: channels, SubtitleTrackIndex: sub.SelectedIndex, SubtitleTransportTrackIndex: sub.TransportIndex, SubtitleCodec: sub.Codec, DownloadedSubtitleID: sub.DownloadedSubtitleID},
		deliverySupportsPlanV3(request, DeliveryClassProgressiveV3, plan)
}

func (p *candidatePlannerV3) hlsRemuxCandidate(c candidateV3) (PlannerResultV3, bool) {
	f, a, request, source := &p.video, &p.video.remuxAudio, p.input.Request, p.source
	if !deliveryAvailableV3(request, DeliveryClassHLSV3) || !f.hlsSubtitleOK || c.video == candidateServerStripV3 && !f.stripHLS ||
		a.hlsConverts && !audioRemuxFitsServerCapV3(p.input, p.file, a.hlsAACChannels) {
		return PlannerResultV3{}, false
	}
	if a.hlsConverts && !p.input.hlsRemuxRegistry().Available(TransformationAudioToAACV3) {
		switch {
		case a.hlsQuirkConverts:
			p.hlsAACMissing = "The device-specific HLS route requires the validated AAC conversion toolchain."
		case a.hlsCodecConverts:
			p.hlsAACMissing = "The HLS route requires the validated AAC conversion toolchain."
		}
		return PlannerResultV3{}, false
	}
	plan := p.remuxBase(c.video)
	plan.Delivery = DeliveryRemuxHLSV3
	plan.Stream = StreamV3{Protocol: StreamHLSV3, Container: containerHLSV3, MIMEType: mimeHLSV3, Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
	if deliverySupportsFeatureV3(request, DeliveryClassHLSV3, FeatureHLSCopyMPEGTSV3) && HLSCopyFitsMPEGTS(source.VideoCodec, "") {
		plan.EffectiveRecipe.SegmentContainer = SegmentContainerMPEGTSV3
	} else {
		plan.EffectiveRecipe.VideoSampleEntry = hlsVideoSampleEntryV3(source, request, c.video != candidateCopyV3)
	}
	channels, transcodeAudio := 0, a.hlsTranscode
	if a.hlsTranscode && !a.hlsQuirkConverts {
		channels = a.hlsAACChannels
		message := "The selected audio track is converted to AAC for HLS delivery."
		if !a.hlsCodecConverts {
			message = fmt.Sprintf("The selected audio track is converted to AAC %s to fit this output's channel limit.", audioLayoutForChannelsV3(channels))
		}
		convertRemuxAudioV3(&plan, channels, hlsAudioAdaptationReasonV3, message)
	}
	if a.firefoxAAC != nil {
		appendAppliedQuirkV3(&plan, *a.firefoxAAC, "")
	}
	if a.hlsQuirkConverts {
		channels, transcodeAudio = a.hlsAACChannels, true
		convertRemuxAudioV3(&plan, channels, "device_hls_audio_adaptation", fmt.Sprintf("The selected audio track is converted to AAC %s for this device's HLS route.", audioLayoutForChannelsV3(channels)))
		appendAppliedQuirkV3(&plan, *a.hlsQuirk, "")
	}
	if c.video == candidateCopyV3 {
		applyCopiedVideoQuirksV3(&plan, source, request, f.high10)
	}
	plan.DecisionReason = "hls_packaging_required"
	targetAudio := codecCopyV3
	if transcodeAudio {
		plan.DecisionReason, targetAudio = hlsAudioAdaptationReasonV3, audioCodecAACV3
	}
	sub := f.hlsSubtitle
	applySubtitleDecisionV3(&plan, sub.Decision)
	plan.Claims.Subtitles = sub.Claims
	p.finalizeV3(&plan)
	renditions, ok := p.applyRenditionsV3(&plan, c.subtitle, true)
	if !ok || !deliverySupportsPlanV3(request, DeliveryClassHLSV3, plan) {
		return PlannerResultV3{}, false
	}
	return PlannerResultV3{SubtitleRendition: renditions.carried, SubtitleRenditionTracks: renditions.tracks, SubtitleRenditionSource: renditions.source, Plan: &plan, PlayMethod: PlayRemux, TranscodeAudio: transcodeAudio, TargetVideoCodec: codecCopyV3, TargetAudioCodec: targetAudio, SourceAudioChannels: stereoDownmixSourceChannelsV3(source.AudioChannels, channels, transcodeAudio), TargetAudioChannels: channels, TargetResolution: resolutionLabelV3(source.Height), TargetBitrateKbps: source.BitrateKbps, SubtitleTrackIndex: sub.SelectedIndex, SubtitleTransportTrackIndex: sub.TransportIndex, SubtitleCodec: sub.Codec, DownloadedSubtitleID: sub.DownloadedSubtitleID}, true
}

// applyRenditionsV3 gives an HLS plan the subtitle presentation the
// candidate names, or reports that it cannot carry it.
func (p *candidatePlannerV3) applyRenditionsV3(plan *PlanV3, presentation candidateSubtitleV3, remux bool) (hlsRenditionsV3, bool) {
	subtitle := p.video.hlsSubtitle
	switch presentation {
	case candidateOnDemandRenditionsV3:
		renditions, source := hlsOnDemandSubtitleRenditionsV3(p.input, *plan, subtitle, remux)
		if len(renditions) == 0 {
			return hlsRenditionsV3{}, false
		}
		tracks := applyHLSOnDemandSubtitleRenditionsV3(plan, renditions)
		p.finalizeV3(plan)
		return hlsRenditionsV3{carried: true, tracks: tracks, source: source}, true
	case candidatePipelineRenditionV3:
		executable := p.input.SubtitleRenditionExecutable
		if remux {
			if !HasFeatureV3(p.input.Request.ClientFeatures, FeatureHLSRemuxSubtitleRenditionsV3) {
				return hlsRenditionsV3{}, false
			}
			executable = p.input.RemuxSubtitleRenditionExecutable
		}
		if !hlsSubtitleRenditionEligibleV3(p.input, subtitle, executable) {
			return hlsRenditionsV3{}, false
		}
		applyHLSSubtitleRenditionV3(plan)
		p.finalizeV3(plan)
		return hlsRenditionsV3{carried: true}, true
	default:
		return hlsRenditionsV3{}, true
	}
}

// transcodeSetupV3 is what every encode candidate shares: the gates an encode
// must pass and its sizing, settled once.
type transcodeSetupV3 struct {
	tone          resolvedToneMapRecipeV3
	h264, hevc    QualityResultV3
	useHEVC       bool
	audioChannels int
	audioKbps     int
}

func (p *candidatePlannerV3) transcodeCandidate(c candidateV3) (PlannerResultV3, bool, *PlannerResultV3) {
	if p.transcode == nil && p.transcodeStop == nil {
		p.transcode, p.transcodeStop = p.transcodeGates()
	}
	setup, f, request := p.transcode, &p.video, p.input.Request
	if p.transcodeStop != nil {
		return PlannerResultV3{}, false, p.transcodeStop
	}
	hevc := c.video == candidateEncodeHEVCV3
	if hevc && !setup.useHEVC {
		return PlannerResultV3{}, false, nil
	}
	quality, codec := setup.h264, transcodeCodecH264
	if hevc {
		quality, codec = setup.hevc, transcodeCodecHEVC
	}
	plan := p.encodePlan(setup, codec, quality)
	if !deliverySupportsPlanV3(request, DeliveryClassHLSV3, plan) {
		message := "The HLS delivery cannot decode the planned transcode recipe."
		if setup.useHEVC && !hevc {
			message = "The HLS delivery cannot decode the H.264 fallback recipe."
		}
		return PlannerResultV3{}, false, refusalV3(terminalPlannerResultV3("adaptation_unavailable", message, false))
	}
	p.finalizeV3(&plan)
	// Special case: a failed HEVC encode moves to H.264, whichever subtitle
	// presentation the failed attempt used.
	if hevc && planAttemptedV3(plan, request.ClientPlaybackContext.Output.OutputContextID, p.input.AttemptedKeys) {
		return PlannerResultV3{}, false, nil
	}
	renditions, ok := p.applyRenditionsV3(&plan, c.subtitle, false)
	if !ok {
		return PlannerResultV3{}, false, nil
	}
	sub, tone := f.hlsSubtitle, setup.tone
	result := PlannerResultV3{SubtitleRendition: renditions.carried, SubtitleRenditionTracks: renditions.tracks, SubtitleRenditionSource: renditions.source, Plan: &plan, PlayMethod: PlayTranscode, TranscodeAudio: true, TargetVideoCodec: codec, TargetAudioCodec: audioCodecAACV3, SourceAudioChannels: stereoDownmixSourceChannelsV3(p.source.AudioChannels, setup.audioChannels, true), TargetAudioChannels: setup.audioChannels, TargetAudioBitrateKbps: setup.audioKbps, TargetResolution: quality.Label, TargetBitrateKbps: quality.BitrateKbps, SubtitleTrackIndex: sub.SelectedIndex, SubtitleTransportTrackIndex: sub.TransportIndex, SubtitleBurnIn: sub.RequiresBurn, SubtitleCodec: sub.Codec, DownloadedSubtitleID: sub.DownloadedSubtitleID}
	if tone.ok {
		result.ToneMapPolicy, result.ToneMapMode, result.ToneMapSourceKind = tone.policy, tone.mode, tone.resolution.Kind
		result.ToneMapRecipeVersion, result.ToneMapPreflightRequired, result.ToneMapSourceRevision = TransformationHDRToSDRToneMapRecipeVersionV3, tone.resolution.PreflightRequired, tone.revision
	}
	return result, true, nil
}

func (p *candidatePlannerV3) encodePlan(setup *transcodeSetupV3, codec string, quality QualityResultV3) PlanV3 {
	f := &p.video
	plan := cloneRemuxPlanCandidateV3(f.base)
	plan.Delivery = DeliveryTranscodeHLSV3
	plan.Stream = StreamV3{Protocol: StreamHLSV3, Container: containerHLSV3, MIMEType: mimeHLSV3, Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
	plan.EffectiveRecipe.VideoCodec = codec
	if codec == transcodeCodecHEVC {
		plan.EffectiveRecipe.VideoSampleEntry = VideoSampleEntryHVC1
	}
	plan.EffectiveRecipe.AudioCodec = audioCodecAACV3
	plan.EffectiveRecipe.Width = intPointerV3(quality.Width)
	plan.EffectiveRecipe.Height = intPointerV3(quality.Height)
	plan.EffectiveRecipe.BitrateKbps = intPointerV3(quality.BitrateKbps)
	plan.EffectiveRecipe.AudioChannels = intPointerV3(setup.audioChannels)
	plan.EffectiveRecipe.AudioLayout = audioLayoutForChannelsV3(setup.audioChannels)
	plan.Transformations = append(plan.Transformations, videoTransformationForTargetV3(codec),
		TransformationV3{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{ClaimAudioDecodeV3}})
	if setup.tone.ok {
		plan.Transformations = append(plan.Transformations, TransformationV3{Name: TransformationHDRToSDRToneMapV3, Executor: ExecutorServerV3, RecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, ValidatedClaims: []string{ClaimHDRMetadataRemovedV3, ClaimSDRBT709OutputV3}})
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: DegradationWarningHDRToneMappedV3, Message: "HDR video is tone-mapped to SDR for this playback route."})
	}
	plan.Claims.Audio = AudioClaimsV3{Codec: audioCodecAACV3, Reason: serverAudioAdaptationReasonV3}
	applySubtitleDecisionV3(&plan, f.hlsSubtitle.Decision)
	plan.Claims.Subtitles = f.hlsSubtitle.Claims
	plan.DecisionReason = quality.Reason
	if f.transcodeReason != "" {
		plan.DecisionReason = f.transcodeReason
	}
	if f.hlsSubtitle.RequiresBurn {
		plan.DecisionReason = "subtitle_burn_in_required"
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: "subtitle_burn_in", Message: "The selected subtitle is rendered into the video."})
	}
	plan.EffectiveRecipe.DynamicRange = DynamicRangeSDRV3
	plan.Claims.Video = VideoClaimsV3{}
	return plan
}

// transcodeGates applies, in order, the refusals any encode of this source
// meets, then sizes it.
func (p *candidatePlannerV3) transcodeGates() (*transcodeSetupV3, *PlannerResultV3) {
	in, source, f := p.input, p.source, &p.video
	forced := f.subtitleForced
	switch {
	case !deliveryAvailableV3(in.Request, DeliveryClassHLSV3):
		return nil, refusalV3(terminalPlannerResultV3("client_hls_unsupported", "The client cannot execute the required HLS adaptation route.", false))
	case f.hlsSubtitle.Terminal != nil:
		return nil, &PlannerResultV3{Terminal: f.hlsSubtitle.Terminal, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
	case !in.Settings.TranscodeEnabled && forced:
		return nil, refusalV3(terminalPlannerResultV3(TerminalSubtitleConversionUnsupportedV3, "The selected subtitle must be burned into the video, but transcoding is unavailable.", false))
	case !in.Settings.TranscodeEnabled:
		return nil, refusalV3(terminalPlannerResultV3("transcoding_disabled", "The source requires video adaptation, but transcoding is unavailable.", false))
	}
	setup := &transcodeSetupV3{}
	var registry *TransformationRegistryV3
	if source.DynamicRange != "" && source.DynamicRange != DynamicRangeSDRV3 {
		setup.tone = resolveToneMapRecipeV3(in, source, nil)
		registry = setup.tone.hlsRegistry
		if !setup.tone.ok && forced {
			return nil, refusalV3(settingsRefusalV3(TerminalSubtitleConversionUnsupportedV3, "The selected subtitle must be burned into the video, but this HDR source cannot be re-encoded."))
		}
		if !setup.tone.ok {
			return nil, refusalV3(settingsRefusalV3(TerminalHDRTranscodeUnsupportedV3, "This HDR source requires video encoding, but no validated HDR-preserving or tone-map recipe is installed."))
		}
	}
	if is4KSourceV3(in.EffectiveFile, source) && !in.Settings.Allow4KTranscode {
		if forced {
			return nil, refusalV3(settingsRefusalV3(TerminalSubtitleConversionUnsupportedV3, "The selected subtitle must be burned into the video, but 4K transcoding is disabled."))
		}
		return nil, refusalV3(settingsRefusalV3(TerminalNoAlternateVersionV3, TerminalMessage4KTranscodeDisabledV3))
	}
	if registry == nil {
		registry = in.hlsVideoRegistry()
	}
	if registry == nil || !registry.Available(TransformationVideoToH264V3) || !registry.Available(TransformationAudioToAACV3) {
		if lowerVersionTakesTranscodeV3(in, source, f.quality) {
			return nil, refusalV3(terminalPlannerResultV3(TerminalNoAlternateVersionV3, terminalMessageLowerVersionServesV3, false))
		}
		return nil, refusalV3(terminalPlannerResultV3("conversion_tool_unavailable", "The required validated H.264/AAC conversion toolchain is unavailable.", true))
	}
	return p.sizeTranscode(setup, registry)
}

// sizeTranscode sets the encode's audio share, bitrate and frame, and picks
// HEVC when the executor and the device both take it.
func (p *candidatePlannerV3) sizeTranscode(setup *transcodeSetupV3, registry *TransformationRegistryV3) (*transcodeSetupV3, *PlannerResultV3) {
	in, source, quality := p.input, p.source, p.video.quality
	capKbps := optionalValueV3(in.Request.BandwidthCapKbps)
	// On the local network with no estimate and no cap, nothing says the link
	// is short, so an automatic encode keeps the source's bitrate.
	unbounded := in.LocalNetwork && in.Request.BandwidthEstimateKbps == nil && capKbps <= 0 && in.ServerBitrateCapKbps <= 0
	if !quality.ExplicitRung && !unbounded {
		outputClass := ladderClassForSize(quality.Width, quality.Height)
		if quality.PreservesSource {
			outputClass = ladderClassForSize(0, quality.Height)
		}
		if outputClass > 0 {
			quality.BitrateKbps = min(quality.BitrateKbps, autoClassCeilingKbpsV3(outputClass))
		}
	}
	setup.audioChannels = aacOutputChannelsV3(in.Request, DeliveryClassHLSV3, source.AudioChannels, true)
	_, defaultAudioKbps := ResolveAACOutputV3(setup.audioChannels, 0)
	audioShare := func(budget int) int { return min(defaultAudioKbps, max(64, budget/4)) }
	budget := quality.BudgetKbps * 95 / 100
	if in.ServerBitrateCapKbps > 0 {
		capBudget := capKbps * 95 / 100
		if capBudget-audioShare(capBudget) < 64 {
			return nil, refusalV3(terminalPlannerResultV3(TerminalBitratePolicyUnavailableV3, "The server bitrate limit is too low for a playable video stream.", false))
		}
		budget = minPositiveV3(budget, capBudget)
	}
	if budget > 0 {
		setup.audioKbps = audioShare(budget)
		if _, stereoKbps := ResolveAACOutputV3(2, 0); setup.audioChannels > 2 && setup.audioKbps < stereoKbps {
			setup.audioChannels = 2
		}
		quality.BitrateKbps = min(quality.BitrateKbps, max(budget-setup.audioKbps, 64))
	}
	if top := bitrateLadder[0]; quality.Height > top.Height {
		width, height := FitLadderBox(source.Width, source.Height, top.Height)
		width, height = encodedFrame(source.Width, source.Height, width, height)
		quality.Label, quality.Width, quality.Height, quality.Class = heightLabel(height), width, height, top.Height
		quality.PreservesSource = false
	}
	setup.h264, setup.hevc = h264BoundedQualityV3(in.Request, quality, source), quality
	chosen := setup.h264
	if in.Settings.AllowHEVCEncoding && registry.Available(TransformationVideoToHEVCV3) {
		if sourceKbps := sourceEquivalentKbps(source, transcodeCodecHEVC); sourceKbps > 0 && setup.hevc.BitrateKbps > 0 {
			setup.hevc.BitrateKbps = min(setup.hevc.BitrateKbps, sourceKbps)
		}
		if hlsHEVCOutputSupportedV3(in.Request, setup.hevc, source) {
			setup.useHEVC, chosen = true, setup.hevc
		}
	}
	if lowerVersionTakesTranscodeV3(in, source, chosen) {
		return nil, refusalV3(terminalPlannerResultV3(TerminalNoAlternateVersionV3, terminalMessageLowerVersionServesV3, false))
	}
	return setup, nil
}
