package playback

import "slices"

// Adaptation reasons say why a planned route changes the file instead of
// playing it as it is: why it encodes the video, converts the audio, or
// repackages the streams. The admin activity views show them. The planner
// derives them from the facts it chose the route by, because a plan's single
// decision_reason names only the last step that shaped the recipe (a video the
// device cannot decode reports its quality step, quality_auto_source).
//
// The vocabulary is closed and stored with the session; add values, never
// rename one.
const (
	// The device cannot decode the video codec, profile, level or bit depth.
	AdaptationVideoCodecUnsupported = "video_codec_unsupported"
	// The client's capability evidence cannot confirm it decodes the video.
	AdaptationVideoCapabilityUnconfirmed = "video_capability_unconfirmed"
	// The output cannot show the source's HDR or Dolby Vision as it is.
	AdaptationDynamicRangeUnsupported = "dynamic_range_unsupported"
	// The source is taller than the device's maximum resolution.
	AdaptationResolutionLimit = "resolution_limit"
	// The client's bandwidth estimate cannot carry the source.
	AdaptationBandwidthLimit = "bandwidth_limit"
	// Earlier buffering lowered the quality: the player reported rebuffering
	// during this play, or the server applied a limit it learned from recent
	// rebuffering on this device and connection.
	AdaptationBuffering = "buffering"
	// The viewer's Maximum bitrate setting (profile or device) is below the
	// source.
	AdaptationBandwidthCap = "bandwidth_cap"
	// The user's access group caps local or remote streams below the source.
	AdaptationServerBitrateLimit = "server_bitrate_limit"
	// The viewer chose a quality below the source.
	AdaptationQualitySelected = "quality_selected"
	// The selected subtitle is rendered into the video.
	AdaptationSubtitleBurnIn = "subtitle_burn_in"
	// Every route that copies the video already failed on the device.
	AdaptationDirectPlayFailed = "direct_play_failed"
	// The device, or the delivery it needs, cannot carry the audio codec.
	AdaptationAudioCodecUnsupported = "audio_codec_unsupported"
	// The audio has more channels than the output allows.
	AdaptationAudioChannelsUnsupported = "audio_channels_unsupported"
	// A known device bug needs the audio converted.
	AdaptationDeviceWorkaround = "device_workaround"
	// The selected audio track is not the one the original file plays.
	AdaptationAudioTrackSelection = "audio_track_selection"
	// The device cannot open the file's container or delivery.
	AdaptationContainerUnsupported = "container_unsupported"
	// The selected subtitle cannot be delivered with the original file.
	AdaptationSubtitleUnsupported = "subtitle_unsupported"
)

// AdaptationReasons lists the vocabulary in the order reasons are reported:
// video causes first, then quality, subtitle, audio and packaging.
var AdaptationReasons = []string{
	AdaptationVideoCodecUnsupported,
	AdaptationVideoCapabilityUnconfirmed,
	AdaptationDynamicRangeUnsupported,
	AdaptationResolutionLimit,
	AdaptationBandwidthLimit,
	AdaptationBuffering,
	AdaptationBandwidthCap,
	AdaptationServerBitrateLimit,
	AdaptationQualitySelected,
	AdaptationSubtitleBurnIn,
	AdaptationDirectPlayFailed,
	AdaptationAudioCodecUnsupported,
	AdaptationAudioChannelsUnsupported,
	AdaptationDeviceWorkaround,
	AdaptationAudioTrackSelection,
	AdaptationContainerUnsupported,
	AdaptationSubtitleUnsupported,
}

// adaptationReasonsV3 collects reasons and reports them in vocabulary order.
type adaptationReasonsV3 map[string]bool

func (r adaptationReasonsV3) add(reason string, when bool) {
	if when {
		r[reason] = true
	}
}

func (r adaptationReasonsV3) list() []string {
	var out []string
	for _, reason := range AdaptationReasons {
		if r[reason] {
			out = append(out, reason)
		}
	}
	return out
}

// NormalizeAdaptationReasons keeps the known reasons, once each, in
// vocabulary order. Stored and reported reasons pass through it, so a value a
// newer server wrote is dropped rather than shown raw.
func NormalizeAdaptationReasons(reasons []string) []string {
	set := adaptationReasonsV3{}
	for _, reason := range reasons {
		set.add(reason, slices.Contains(AdaptationReasons, reason))
	}
	return set.list()
}

