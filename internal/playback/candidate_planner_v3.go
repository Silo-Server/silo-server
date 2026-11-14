package playback

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A candidate is one complete way to play the file: one option per dimension.
// The planner lists every candidate the facts allow, sorts the list by
// candidatePreferenceV3, and takes the first viable one that has not been
// attempted. Viability is checked lazily from the top, so a lookup that can
// reach pooled nodes or run FFmpeg runs only for a candidate that needs it.
// Each dimension's values are declared in preference order.
type candidateV3 struct {
	route    candidateRouteV3
	video    candidateVideoV3
	audio    candidateAudioV3
	delivery candidateDeliveryV3
	subtitle candidateSubtitleV3
}

type candidateRouteV3 int

const (
	candidateOriginalV3 candidateRouteV3 = iota
	candidateRemuxV3
	candidateTranscodeV3
)

type candidateVideoV3 int

const (
	candidateClientDV81V3 candidateVideoV3 = iota
	candidateClientHDR10V3
	candidateServerDV81V3
	candidateCopyV3
	candidateDV8BaseLayerV3
	candidateClientManagedV3
	candidateServerStripV3
	candidateEncodeHEVCV3
	candidateEncodeH264V3
)

type candidateAudioV3 int

const (
	candidateAudioCopyV3 candidateAudioV3 = iota
	candidateAudioConvertV3
)

type candidateDeliveryV3 int

const (
	candidateOriginalHTTPV3 candidateDeliveryV3 = iota
	candidateProgressiveV3
	candidateHLSV3
)

// candidateSubtitleV3 is how an HLS stream carries the selected subtitle;
// other deliveries use their own subtitle decision.
type candidateSubtitleV3 int

const (
	candidateOnDemandRenditionsV3 candidateSubtitleV3 = iota
	candidatePipelineRenditionV3
	candidateSidecarV3
)

// candidatePreferenceV3 orders candidates; each rule breaks the ties the rules
// before it leave.
var candidatePreferenceV3 = []func(a, b candidateV3) int{
	// The original file, then a copy remux, then an encode.
	func(a, b candidateV3) int { return cmp.Compare(a.route, b.route) },
	// Dolby Vision conversions before the copy, the copy before the strip,
	// HEVC before H.264.
	func(a, b candidateV3) int { return cmp.Compare(a.video, b.video) },
	// Audio copied before converted.
	func(a, b candidateV3) int { return cmp.Compare(a.audio, b.audio) },
	// Progressive before HLS.
	func(a, b candidateV3) int { return cmp.Compare(a.delivery, b.delivery) },
	// On-demand renditions, then the pipeline rendition, then a sidecar.
	func(a, b candidateV3) int { return cmp.Compare(a.subtitle, b.subtitle) },
}

func sortCandidatesV3(candidates []candidateV3) {
	slices.SortStableFunc(candidates, func(a, b candidateV3) int {
		for _, rule := range candidatePreferenceV3 {
			if order := rule(a, b); order != 0 {
				return order
			}
		}
		return 0
	})
}

func audioOpV3(convert bool) candidateAudioV3 {
	if convert {
		return candidateAudioConvertV3
	}
	return candidateAudioCopyV3
}

type candidatePlannerV3 struct {
	input             PlannerInputV3
	file              *models.MediaFile
	source            SourceDescriptorV3
	capRequiresEncode bool
	video             videoFactsV3
	// hlsAACMissing is set when an HLS copy remux needed an AAC conversion no
	// HLS remux executor offers.
	hlsAACMissing string
	transcode     *transcodeSetupV3
	transcodeStop *PlannerResultV3
}

