package playback

import (
	"fmt"
	"strings"
)

// Surround bitstream conversion turns a multichannel track the client would
// decode to PCM into Dolby Digital Plus (or Dolby Digital) 5.1 that its HDMI
// sink passes through.
//
// Android TVs decode TrueHD and DTS with the bundled FFmpeg decoder, but a
// TV's own HDMI/eARC output commonly mixes app PCM down to stereo, so a 5.1
// receiver gets 2.0. Android cannot tell that mixer apart from one that
// carries multichannel PCM: a failed direct multichannel PCM probe does not
// prove a stereo mixer. Like Plex, the server therefore prefers E-AC-3 or
// AC-3 5.1 whenever the sink validates that bitstream but not the source
// codec. A device whose mixer does carry multichannel PCM gives up lossless
// PCM for lossy 5.1 surround; one that passes the source codec through is
// unaffected, because validated passthrough wins first.
const (
	surroundBitstreamCodecEAC3V3      = audioCodecEAC3
	surroundBitstreamCodecAC3V3       = audioCodecAC3
	surroundBitstreamChannelsV3       = 6
	surroundBitstreamBitrateKbpsV3    = 640
	surroundBitstreamSampleRateHzV3   = 48000
	decisionReasonSurroundBitstreamV3 = "surround_bitstream_conversion"
	audioClaimClientDecodeV3          = "client_decode_supported"
	ffmpegAudioCodecArg               = "-c:a"
	ffmpegAudioBitrateArg             = "-b:a"
	ffmpegInputFormatLavfiV3          = "lavfi"
)

// surroundBitstreamAudioArgsV3 is the frozen audio_to_eac3@1 / audio_to_ac3@1
// encode: 48 kHz, six channels, 640 kbps. The registry smoke test and the
// executor share it, so an advertised recipe is the recipe that runs.
func surroundBitstreamAudioArgsV3(codec string) []string {
	return []string{
		ffmpegAudioCodecArg, codec,
		ffmpegAudioBitrateArg, fmt.Sprintf("%dk", surroundBitstreamBitrateKbpsV3),
		"-ac", fmt.Sprintf("%d", surroundBitstreamChannelsV3),
		"-ar", fmt.Sprintf("%d", surroundBitstreamSampleRateHzV3),
	}
}

func surroundBitstreamTransformationV3(codec string) (name, version string) {
	if codec == surroundBitstreamCodecAC3V3 {
		return TransformationAudioToAC3V3, TransformationAudioToAC3RecipeVersionV3
	}
	return TransformationAudioToEAC3V3, TransformationAudioToEAC3RecipeVersionV3
}

// surroundBitstreamTargetV3 returns the codec a surround conversion should
// produce for this request, or "" when the rule does not apply. It applies
// only when the client would decode the source locally, the source has more
// than two channels, and the client attests with exact evidence that its sink
// takes E-AC-3 (preferred) or AC-3 as 5.1.
func surroundBitstreamTargetV3(source SourceDescriptorV3, request StartRequestV3, audioOK, passthrough bool, claim AudioClaimsV3) string {
	if !audioOK || passthrough || claim.Reason != audioClaimClientDecodeV3 || source.AudioChannels <= 2 {
		return ""
	}
	if request.Capabilities.AudioEvidence != EvidenceExactV3 || !HasFeatureV3(request.ClientFeatures, FeatureLayoutPassthrough) {
		return ""
	}
	caps := request.ClientPlaybackContext.Output.AudioPassthrough
	if caps == nil {
		caps = request.Capabilities.AudioPassthrough
	}
	// A sink that lists the source codec takes it as a bitstream; a layout
	// label the passthrough check did not match is no reason to re-encode.
	if caps == nil || containsFoldV3(caps.PassthroughCodecs, source.AudioCodec) {
		return ""
	}
	for _, codec := range []string{surroundBitstreamCodecEAC3V3, surroundBitstreamCodecAC3V3} {
		if !containsFoldV3(caps.PassthroughCodecs, codec) {
			continue
		}
		for _, entry := range caps.Entries {
			if strings.EqualFold(entry.Codec, codec) &&
				containsIntV3(entry.ChannelCounts, surroundBitstreamChannelsV3) &&
				containsFoldV3(entry.Layouts, audioLayoutSurround51V3) {
				return codec
			}
		}
	}
	return ""
}

