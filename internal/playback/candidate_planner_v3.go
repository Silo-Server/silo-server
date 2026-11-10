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
type candidateV3 struct {
	route         candidateRouteV3
	delivery      string
	video         candidateVideoV3
	convertsAudio bool
	subtitle      candidateSubtitleV3
}

// Wire tokens the candidate planner shares with the walk in plan_v3.go.
const (
	decisionReasonValidatedOriginalV3 = "validated_original_playback"
	degradationDVELDiscardedV3        = "dolby_vision_enhancement_layer_discarded"
	degradationDVRemovedV3            = "dolby_vision_removed"
	mimeHLSV3                         = "application/vnd.apple.mpegurl"
	seekRestorationPlayerPositionV3   = "player_position"
	dvBaseLayerReasonV3               = "base_layer_compatible_hevc"
	degradationDVBaseLayerOnlyV3      = "dolby_vision_base_layer_only"
	transcodeReasonCopyExhaustedV3    = "copy_routes_exhausted"
	audioReasonNoTrackV3              = "no_audio_track"
)

type candidateRouteV3 int

const (
	candidateOriginalV3 candidateRouteV3 = iota
	candidateRemuxV3
	candidateTranscodeV3
)

// candidateVideoV3 values are listed in preference order.
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

// candidateSubtitleV3 is how an HLS stream carries the selected subtitle;
// every other route uses the delivery's own subtitle decision.
type candidateSubtitleV3 int

const (
	candidateOnDemandRenditionsV3 candidateSubtitleV3 = iota
	candidatePipelineRenditionV3
	candidateSidecarV3
)

// candidatePreferenceV3 orders candidates. Each rule breaks the ties the
// rules before it leave.
var candidatePreferenceV3 = []func(a, b candidateV3) int{
	// The original file, then a copy remux, then an encode.
	func(a, b candidateV3) int { return cmp.Compare(a.route, b.route) },
	// The video recipe, in candidateVideoV3 order: Dolby Vision conversions
	// before the copy, the copy before stripping, HEVC before H.264.
	func(a, b candidateV3) int { return cmp.Compare(a.video, b.video) },
	// Audio copied before converted.
	func(a, b candidateV3) int { return compareFalseFirstV3(a.convertsAudio, b.convertsAudio) },
	// Progressive before HLS.
	func(a, b candidateV3) int {
		return compareFalseFirstV3(a.delivery == DeliveryClassHLSV3, b.delivery == DeliveryClassHLSV3)
	},
	// On-demand renditions, then the pipeline rendition, then a sidecar.
	func(a, b candidateV3) int { return cmp.Compare(a.subtitle, b.subtitle) },
}

func compareFalseFirstV3(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
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

// planPlaybackCandidatesV3 returns what PlanPlaybackV3 returns, chosen from a
// sorted candidate list instead of a walk.
func planPlaybackCandidatesV3(input PlannerInputV3) PlannerResultV3 {
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
	// Under a cap the source must be encoded to fit, every candidate that
	// survives encodes, so only a refusal changes: the cap names it.
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

// selectCandidateV3 checks candidates in order and returns the first viable
// one not yet attempted. enter runs once before the first candidate of each
// route, and once more for every route the list never reached; it can end
// planning with a refusal. evaluate builds a candidate's plan, reports whether
// it is viable, or ends planning with a refusal.
func (p *candidatePlannerV3) selectCandidateV3(candidates []candidateV3, enter func(candidateRouteV3) *PlannerResultV3,
	evaluate func(candidateV3) (PlannerResultV3, bool, *PlannerResultV3), exhausted PlannerResultV3) PlannerResultV3 {
	output := p.input.Request.ClientPlaybackContext.Output.OutputContextID
	next := candidateOriginalV3
	for _, candidate := range candidates {
		for ; next <= candidate.route; next++ {
			if stop := enter(next); stop != nil {
				return *stop
			}
		}
		result, ok, stop := evaluate(candidate)
		if stop != nil {
			return *stop
		}
		if ok && !planAttemptedV3(*result.Plan, output, p.input.AttemptedKeys) {
			return result
		}
	}
	for ; next <= candidateTranscodeV3; next++ {
		if stop := enter(next); stop != nil {
			return *stop
		}
	}
	return exhausted
}

func refusalV3(result PlannerResultV3) *PlannerResultV3 { return &result }

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
		Timeline:               TimelineV3{SourceStartSeconds: floatOrZeroV3(request.StartPosition), PlayerStartSeconds: floatOrZeroV3(request.StartPosition), CanSeekAnywhere: true, SeekRestoration: seekRestorationPlayerPositionV3},
	}
}