// qualityAdaptationReasonV3 names what lowered an encode's quality.
func (p *candidatePlannerV3) qualityAdaptationReasonV3(quality QualityResultV3) string {
	switch quality.Reason {
	case qualityReasonDeviceLimitV3:
		return AdaptationResolutionLimit
	case qualityReasonBandwidthLimitV3:
		if p.input.Request.BandwidthEstimateSource == BandwidthEstimateObservedRebufferV3 {
			return AdaptationBuffering
		}
		return AdaptationBandwidthLimit
	case decisionReasonBandwidthCapV3:
		return p.capAdaptationReasonV3()
	case qualityReasonFixedRungV3:
		return AdaptationQualitySelected
	}
	return ""
}

// capAdaptationReasonV3 names whose bitrate cap binds: the server's stream
// limit when it is the lower one, else the request's.
func (p *candidatePlannerV3) capAdaptationReasonV3() string {
	if p.serverCapBinds {
		return AdaptationServerBitrateLimit
	}
	return AdaptationBandwidthCap
}

// videoAdaptationReasonsV3 explains a video file's selected route; direct
// play has no reasons.
func (p *candidatePlannerV3) videoAdaptationReasonsV3(result PlannerResultV3) []string {
	if result.Plan == nil {
		return nil
	}
	f, request, source := &p.video, p.input.Request, p.source
	reasons := adaptationReasonsV3{}
	switch result.PlayMethod {
	case PlayTranscode:
		reasons.add(AdaptationVideoCodecUnsupported, !f.videoOK && !f.evidenceInsufficient)
		reasons.add(AdaptationVideoCapabilityUnconfirmed, !f.videoOK && f.evidenceInsufficient)
		reasons.add(AdaptationDynamicRangeUnsupported, !f.anyCopyRange())
		if f.quality.RequiresTranscode {
			quality := p.qualityAdaptationReasonV3(f.quality)
			reasons.add(quality, quality != "")
			reasons.add(p.capAdaptationReasonV3(), p.capRequiresEncode)
		}
		reasons.add(AdaptationSubtitleBurnIn, result.SubtitleBurnIn)
		if f.transcodeReason == transcodeReasonCopyExhaustedV3 {
			// The video could be copied, but no copy route is left: either
			// they failed on the device, or none was open for this source.
			reasons.add(AdaptationDirectPlayFailed, len(p.input.AttemptedKeys) > 0)
			p.originalBlockersV3(reasons)
		}
		// An audio-converting remux may not fit the server's cap, so the
		// encode follows (transcodeStageFacts). The cap is the reason only
		// when it rejected that remux; one that fitted and then failed on the
		// device is a failed copy.
		if len(reasons) == 0 && p.input.ServerBitrateCapKbps > 0 && f.remuxActive &&
			(f.remuxAudio.progressiveConverts || f.remuxAudio.hlsConverts) {
			capRejected := p.capRejectsAudioRemuxV3()
			reasons.add(AdaptationServerBitrateLimit, capRejected)
			reasons.add(AdaptationDirectPlayFailed, !capRejected && len(p.input.AttemptedKeys) > 0)
		}
		// The encode always re-encodes audio; name the audio only when the
		// audio alone could not have been copied.
		reasons.add(AdaptationAudioCodecUnsupported, !f.audioOK)
		reasons.add(AdaptationAudioChannelsUnsupported, f.audioOK && !f.noAudioTrack &&
			deliveryExceedsMaxChannelsV3(request, DeliveryClassHLSV3, source.AudioCodec, source.AudioChannels))
	case PlayRemux:
		a := &f.remuxAudio
		if result.TranscodeAudio {
			hls := result.Plan.Delivery == DeliveryRemuxHLSV3
			codecConverts, channelLimited := a.progressiveCodecConverts, a.progressiveChannelLimited
			if hls {
				codecConverts, channelLimited = a.hlsCodecConverts, a.hlsChannelLimited
			}
			reasons.add(AdaptationAudioCodecUnsupported, !f.audioOK || codecConverts && a.firefoxAAC == nil)
			reasons.add(AdaptationAudioChannelsUnsupported, channelLimited)
			reasons.add(AdaptationDeviceWorkaround, a.firefoxAAC != nil || hls && a.hlsQuirkConverts)
		}
		reasons.add(AdaptationDynamicRangeUnsupported, !f.originalRangeOK())
		p.originalBlockersV3(reasons)
		p.attemptedOriginalV3(reasons)
	}
	return reasons.list()
}