// PlanPlaybackV3 chooses a playable protocol-v3 route for one file from
// source and client facts.
func PlanPlaybackV3(input PlannerInputV3) PlannerResultV3 {
	if input.RequestedFile == nil {
		return terminalPlannerResultV3("source_unavailable", "The requested media source is unavailable.", false)
	}
	file := input.EffectiveFile
	if file == nil {
		file = input.RequestedFile
	}
	if file.ProbeRejected() {
		return terminalPlannerResultV3(TerminalSourceUnreadableV3, TerminalSourceUnreadableMessageV3, false)
	}
	// No route plays an audio track FFmpeg cannot decode; another track can.
	if input.AudioTrackIndex >= 0 && input.AudioTrackIndex < len(file.AudioTracks) && !AudioTrackDecodable(file.AudioTracks, input.AudioTrackIndex, file.CodecAudio) {
		message := TerminalAudioTrackUndecodableMessageV3
		if !AnyAudioTrackDecodable(file.AudioTracks, file.CodecAudio) {
			message = TerminalNoDecodableAudioMessageV3
		}
		return terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, message, false)
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	p := &candidatePlannerV3{input: memoizeLookupsV3(input), file: file}
	p.source = SourceDescriptorFromFileV3(file, input.AudioTrackIndex)
	p.capRequiresEncode = applyServerBitrateCapV3(&p.input, file, p.source)
	var result PlannerResultV3
	if file.IsAudioOnly() {
		result = p.planAudioOnly()
	} else {
		result = p.planVideo()
	}
	// Under a cap the source must be encoded to fit, only encoding candidates
	// exist, so only a refusal changes: the cap names it.
	if p.capRequiresEncode && result.Terminal != nil {
		result = terminalPlannerResultV3(TerminalBitratePolicyUnavailableV3, "This stream exceeds the server bitrate limit, and no compliant transcoding route is available.", result.Terminal.Retryable)
	}
	if result.Plan != nil {
		result.Plan.ActiveQuality = activeQualityV3(p.input.Request, *result.Plan)
		markNativeSelectableV3(result.Plan, file, p.input)
	}
	return result
}

// memoizeLookupsV3 makes each lazy lookup run at most once per planning run;
// candidates that share an executor pool or a file probe ask it repeatedly.
func memoizeLookupsV3(input PlannerInputV3) PlannerInputV3 {
	input.ProgressiveRemuxRegistry = memoV3(input.ProgressiveRemuxRegistry)
	input.HLSRemuxRegistry = memoV3(input.HLSRemuxRegistry)
	input.HLSVideoRegistry = memoV3(input.HLSVideoRegistry)
	input.HLSRegistry = memoV3(input.HLSRegistry)
	input.HLSToneMapCapabilities = memoV3(input.HLSToneMapCapabilities)
	input.DVRPUStrippable = memoV3(input.DVRPUStrippable)
	input.SubtitleRenditionExecutable = memoV3(input.SubtitleRenditionExecutable)
	input.RemuxSubtitleRenditionExecutable = memoV3(input.RemuxSubtitleRenditionExecutable)
	input.OnDemandSubtitleRenditionsExecutable = memoV3(input.OnDemandSubtitleRenditionsExecutable)
	input.RemuxOnDemandSubtitleRenditionsExecutable = memoV3(input.RemuxOnDemandSubtitleRenditionsExecutable)
	input.CueIndexedSubtitleTracks = memoV3(input.CueIndexedSubtitleTracks)
	return input
}

func memoV3[T any](lookup func() T) func() T {
	if lookup == nil {
		return nil
	}
	return sync.OnceValue(lookup)
}

