package playback

// videoFactsV3 is what the planner knows about a video file before it builds
// any candidate: what each delivery can carry, and the plan every route
// starts from.
type videoFactsV3 struct {
	subtitle, remuxSubtitle, hlsSubtitle               SubtitlePolicyResultV3
	originalSubtitleOK, remuxSubtitleOK, hlsSubtitleOK bool
	quality                                            QualityResultV3
	videoOK                                            bool
	high10                                             *AppliedQuirkV3
	rangeOK, clientManagedRange, dv8BaseOK             bool
	dv8BaseRange                                       string
	audioOK, originalAudioOK, noAudioTrack             bool
	audioClaims                                        AudioClaimsV3
	stripProgressive, stripHLS                         bool
	clientDV81, clientHDR10, serverDV81                bool
	base                                               PlanV3
	// mustAdapt means no route that copies the video can work.
	mustAdapt, remuxActive, transcodeStage, subtitleForced bool
	transcodeReason                                        string
	remuxAudio                                             remuxAudioFactsV3
}

// remuxAudioFactsV3 is the one audio decision of each copy-remux delivery.
type remuxAudioFactsV3 struct {
	progressiveCodecConverts, progressiveChannelLimited, progressiveConverts bool
	hlsCodecConverts, hlsChannelLimited, hlsTranscode, hlsQuirkConverts      bool
	// hlsConverts includes the device quirk that converts audio HLS could copy.
	hlsConverts    bool
	hlsAACChannels int
	firefoxAAC     *AppliedQuirkV3
	hlsQuirk       *AppliedQuirkV3
}

func (f *videoFactsV3) originalRangeOK() bool {
	return f.rangeOK || f.clientManagedRange || f.dv8BaseOK
}

func (f *videoFactsV3) anyCopyRange() bool {
	return f.originalRangeOK() || f.stripProgressive || f.stripHLS || f.serverDV81 || f.clientDV81 || f.clientHDR10
}

func (p *candidatePlannerV3) planVideo() PlannerResultV3 {
	if refusal := p.videoFacts(); refusal != nil {
		return *refusal
	}
	exhausted := terminalPlannerResultV3("adaptation_unavailable", "No validated playback route is available for this source and output route.", false)
	if p.video.transcodeStage {
		exhausted = terminalPlannerResultV3("adaptation_exhausted", "All compatible playback recipes have already failed for this output route.", false)
	}
	return p.selectCandidateV3(p.videoCandidates(), p.enterVideoRoute, p.evaluateVideo, exhausted)
}

