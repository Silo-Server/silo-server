package playback

import (
	"fmt"
	"strconv"
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
		"-ac", strconv.Itoa(surroundBitstreamChannelsV3),
		"-ar", strconv.Itoa(surroundBitstreamSampleRateHzV3),
	}
}

func surroundBitstreamTransformationV3(codec string) (name, version string) {
	if codec == surroundBitstreamCodecAC3V3 {
		return TransformationAudioToAC3V3, TransformationAudioToAC3RecipeVersionV3
	}
	return TransformationAudioToEAC3V3, TransformationAudioToEAC3RecipeVersionV3
}

// surroundBitstreamCodecs lists, E-AC-3 first, the codecs a surround
// conversion may produce in place of the route the planner would otherwise
// choose. It is empty unless the client would decode a track with more than
// two channels locally and attests with exact evidence that its sink takes the
// codec as 5.1 but not the source codec, and the route keeps the original
// picture and subtitle presentation: native range, no Dolby Vision Profile 7
// handling, no server bitrate cap, and the same subtitle mode and claims as
// the original file.
func (p *candidatePlannerV3) surroundBitstreamCodecs() []string {
	f, in, source := &p.video, p.input, p.source
	request := in.Request
	if f.mustAdapt || !f.videoOK || !f.rangeOK || !f.audioOK || f.base.Claims.Audio.Passthrough ||
		f.audioClaims.Reason != audioClaimClientDecodeV3 || source.AudioChannels <= 2 ||
		source.DVProfile == 7 || source.VideoCopyUnsafe || !f.quality.PreservesSource || in.ServerBitrateCapKbps > 0 {
		return nil
	}
	if !f.originalSubtitleOK || !f.hlsSubtitleOK || f.hlsSubtitle.Claims != f.subtitle.Claims || f.hlsSubtitle.Decision.Mode != f.subtitle.Decision.Mode {
		return nil
	}
	if request.Capabilities.AudioEvidence != EvidenceExactV3 || !HasFeatureV3(request.ClientFeatures, FeatureLayoutPassthrough) ||
		!deliveryAvailableV3(request, DeliveryClassHLSV3) {
		return nil
	}
	caps := request.ClientPlaybackContext.Output.AudioPassthrough
	if caps == nil {
		caps = request.Capabilities.AudioPassthrough
	}
	// A sink that lists the source codec takes it as a bitstream; a layout
	// label the passthrough check did not match is no reason to re-encode.
	if caps == nil || containsFoldV3(caps.PassthroughCodecs, source.AudioCodec) {
		return nil
	}
	var codecs []string
	for _, codec := range []string{surroundBitstreamCodecEAC3V3, surroundBitstreamCodecAC3V3} {
		if !containsFoldV3(caps.PassthroughCodecs, codec) {
			continue
		}
		for _, entry := range caps.Entries {
			if strings.EqualFold(entry.Codec, codec) &&
				containsIntV3(entry.ChannelCounts, surroundBitstreamChannelsV3) &&
				containsFoldV3(entry.Layouts, audioLayoutSurround51V3) {
				codecs = append(codecs, codec)
				break
			}
		}
	}
	return codecs
}

// surroundBitstreamCandidate builds the copy-video HLS remux that converts
// the selected audio to the first listed codec that an HLS executor offers
// and the HLS delivery accepts, or nil when none qualifies. It is one
// candidate whatever the codec, so once it was attempted the planner moves on
// to the route it improved on.
func (p *candidatePlannerV3) surroundBitstreamCandidate() *PlannerResultV3 {
	for _, codec := range p.surroundBitstreamCodecs() {
		if result := p.surroundBitstreamPlan(codec); result != nil {
			return result
		}
	}
	return nil
}

func (p *candidatePlannerV3) surroundBitstreamPlan(target string) *PlannerResultV3 {
	f, request, source := &p.video, p.input.Request, p.source
	name, version := surroundBitstreamTransformationV3(target)
	if !p.input.hlsRemuxRegistry().Available(name) {
		return nil
	}
	plan := p.remuxBase(candidateCopyV3)
	plan.Delivery = DeliveryRemuxHLSV3
	plan.Stream = streamV3(StreamHLSV3, containerHLSV3, mimeHLSV3)
	if deliverySupportsFeatureV3(request, DeliveryClassHLSV3, FeatureHLSCopyMPEGTSV3) && HLSCopyFitsMPEGTS(source.VideoCodec, target) {
		// Package it as the client's other copy remuxes are packaged.
		plan.EffectiveRecipe.SegmentContainer = SegmentContainerMPEGTSV3
	} else {
		plan.EffectiveRecipe.VideoSampleEntry = hlsVideoSampleEntryV3(source, request, false)
	}
	plan.EffectiveRecipe.AudioCodec = target
	plan.EffectiveRecipe.AudioChannels = intPointerV3(surroundBitstreamChannelsV3)
	plan.EffectiveRecipe.AudioLayout = audioLayoutSurround51V3
	plan.Claims.Audio = AudioClaimsV3{Codec: target, Passthrough: true, Reason: decisionReasonSurroundBitstreamV3}
	plan.Transformations = append(plan.Transformations, TransformationV3{Name: name, Executor: ExecutorServerV3, RecipeVersion: version, ValidatedClaims: []string{ClaimAudioDecodeV3}})
	plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationAudioConvertedV3, Message: surroundBitstreamWarningV3(source, target)})
	plan.DecisionReason = decisionReasonSurroundBitstreamV3
	p.remuxQuirks(&plan, candidateCopyV3, false)
	sub := f.hlsSubtitle
	applySubtitleDecisionV3(&plan, sub.Decision)
	plan.Claims.Subtitles = sub.Claims
	p.finalizeV3(&plan)
	if !deliverySupportsPlanV3(request, DeliveryClassHLSV3, plan) {
		return nil
	}
	return &PlannerResultV3{
		Plan:                        &plan,
		PlayMethod:                  PlayRemux,
		TranscodeAudio:              true,
		TargetVideoCodec:            codecCopyV3,
		TargetAudioCodec:            target,
		TargetAudioChannels:         surroundBitstreamChannelsV3,
		TargetAudioBitrateKbps:      surroundBitstreamBitrateKbpsV3,
		TargetResolution:            resolutionLabelV3(source.Height),
		TargetBitrateKbps:           source.BitrateKbps,
		SubtitleTrackIndex:          sub.SelectedIndex,
		SubtitleTransportTrackIndex: sub.TransportIndex,
		SubtitleCodec:               sub.Codec,
		DownloadedSubtitleID:        sub.DownloadedSubtitleID,
	}
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