// selectCandidateV3 returns the first viable candidate not yet attempted.
// enter runs once per route, in order, before that route's first candidate or
// at the end for routes without one, and may end planning with a refusal.
// evaluate returns a candidate's result, nil when it is not viable, or a
// refusal that ends planning.
func (p *candidatePlannerV3) selectCandidateV3(candidates []candidateV3, enter func(candidateRouteV3) *PlannerResultV3,
	evaluate func(candidateV3) (*PlannerResultV3, *PlannerResultV3), exhausted PlannerResultV3) PlannerResultV3 {
	output := p.input.Request.ClientPlaybackContext.Output.OutputContextID
	end := candidateTranscodeV3 + 1
	next := candidateOriginalV3
	for _, candidate := range append(candidates, candidateV3{route: end}) {
		for ; next <= candidate.route && next < end; next++ {
			if stop := enter(next); stop != nil {
				return *stop
			}
		}
		if candidate.route == end {
			break
		}
		result, stop := evaluate(candidate)
		if stop != nil {
			return *stop
		}
		if result != nil && !planAttemptedV3(*result.Plan, output, p.input.AttemptedKeys) {
			return *result
		}
	}
	return exhausted
}

func refusalV3(result PlannerResultV3) *PlannerResultV3 { return &result }

func exhaustedV3() PlannerResultV3 {
	return terminalPlannerResultV3("adaptation_exhausted", "All compatible playback recipes have already failed for this output route.", false)
}

// newPlanV3 is the plan every route of this file starts from.
func (p *candidatePlannerV3) newPlanV3(subtitle SubtitlePolicyResultV3) PlanV3 {
	request := p.input.Request
	return PlanV3{
		ProtocolVersion:        ProtocolV3,
		ExpiresAt:              NewPlanExpiryV3(p.input.Now),
		SelectedTracks:         selectedTracksForPlanV3(p.file, p.input.AudioTrackIndex, subtitle),
		EffectiveRecipe:        recipeFromSourceV3(p.source),
		Transformations:        []TransformationV3{},
		AppliedQuirks:          []AppliedQuirkV3{},
		RuntimeCorrections:     []string{},
		DegradationWarnings:    []DegradationWarningV3{},
		RequestedMediaFileID:   p.input.RequestedFile.ID,
		EffectiveMediaFileID:   p.file.ID,
		Source:                 p.source,
		SubtitleFidelityPolicy: subtitlePolicyNameV3(request.SubtitleFidelityPreference),
		Timeline:               TimelineV3{SourceStartSeconds: floatOrZeroV3(request.StartPosition), PlayerStartSeconds: floatOrZeroV3(request.StartPosition), CanSeekAnywhere: true, SeekRestoration: "player_position"},
	}
}

func (p *candidatePlannerV3) finalizeV3(plan *PlanV3) {
	finalizePlanIdentityV3(plan, p.input.Request.PlaybackAttemptID, p.input.Request.ClientPlaybackContext.Output.OutputContextID)
}