func (p *candidatePlannerV3) finalizeV3(plan *PlanV3) {
	finalizePlanIdentityV3(plan, p.input.Request.PlaybackAttemptID, p.input.Request.ClientPlaybackContext.Output.OutputContextID)
}

// planAudioOnly plans a file without a video track: the original file, or a
// progressive remux whose one audio recipe is fixed by the facts.
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
	remux.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: containerMP4V3, MIMEType: AudioOnlyRemuxMIMEV3, Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
	remux.DecisionReason = decisionReasonContainerNormalizationV3
	// The remux copies unless the copy cannot play there; then it converts.
	forced := !audioOK || capExceeded
	convert := forced || !deliverySupportsPlanV3(request, DeliveryClassProgressiveV3, remux)

	candidates := []candidateV3{{route: candidateRemuxV3, delivery: DeliveryClassProgressiveV3, convertsAudio: convert}}
	if audioOK && !capExceeded && containsFoldV3(request.Capabilities.Containers, source.Container) &&
		(audioSelectionUsesContainerDefaultV3(p.file, p.input.AudioTrackIndex) || clientSelectsOriginalAudioTrackV3(request)) {
		candidates = append(candidates, candidateV3{route: candidateOriginalV3, delivery: DeliveryClassOriginalHTTPV3})
	}
	sortCandidatesV3(candidates)
	enter := func(route candidateRouteV3) *PlannerResultV3 {
		if route == candidateRemuxV3 && !deliveryAvailableV3(request, DeliveryClassProgressiveV3) {
			return refusalV3(terminalPlannerResultV3("adaptation_unavailable", "No validated playback route is available for this audio source.", false))
		}
		return nil
	}
	evaluate := func(c candidateV3) (PlannerResultV3, bool, *PlannerResultV3) {
		if c.route == candidateOriginalV3 {
			plan := base
			plan.Delivery = DeliveryOriginalHTTPV3
			plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: source.Container, MIMEType: MimeFromExtension(p.file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
			plan.DecisionReason = decisionReasonValidatedOriginalV3
			p.finalizeV3(&plan)
			return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1},
				deliverySupportsPlanV3(request, DeliveryClassOriginalHTTPV3, plan), nil
		}
		return p.audioOnlyRemux(remux, c.convertsAudio, forced, capExceeded)
	}
	exhausted := terminalPlannerResultV3("adaptation_exhausted", "All compatible playback recipes have already failed for this output route.", false)
	return p.selectCandidateV3(candidates, enter, evaluate, exhausted)
}

func (p *candidatePlannerV3) audioOnlyRemux(plan PlanV3, convert, forced, capExceeded bool) (PlannerResultV3, bool, *PlannerResultV3) {
	request, source := p.input.Request, p.source
	undecodable := refusalV3(terminalPlannerResultV3("adaptation_unavailable", "The progressive delivery cannot decode the planned audio recipe.", false))
	channels, bitrate := 0, 0
	if convert {
		registry := p.input.progressiveRemuxRegistry()
		aacOK := registry != nil && registry.Available(TransformationAudioToAACV3)
		channels = audioOnlyAACOutputChannelsV3(request, source)
		bitrate = audioOnlyAACBitrateKbpsV3(optionalValueV3(request.BandwidthCapKbps))
		if forced && p.input.ServerBitrateCapKbps > 0 {
			bitrate = min(bitrate, p.input.ServerBitrateCapKbps*95/100)
		}
		switch {
		case !aacOK && forced:
			return PlannerResultV3{}, false, refusalV3(terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, "The required validated AAC conversion toolchain is unavailable.", true))
		case !aacOK:
			return PlannerResultV3{}, false, undecodable
		case bitrate < 32 && forced && p.input.ServerBitrateCapKbps > 0:
			return PlannerResultV3{}, false, refusalV3(terminalPlannerResultV3(TerminalBitratePolicyUnavailableV3, "The server bitrate limit is too low for a playable audio stream.", false))
		}
		applyAudioOnlyAACConversionV3(&plan, channels, bitrate, forced && capExceeded)
	}
	if !deliverySupportsPlanV3(request, DeliveryClassProgressiveV3, plan) {
		return PlannerResultV3{}, false, undecodable
	}
	p.finalizeV3(&plan)
	return PlannerResultV3{Plan: &plan, PlayMethod: PlayRemux, TranscodeAudio: convert, TargetAudioCodec: plan.EffectiveRecipe.AudioCodec, SourceAudioChannels: stereoDownmixSourceChannelsV3(source.AudioChannels, channels, convert), TargetAudioChannels: channels, TargetAudioBitrateKbps: bitrate, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}, true, nil
}