// attemptedOriginalV3 names a failed direct play as the reason for a remux
// nothing else forced: the original route was open, so the planner passed it
// over only because the client had already tried it.
func (p *candidatePlannerV3) attemptedOriginalV3(reasons adaptationReasonsV3) {
	reasons.add(AdaptationDirectPlayFailed, len(reasons) == 0 && len(p.input.AttemptedKeys) > 0)
}

// capRejectsAudioRemuxV3 reports whether the server's bitrate cap left out an
// audio-converting copy remux, as progressiveCandidate and hlsRemuxCandidate
// check it.
func (p *candidatePlannerV3) capRejectsAudioRemuxV3() bool {
	a, request, source := &p.video.remuxAudio, p.input.Request, p.source
	progressiveChannels := aacOutputChannelsV3(request, DeliveryClassProgressiveV3, source.AudioChannels, a.progressiveChannelLimited)
	return a.progressiveConverts && !audioRemuxFitsServerCapV3(p.input, p.file, progressiveChannels) ||
		a.hlsConverts && !audioRemuxFitsServerCapV3(p.input, p.file, a.hlsAACChannels)
}

// originalBlockersV3 adds what kept a copyable video from the original route.
func (p *candidatePlannerV3) originalBlockersV3(reasons adaptationReasonsV3) {
	f, request := &p.video, p.input.Request
	reasons.add(AdaptationAudioChannelsUnsupported, !f.noAudioTrack &&
		deliveryExceedsMaxChannelsV3(request, DeliveryClassOriginalHTTPV3, p.source.AudioCodec, p.source.AudioChannels))
	reasons.add(AdaptationAudioTrackSelection, !f.originalAudioOK)
	reasons.add(AdaptationSubtitleUnsupported, !f.originalSubtitleOK)
	reasons.add(AdaptationContainerUnsupported, !containsFoldV3(request.Capabilities.Containers, p.source.Container) ||
		!deliveryAvailableV3(request, DeliveryClassOriginalHTTPV3))
}

// audioOnlyAdaptationReasonsV3 explains an audio-only file's selected route.
func (p *candidatePlannerV3) audioOnlyAdaptationReasonsV3(result PlannerResultV3, audioOK, capExceeded bool) []string {
	if result.Plan == nil || result.PlayMethod != PlayRemux {
		return nil
	}
	request, source := p.input.Request, p.source
	reasons := adaptationReasonsV3{}
	if result.TranscodeAudio {
		reasons.add(p.capAdaptationReasonV3(), capExceeded)
		// A convert neither the codec nor the cap forced is one the
		// progressive delivery needs: for its channel ceiling, or its codecs.
		channelLimited := deliveryExceedsMaxChannelsV3(request, DeliveryClassProgressiveV3, source.AudioCodec, source.AudioChannels)
		reasons.add(AdaptationAudioCodecUnsupported, !audioOK || !capExceeded && !channelLimited)
		reasons.add(AdaptationAudioChannelsUnsupported, audioOK && !capExceeded && channelLimited)
	}
	// The original delivery's own channel ceiling can rule it out while the
	// remux copies the audio.
	reasons.add(AdaptationAudioChannelsUnsupported, deliveryExceedsMaxChannelsV3(request, DeliveryClassOriginalHTTPV3, source.AudioCodec, source.AudioChannels))
	reasons.add(AdaptationAudioTrackSelection, !audioSelectionUsesContainerDefaultV3(p.file, p.input.AudioTrackIndex) && !clientSelectsOriginalAudioTrackV3(request))
	reasons.add(AdaptationContainerUnsupported, !containsFoldV3(request.Capabilities.Containers, source.Container) ||
		!deliveryAvailableV3(request, DeliveryClassOriginalHTTPV3))
	p.attemptedOriginalV3(reasons)
	return reasons.list()
}