func streamV3(protocol StreamProtocolV3, container, mime string) StreamV3 {
	return StreamV3{Protocol: protocol, Container: container, MIMEType: mime, Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
}

// planAudioOnly plans a file without a video track: the original file, or a
// progressive remux whose one audio recipe the facts fix.
func (p *candidatePlannerV3) planAudioOnly() PlannerResultV3 {
	request, source := p.input.Request, p.source
	audioOK, _, audioClaims := audioEligibilityV3(source, request)
	capKbps := optionalValueV3(request.BandwidthCapKbps)
	capExceeded := capKbps > 0 && (source.BitrateKbps > capKbps || p.input.ServerBitrateCapKbps > 0 && source.BitrateKbps <= 0)
	if source.AudioCodec == "" {
		return terminalPlannerResultV3("source_metadata_incomplete", "The source is missing audio metadata required for a validated playback route.", true)
	}
	base := p.newPlanV3(SubtitlePolicyResultV3{SelectedIndex: -1, TransportIndex: -1})
	base.Claims = ValidationClaimsV3{Audio: audioClaims}
	base.Subtitle = SubtitleDecisionV3{Mode: SubtitleOffV3, Inventory: []SubtitleInventoryItemV3{}}
	base.AvailableQualities = audioAvailableQualitiesV3(source)
	remux := base
	remux.Delivery = DeliveryRemuxProgressiveV3
	remux.Stream = streamV3(StreamHTTPProgressiveV3, containerMP4V3, AudioOnlyRemuxMIMEV3)
	remux.DecisionReason = decisionReasonContainerNormalizationV3
	// The remux copies unless the copy cannot play there.
	forced := !audioOK || capExceeded
	convert := forced || !deliverySupportsPlanV3(request, DeliveryClassProgressiveV3, remux)
	candidates := []candidateV3{{route: candidateRemuxV3, delivery: candidateProgressiveV3, audio: audioOpV3(convert)}}
	if audioOK && !capExceeded && containsFoldV3(request.Capabilities.Containers, source.Container) &&
		(audioSelectionUsesContainerDefaultV3(p.file, p.input.AudioTrackIndex) || clientSelectsOriginalAudioTrackV3(request)) {
		candidates = append(candidates, candidateV3{route: candidateOriginalV3})
	}
	sortCandidatesV3(candidates)
	enter := func(route candidateRouteV3) *PlannerResultV3 {
		if route == candidateRemuxV3 && !deliveryAvailableV3(request, DeliveryClassProgressiveV3) {
			return refusalV3(terminalPlannerResultV3("adaptation_unavailable", "No validated playback route is available for this audio source.", false))
		}
		return nil
	}
	evaluate := func(c candidateV3) (*PlannerResultV3, *PlannerResultV3) {
		if c.route == candidateRemuxV3 {
			return p.audioOnlyRemux(remux, convert, forced, capExceeded)
		}
		plan := base
		plan.Delivery = DeliveryOriginalHTTPV3
		plan.Stream = streamV3(StreamHTTPProgressiveV3, source.Container, MimeFromExtension(p.file.FilePath))
		plan.DecisionReason = decisionReasonValidatedOriginalV3
		p.finalizeV3(&plan)
		if !deliverySupportsPlanV3(request, DeliveryClassOriginalHTTPV3, plan) {
			return nil, nil
		}
		return &PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}, nil
	}
	return p.selectCandidateV3(candidates, enter, evaluate, exhaustedV3())
}

func (p *candidatePlannerV3) audioOnlyRemux(plan PlanV3, convert, forced, capExceeded bool) (*PlannerResultV3, *PlannerResultV3) {
	request, source := p.input.Request, p.source
	undecodable := refusalV3(terminalPlannerResultV3("adaptation_unavailable", "The progressive delivery cannot decode the planned audio recipe.", false))
	channels, bitrate := 0, 0
	if convert {
		registry := p.input.progressiveRemuxRegistry()
		aacOK := registry != nil && registry.Available(TransformationAudioToAACV3)
		channels = audioOnlyAACOutputChannelsV3(request, source)
		bitrate = audioOnlyAACBitrateKbpsV3(optionalValueV3(request.BandwidthCapKbps))
		capped := forced && p.input.ServerBitrateCapKbps > 0
		if capped {
			bitrate = min(bitrate, p.input.ServerBitrateCapKbps*95/100)
		}
		switch {
		case !aacOK && forced:
			return nil, refusalV3(terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, "The required validated AAC conversion toolchain is unavailable.", true))
		case !aacOK:
			return nil, undecodable
		case capped && bitrate < 32:
			return nil, refusalV3(terminalPlannerResultV3(TerminalBitratePolicyUnavailableV3, "The server bitrate limit is too low for a playable audio stream.", false))
		}
		applyAudioOnlyAACConversionV3(&plan, channels, bitrate, forced && capExceeded)
	}
	if !deliverySupportsPlanV3(request, DeliveryClassProgressiveV3, plan) {
		return nil, undecodable
	}
	p.finalizeV3(&plan)
	return &PlannerResultV3{Plan: &plan, PlayMethod: PlayRemux, TranscodeAudio: convert, TargetAudioCodec: plan.EffectiveRecipe.AudioCodec, SourceAudioChannels: stereoDownmixSourceChannelsV3(source.AudioChannels, channels, convert), TargetAudioChannels: channels, TargetAudioBitrateKbps: bitrate, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}, nil
}