// planSurroundBitstreamRemuxV3 builds the copy-video HLS remux that converts
// the selected audio to target. The caller has already required the original
// picture; this returns false when the route would change the subtitle
// presentation, when no HLS executor offers the recipe, or when the client
// already tried it, and the caller then keeps the route it would have chosen.
func planSurroundBitstreamRemuxV3(input PlannerInputV3, base PlanV3, source SourceDescriptorV3, target string, original, hls SubtitlePolicyResultV3, high10Quirk *AppliedQuirkV3) (PlannerResultV3, bool) {
	if !deliveryAvailableV3(input.Request, DeliveryClassHLSV3) || input.ServerBitrateCapKbps > 0 || source.VideoCopyUnsafe {
		return PlannerResultV3{}, false
	}
	if hls.Terminal != nil || hls.RequiresBurn || hls.Claims != original.Claims || hls.Decision.Mode != original.Decision.Mode {
		return PlannerResultV3{}, false
	}
	name, version := surroundBitstreamTransformationV3(target)
	if !input.hlsRemuxRegistry().Available(name) {
		return PlannerResultV3{}, false
	}
	plan := cloneRemuxPlanCandidateV3(base)
	plan.Delivery = DeliveryRemuxHLSV3
	plan.Stream = StreamV3{Protocol: StreamHLSV3, Container: containerHLSV3, MIMEType: mimeTypeHLSV3, Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
	plan.EffectiveRecipe.VideoSampleEntry = hlsVideoSampleEntryV3(source, input.Request, false)
	plan.EffectiveRecipe.AudioCodec = target
	plan.EffectiveRecipe.AudioChannels = intPointerV3(surroundBitstreamChannelsV3)
	plan.EffectiveRecipe.AudioLayout = audioLayoutSurround51V3
	plan.Claims.Audio = AudioClaimsV3{Codec: target, Passthrough: true, Reason: decisionReasonSurroundBitstreamV3}
	plan.Transformations = append(plan.Transformations, TransformationV3{Name: name, Executor: ExecutorServerV3, RecipeVersion: version, ValidatedClaims: []string{ClaimAudioDecodeV3}})
	plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationAudioConvertedV3, Message: surroundBitstreamWarningV3(source, target)})
	plan.DecisionReason = decisionReasonSurroundBitstreamV3
	applyCopiedVideoQuirksV3(&plan, source, input.Request, high10Quirk)
	applySubtitleDecisionV3(&plan, hls.Decision)
	plan.Claims.Subtitles = hls.Claims
	finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID, input.Request.ClientPlaybackContext.Output.OutputContextID)
	if !deliverySupportsPlanV3(input.Request, DeliveryClassHLSV3, plan) || planAttemptedV3(plan, input.Request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
		return PlannerResultV3{}, false
	}
	return PlannerResultV3{
		Plan:                        &plan,
		PlayMethod:                  PlayRemux,
		TranscodeAudio:              true,
		TargetVideoCodec:            codecCopyV3,
		TargetAudioCodec:            target,
		TargetAudioChannels:         surroundBitstreamChannelsV3,
		TargetAudioBitrateKbps:      surroundBitstreamBitrateKbpsV3,
		TargetResolution:            resolutionLabelV3(source.Height),
		TargetBitrateKbps:           source.BitrateKbps,
		SubtitleTrackIndex:          hls.SelectedIndex,
		SubtitleTransportTrackIndex: hls.TransportIndex,
		SubtitleCodec:               hls.Codec,
		DownloadedSubtitleID:        hls.DownloadedSubtitleID,
	}, true
}

func surroundBitstreamWarningV3(source SourceDescriptorV3, target string) string {
	label := "E-AC-3"
	if target == surroundBitstreamCodecAC3V3 {
		label = "AC-3"
	}
	message := fmt.Sprintf("The selected audio track is converted to %s 5.1 so the audio output receives surround sound.", label)
	if source.AudioChannels > surroundBitstreamChannelsV3 {
		message += fmt.Sprintf(" The source's %d channels are mixed to 5.1.", source.AudioChannels)
	}
	return message
}

// IsSurroundBitstreamConversionV3 reports whether plan is the optional
// surround conversion, which a caller may drop in favor of direct play when
// admission or route preparation refuses it.
func IsSurroundBitstreamConversionV3(plan *PlanV3) bool {
	return plan != nil && plan.DecisionReason == decisionReasonSurroundBitstreamV3
}