// videoFacts fills p.video, or refuses when no candidate could exist. The
// lazy lookups it makes are the ones the facts themselves depend on.
func (p *candidatePlannerV3) videoFacts() *PlannerResultV3 {
	in, file, source, f := p.input, p.file, p.source, &p.video
	request := in.Request
	policy := func(class string) SubtitlePolicyResultV3 {
		return ResolveSubtitlePolicyV3(file, request, in.Settings.TranscodeEnabled, class, in.AdditionalSubtitles, in.AudioTrackIndex, in.SubtitleTrackFacts)
	}
	f.subtitle, f.remuxSubtitle, f.hlsSubtitle = policy(DeliveryClassOriginalHTTPV3), policy(DeliveryClassProgressiveV3), policy(DeliveryClassHLSV3)
	if f.subtitle.Terminal != nil && f.remuxSubtitle.Terminal != nil && f.hlsSubtitle.Terminal != nil {
		return &PlannerResultV3{Terminal: f.subtitle.Terminal, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
	}
	f.originalSubtitleOK = f.subtitle.Terminal == nil && !f.subtitle.RequiresBurn
	f.remuxSubtitleOK = f.remuxSubtitle.Terminal == nil && !f.remuxSubtitle.RequiresBurn
	f.hlsSubtitleOK = f.hlsSubtitle.Terminal == nil && !f.hlsSubtitle.RequiresBurn
	f.quality = ResolveQualityPolicyV3(request, source)
	if p.capRequiresEncode {
		f.quality.RequiresTranscode, f.quality.PreservesSource = true, false
		capKbps := optionalValueV3(request.BandwidthCapKbps)
		if f.quality.BitrateKbps <= 0 {
			f.quality.BitrateKbps = capKbps
		} else {
			f.quality.BitrateKbps = min(f.quality.BitrateKbps, capKbps)
		}
	}
	videoOK, evidenceInsufficient := videoEligibleV3(source, request)
	if quirk, ok := high10DecodeOverrideV3(source, request); !videoOK && ok {
		videoOK, f.high10 = true, quirk
	}
	f.videoOK = videoOK
	rangeOK, videoClaims := outputRangeEligibleV3(source, request)
	f.rangeOK = rangeOK
	f.clientManagedRange = clientManagesOriginalDynamicRangeV3(source, request)
	f.dv8BaseOK, f.dv8BaseRange = clientDV8BaseLayerFallbackV3(source, request)
	audioOK, passthrough, audioClaims := audioEligibilityV3(source, request)
	f.originalAudioOK = audioSelectionUsesContainerDefaultV3(file, in.AudioTrackIndex) || clientSelectsOriginalAudioTrackV3(request)
	f.noAudioTrack = source.AudioCodec == "" && len(file.AudioTracks) == 0
	if !audioOK && f.noAudioTrack {
		audioOK, audioClaims.Reason = true, audioReasonNoTrackV3
	}
	f.audioOK, f.audioClaims = audioOK, audioClaims
	stripUnsupported := p.dolbyVisionFacts()
	if stripUnsupported && !f.originalRangeOK() && !f.serverDV81 && !f.clientDV81 && !f.clientHDR10 &&
		(!resolveToneMapRecipeV3(in, source, nil).ok || !videoTranscodeExecutableV3(in, source)) {
		return refusalV3(terminalPlannerResultV3(TerminalDVConversionUnsupportedV3,
			"This source's Dolby Vision metadata cannot be removed cleanly, and this device cannot play the source as it is.", false))
	}
	p.videoBase(videoClaims, passthrough, stripUnsupported)
	if !routeVideoMetadataCompleteV3(source) {
		return refusalV3(terminalPlannerResultV3("source_metadata_incomplete", "The source is missing video metadata required for a validated playback route.", true))
	}
	if !videoOK && evidenceInsufficient {
		f.base.DegradationWarnings = append(f.base.DegradationWarnings, DegradationWarningV3{
			Code:    EvidenceInsufficientForDirectV3,
			Message: "The client's capability evidence tier cannot validate this stream for a direct route; an adapted route is used instead.",
		})
	}
	p.routeFacts(evidenceInsufficient)
	return nil
}

// dolbyVisionFacts settles which Dolby Vision conversions are open. Each
// delivery asks its own executor pool, and the RPU probe runs only once one
// of them offers the strip. It reports a strip the source itself cannot take.
func (p *candidatePlannerV3) dolbyVisionFacts() bool {
	in, source, f := p.input, p.source, &p.video
	request := in.Request
	hlsOK := deliveryAvailableV3(request, DeliveryClassHLSV3)
	if source.DynamicRange == DynamicRangeDolbyVisionV3 && clientSupportsHDR10V3(request, source) &&
		(source.DVProfile == 7 || source.DVProfile == 8 && source.DVBLCompatID == 1) {
		if deliveryAvailableV3(request, DeliveryClassProgressiveV3) {
			f.stripProgressive = canStripDolbyVisionToHDR10V3(source, request, in.progressiveRemuxRegistry())
		}
		if hlsOK {
			f.stripHLS = canStripDolbyVisionToHDR10V3(source, request, in.hlsRemuxRegistry())
		}
	}
	stripUnsupported := (f.stripProgressive || f.stripHLS) && !in.dvRPUStrippable()
	if stripUnsupported {
		f.stripProgressive, f.stripHLS = false, false
	}
	f.clientDV81 = canClientTransformDV7ToDV81V3(source, request)
	f.serverDV81 = hlsOK && canServerConvertDV7ToDV81V3(source, p.file, request, in.hlsRemuxRegistry)
	f.clientHDR10 = canClientTransformDV7ToHDR10V3(source, request)
	return stripUnsupported
}

func (p *candidatePlannerV3) videoBase(videoClaims VideoClaimsV3, passthrough, stripUnsupported bool) {
	f := &p.video
	selected := f.subtitle
	if selected.Terminal != nil {
		selected = f.remuxSubtitle
		if selected.Terminal != nil {
			selected = f.hlsSubtitle
		}
	}
	base := p.newPlanV3(selected)
	base.Claims = ValidationClaimsV3{Video: videoClaims, Audio: f.audioClaims, Subtitles: f.subtitle.Claims}
	base.Claims.Audio.Passthrough = passthrough
	base.Subtitle = f.subtitle.Decision
	base.AvailableQualities = availableQualitiesV3(p.input, p.source)
	base.Subtitle.Inventory = BuildSubtitleInventoryV3(p.file, p.input.AdditionalSubtitles)
	if f.subtitle.Degraded {
		base.DegradationWarnings = append(base.DegradationWarnings, SubtitleTrackUnavailableWarningV3())
	}
	if p.source.DynamicRange == DynamicRangeHDRUnknownV3 && (f.rangeOK || f.clientManagedRange) {
		base.DegradationWarnings = append(base.DegradationWarnings, DegradationWarningV3{
			Code:    "hdr_range_assumed_hdr10",
			Message: "The source is flagged HDR without precise range metadata; playback treats it as HDR10 unless the client resolves a more precise presentation.",
		})
	}
	if stripUnsupported {
		base.DegradationWarnings = append(base.DegradationWarnings, DegradationWarningV3{
			Code:    "dolby_vision_strip_unsupported_by_source",
			Message: "This source's Dolby Vision metadata cannot be removed cleanly, so the validated HDR10 route is unavailable for it.",
		})
	}
	f.base = base
}

// routeFacts decides which routes get candidates. An automatic quality
// reduction that only an encode could deliver, and no encode can run, is
// dropped with a warning so the source still plays.
func (p *candidatePlannerV3) routeFacts(evidenceInsufficient bool) {
	in, source, f := p.input, p.source, &p.video
	anySubtitle := f.originalSubtitleOK || f.remuxSubtitleOK || f.hlsSubtitleOK
	if in.ServerBitrateCapKbps == 0 && f.quality.RequiresTranscode && !f.quality.ExplicitRung && anySubtitle && f.videoOK &&
		f.anyCopyRange() && !videoTranscodeExecutableV3(in, source) && !lowerVersionServesQualitiesV3(in) {
		warnings := append(f.quality.Warnings, DegradationWarningV3{
			Code:    "quality_reduction_unavailable",
			Message: "Reduced-quality transcoding is unavailable for this source; it is delivered at original quality.",
		})
		f.quality = originalQualityResultV3(source)
		f.quality.Warnings = warnings
	}
	f.base.DegradationWarnings = append(f.base.DegradationWarnings, f.quality.Warnings...)
	f.mustAdapt = f.quality.RequiresTranscode || !f.videoOK || !f.anyCopyRange() || !anySubtitle
	remuxRangeOK := f.rangeOK && source.DVProfile != 7 || f.serverDV81
	f.remuxActive = !f.mustAdapt && !source.VideoCopyUnsafe && (remuxRangeOK || f.stripProgressive || f.stripHLS) &&
		(f.remuxSubtitleOK || f.hlsSubtitleOK)
	if f.remuxActive {
		p.remuxAudioFacts()
	}
	p.transcodeStageFacts(evidenceInsufficient, anySubtitle)
}

// transcodeStageFacts decides whether encode candidates exist and the reason
// their plans name.
func (p *candidatePlannerV3) transcodeStageFacts(evidenceInsufficient, anySubtitle bool) {
	f := &p.video
	switch {
	case f.mustAdapt:
		f.transcodeStage = true
		if !f.quality.RequiresTranscode && !f.videoOK && evidenceInsufficient {
			f.transcodeReason = EvidenceInsufficientForDirectV3
		}
		f.subtitleForced = !f.quality.RequiresTranscode && f.videoOK && f.anyCopyRange() && !anySubtitle
	case f.remuxActive && p.input.ServerBitrateCapKbps > 0 && (f.remuxAudio.progressiveConverts || f.remuxAudio.hlsConverts):
		// Special case: an audio-converting remux under a server cap may not
		// fit it, so the encode follows with its own reason and even without
		// HLS, where its refusal is client_hls_unsupported.
		f.transcodeStage = true
	default:
		f.transcodeStage = deliveryAvailableV3(p.input.Request, DeliveryClassHLSV3)
		f.transcodeReason = transcodeReasonCopyExhaustedV3
	}
}

func (p *candidatePlannerV3) remuxAudioFacts() {
	request, source, f := p.input.Request, p.source, &p.video
	a := &f.remuxAudio
	a.progressiveChannelLimited = !f.noAudioTrack && deliveryExceedsMaxChannelsV3(request, DeliveryClassProgressiveV3, source.AudioCodec, source.AudioChannels)
	a.hlsChannelLimited = !f.noAudioTrack && deliveryExceedsMaxChannelsV3(request, DeliveryClassHLSV3, source.AudioCodec, source.AudioChannels)
	progressiveCodecOK := f.noAudioTrack || deliverySupportsAudioClaimV3(request, DeliveryClassProgressiveV3, source.AudioCodec, f.audioClaims, f.audioOK)
	hlsCodecOK := f.noAudioTrack || hlsNativeAudioCodecV3(source.AudioCodec) &&
		deliverySupportsAudioClaimV3(request, DeliveryClassHLSV3, source.AudioCodec, f.audioClaims, f.audioOK)
	// Matroska AAC timestamps break Firefox once copied into MP4, so both
	// remux deliveries re-encode it.
	a.firefoxAAC, _ = firefoxMatroskaAACTimingQuirkV3(source, request)
	a.progressiveCodecConverts = !progressiveCodecOK || a.firefoxAAC != nil
	a.progressiveConverts = a.progressiveCodecConverts || a.progressiveChannelLimited
	a.hlsCodecConverts = !hlsCodecOK || a.firefoxAAC != nil
	a.hlsTranscode = a.hlsCodecConverts || a.hlsChannelLimited
	a.hlsQuirk, _ = hlsEAC3AudioCorrectionV3(source, request)
	a.hlsQuirkConverts = a.hlsQuirk != nil && !a.hlsCodecConverts
	a.hlsConverts = a.hlsTranscode || a.hlsQuirk != nil
	switch {
	case a.hlsQuirkConverts:
		a.hlsAACChannels = aacOutputChannelsV3(request, DeliveryClassHLSV3, source.AudioChannels, false)
	case a.hlsTranscode:
		a.hlsAACChannels = aacOutputChannelsV3(request, DeliveryClassHLSV3, source.AudioChannels, a.hlsChannelLimited || !hlsNativeAudioCodecV3(source.AudioCodec))
	}
}

// videoCandidates lists every candidate the facts allow, then sorts them.
func (p *candidatePlannerV3) videoCandidates() []candidateV3 {
	f, source, request := &p.video, p.source, p.input.Request
	var candidates []candidateV3
	add := func(route candidateRouteV3, delivery string, converts bool, video candidateVideoV3, subtitles ...candidateSubtitleV3) {
		for _, subtitle := range subtitles {
			candidates = append(candidates, candidateV3{route: route, delivery: delivery, video: video, convertsAudio: converts, subtitle: subtitle})
		}
	}
	hlsSubtitles := []candidateSubtitleV3{candidateOnDemandRenditionsV3, candidatePipelineRenditionV3, candidateSidecarV3}
	originalOK := !f.mustAdapt && f.quality.PreservesSource && f.videoOK && f.audioOK && f.originalAudioOK && f.originalSubtitleOK &&
		containsFoldV3(request.Capabilities.Containers, source.Container) && deliveryAvailableV3(request, DeliveryClassOriginalHTTPV3)
	var originals []candidateVideoV3
	switch {
	case !originalOK:
	case source.DVProfile == 7:
		// A dual-layer Profile 7 file plays original only when the client
		// converts or presents it itself.
		originals = appendIfV3(originals, f.clientDV81, candidateClientDV81V3)
		originals = appendIfV3(originals, f.clientHDR10, candidateClientHDR10V3)
		originals = appendIfV3(originals, f.clientManagedRange, candidateClientManagedV3)
	default:
		originals = appendIfV3(originals, f.rangeOK, candidateCopyV3)
		originals = appendIfV3(originals, f.dv8BaseOK, candidateDV8BaseLayerV3)
		originals = appendIfV3(originals, !f.rangeOK && !f.dv8BaseOK && f.clientManagedRange, candidateClientManagedV3)
	}
	for _, video := range originals {
		add(candidateOriginalV3, DeliveryClassOriginalHTTPV3, false, video, candidateSidecarV3)
	}
	if f.remuxActive {
		strip := f.stripProgressive || f.stripHLS
		var remuxes []candidateVideoV3
		remuxes = appendIfV3(remuxes, f.serverDV81, candidateServerDV81V3)
		// A remux copies only the base layer, so Profile 7 is never preserved.
		remuxes = appendIfV3(remuxes, source.DVProfile != 7 && (f.rangeOK || !strip), candidateCopyV3)
		remuxes = appendIfV3(remuxes, strip, candidateServerStripV3)
		for _, video := range remuxes {
			if video != candidateServerDV81V3 {
				add(candidateRemuxV3, DeliveryClassProgressiveV3, f.remuxAudio.progressiveConverts, video, candidateSidecarV3)
			}
			add(candidateRemuxV3, DeliveryClassHLSV3, f.remuxAudio.hlsConverts, video, hlsSubtitles...)
		}
	}
	if f.transcodeStage {
		add(candidateTranscodeV3, DeliveryClassHLSV3, true, candidateEncodeHEVCV3, hlsSubtitles...)
		add(candidateTranscodeV3, DeliveryClassHLSV3, true, candidateEncodeH264V3, hlsSubtitles...)
	}
	sortCandidatesV3(candidates)
	return candidates
}

func appendIfV3(videos []candidateVideoV3, ok bool, video candidateVideoV3) []candidateVideoV3 {
	if ok {
		return append(videos, video)
	}
	return videos
}

// enterVideoRoute holds the refusals that end planning between routes.
func (p *candidatePlannerV3) enterVideoRoute(route candidateRouteV3) *PlannerResultV3 {
	f, a := &p.video, &p.video.remuxAudio
	if !f.remuxActive {
		return nil
	}
	switch route {
	case candidateRemuxV3:
		// Special case: when both remux deliveries must convert the codec and
		// neither pool offers AAC, the missing toolchain is the refusal, even
		// though an encode might run.
		if a.progressiveCodecConverts && a.hlsCodecConverts && !p.progressiveAACOK() &&
			(!deliveryAvailableV3(p.input.Request, DeliveryClassHLSV3) || !p.input.hlsRemuxRegistry().Available(TransformationAudioToAACV3)) {
			return refusalV3(terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, "The required validated AAC conversion toolchain is unavailable.", true))
		}
	case candidateTranscodeV3:
		// Special case: likewise when an HLS copy remux lacked only AAC.
		if p.hlsAACMissing != "" {
			return refusalV3(terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, p.hlsAACMissing, true))
		}
	}
	return nil
}

func (p *candidatePlannerV3) progressiveAACOK() bool {
	return p.video.remuxAudio.progressiveConverts && deliveryAvailableV3(p.input.Request, DeliveryClassProgressiveV3) &&
		p.input.progressiveRemuxRegistry().Available(TransformationAudioToAACV3)
}

func (p *candidatePlannerV3) evaluateVideo(c candidateV3) (PlannerResultV3, bool, *PlannerResultV3) {
	var result PlannerResultV3
	var ok bool
	switch {
	case c.route == candidateTranscodeV3:
		return p.transcodeCandidate(c)
	case c.route == candidateOriginalV3:
		result, ok = p.originalCandidate(c)
	case c.delivery == DeliveryClassHLSV3:
		result, ok = p.hlsRemuxCandidate(c)
	default:
		result, ok = p.progressiveCandidate(c)
	}
	return result, ok, nil
}
