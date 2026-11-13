package playback

// Wire tokens a plan carries that tests also name.
const (
	decisionReasonValidatedOriginalV3 = "validated_original_playback"
	degradationDVRemovedV3            = "dolby_vision_removed"
	dvBaseLayerReasonV3               = "base_layer_compatible_hevc"
	degradationDVBaseLayerOnlyV3      = "dolby_vision_base_layer_only"
	transcodeReasonCopyExhaustedV3    = "copy_routes_exhausted"
	audioReasonNoTrackV3              = "no_audio_track"
)

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
	hlsConverts          bool
	hlsAACChannels       int
	firefoxAAC, hlsQuirk *AppliedQuirkV3
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
		exhausted = exhaustedV3()
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
		// The quality resolver sizes from the video track rate, which can
		// undercount the total a source-preserving route sends.
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
	// Video-only media has no audio to adapt.
	f.noAudioTrack = source.AudioCodec == "" && len(file.AudioTracks) == 0
	if !audioOK && f.noAudioTrack {
		audioOK, audioClaims.Reason = true, audioReasonNoTrackV3
	}
	f.audioOK, f.audioClaims = audioOK, audioClaims
	stripUnsupported := p.dolbyVisionFacts()
	// Name the real cause, a source whose Dolby Vision metadata cannot be
	// removed, when nothing else can play it and no tone-map encode can run.
	if stripUnsupported && !f.originalRangeOK() && !f.serverDV81 && !f.clientDV81 && !f.clientHDR10 &&
		(!resolveToneMapRecipeV3(in, source, nil).ok || !encodeCanRunV3(in, source)) {
		return refusalV3(terminalPlannerResultV3(TerminalDVConversionUnsupportedV3,
			"This source's Dolby Vision metadata cannot be removed cleanly, and this device cannot play the source as it is.", false))
	}
	p.videoBase(videoClaims, passthrough, stripUnsupported)
	if !routeVideoMetadataCompleteV3(source) {
		return refusalV3(terminalPlannerResultV3("source_metadata_incomplete", "The source is missing video metadata required for a validated playback route.", true))
	}
	if !videoOK && evidenceInsufficient {
		f.base.DegradationWarnings = append(f.base.DegradationWarnings, DegradationWarningV3{Code: EvidenceInsufficientForDirectV3, Message: "The client's capability evidence tier cannot validate this stream for a direct route; an adapted route is used instead."})
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
	// Every delivery's policy addresses the same selected track.
	selected := f.subtitle
	for _, next := range []SubtitlePolicyResultV3{f.remuxSubtitle, f.hlsSubtitle} {
		if selected.Terminal != nil {
			selected = next
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
		base.DegradationWarnings = append(base.DegradationWarnings, DegradationWarningV3{Code: "hdr_range_assumed_hdr10", Message: "The source is flagged HDR without precise range metadata; playback treats it as HDR10 unless the client resolves a more precise presentation."})
	}
	if stripUnsupported {
		base.DegradationWarnings = append(base.DegradationWarnings, DegradationWarningV3{Code: "dolby_vision_strip_unsupported_by_source", Message: "This source's Dolby Vision metadata cannot be removed cleanly, so the validated HDR10 route is unavailable for it."})
	}
	f.base = base
}

// routeFacts decides which routes get candidates. An automatic quality
// reduction that only an encode could deliver, when no encode can run and no
// lower version serves it, is dropped with a warning so the source plays.
func (p *candidatePlannerV3) routeFacts(evidenceInsufficient bool) {
	in, source, f := p.input, p.source, &p.video
	anySubtitle := f.originalSubtitleOK || f.remuxSubtitleOK || f.hlsSubtitleOK
	if in.ServerBitrateCapKbps == 0 && f.quality.RequiresTranscode && !f.quality.ExplicitRung && anySubtitle && f.videoOK &&
		f.anyCopyRange() && !encodeCanRunV3(in, source) && !lowerVersionServesQualitiesV3(in) {
		warnings := append(f.quality.Warnings, DegradationWarningV3{Code: "quality_reduction_unavailable", Message: "Reduced-quality transcoding is unavailable for this source; it is delivered at original quality."})
		f.quality = originalQualityResultV3(source)
		f.quality.Warnings = warnings
	}
	f.base.DegradationWarnings = append(f.base.DegradationWarnings, f.quality.Warnings...)
	f.mustAdapt = f.quality.RequiresTranscode || !f.videoOK || !f.anyCopyRange() || !anySubtitle
	// A remux maps only the base layer, so Profile 7 is never copied as is.
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
	in, f := p.input, &p.video
	switch {
	case f.mustAdapt:
		f.transcodeStage = true
		if !f.quality.RequiresTranscode && !f.videoOK && evidenceInsufficient {
			f.transcodeReason = EvidenceInsufficientForDirectV3
		}
		// Only the burn-in forces the encode: a refusal names the subtitle.
		f.subtitleForced = !f.quality.RequiresTranscode && f.videoOK && f.anyCopyRange() && !anySubtitle
	case f.remuxActive && in.ServerBitrateCapKbps > 0 && (f.remuxAudio.progressiveConverts || f.remuxAudio.hlsConverts):
		// Special case: an audio-converting remux may not fit a server cap, so
		// the encode follows with its own reason, even without HLS.
		f.transcodeStage = true
	default:
		f.transcodeStage = deliveryAvailableV3(in.Request, DeliveryClassHLSV3)
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
		// Non-native codecs keep their surround, as many channels as a
		// ceiling allows.
		a.hlsAACChannels = aacOutputChannelsV3(request, DeliveryClassHLSV3, source.AudioChannels, a.hlsChannelLimited || !hlsNativeAudioCodecV3(source.AudioCodec))
	}
}

// videoCandidates lists every candidate the facts allow, then sorts them.
func (p *candidatePlannerV3) videoCandidates() []candidateV3 {
	f, source, request := &p.video, p.source, p.input.Request
	var candidates []candidateV3
	add := func(c candidateV3, ok bool, subtitles ...candidateSubtitleV3) {
		for _, subtitle := range subtitles {
			if c.subtitle = subtitle; ok {
				candidates = append(candidates, c)
			}
		}
	}
	sidecar := []candidateSubtitleV3{candidateSidecarV3}
	hls := []candidateSubtitleV3{candidateOnDemandRenditionsV3, candidatePipelineRenditionV3, candidateSidecarV3}
	if !f.mustAdapt && f.quality.PreservesSource && f.videoOK && f.audioOK && f.originalAudioOK && f.originalSubtitleOK &&
		containsFoldV3(request.Capabilities.Containers, source.Container) && deliveryAvailableV3(request, DeliveryClassOriginalHTTPV3) {
		original := func(video candidateVideoV3, ok bool) {
			add(candidateV3{route: candidateOriginalV3, video: video}, ok, sidecar...)
		}
		dv7 := source.DVProfile == 7
		// A dual-layer Profile 7 file plays original only when the client
		// converts or presents it itself.
		original(candidateClientDV81V3, dv7 && f.clientDV81)
		original(candidateClientHDR10V3, dv7 && f.clientHDR10)
		original(candidateCopyV3, !dv7 && f.rangeOK)
		original(candidateDV8BaseLayerV3, !dv7 && f.dv8BaseOK)
		original(candidateClientManagedV3, f.clientManagedRange && (dv7 || !f.rangeOK && !f.dv8BaseOK))
	}
	if f.remuxActive {
		strip := f.stripProgressive || f.stripHLS
		a := &f.remuxAudio
		for video, ok := range map[candidateVideoV3]bool{
			candidateServerDV81V3:  f.serverDV81,
			candidateCopyV3:        source.DVProfile != 7 && (f.rangeOK || !strip),
			candidateServerStripV3: strip,
		} {
			// The server's Profile 8.1 conversion rewrites fMP4 HLS only.
			add(candidateV3{route: candidateRemuxV3, video: video, audio: audioOpV3(a.progressiveConverts), delivery: candidateProgressiveV3}, ok && video != candidateServerDV81V3, sidecar...)
			add(candidateV3{route: candidateRemuxV3, video: video, audio: audioOpV3(a.hlsConverts), delivery: candidateHLSV3}, ok, hls...)
		}
	}
	for _, video := range []candidateVideoV3{candidateEncodeHEVCV3, candidateEncodeH264V3} {
		add(candidateV3{route: candidateTranscodeV3, video: video, audio: candidateAudioConvertV3, delivery: candidateHLSV3}, f.transcodeStage, hls...)
	}
	sortCandidatesV3(candidates)
	return candidates
}

// enterVideoRoute holds the refusals that end planning between routes.
func (p *candidatePlannerV3) enterVideoRoute(route candidateRouteV3) *PlannerResultV3 {
	a := &p.video.remuxAudio
	switch {
	case !p.video.remuxActive:
	case route == candidateRemuxV3:
		// Special case: when both remux deliveries must convert the codec and
		// neither pool offers AAC, the missing toolchain is the refusal, even
		// though an encode might run.
		if a.progressiveCodecConverts && a.hlsCodecConverts && !p.progressiveAACOK() &&
			(!deliveryAvailableV3(p.input.Request, DeliveryClassHLSV3) || !p.input.hlsRemuxRegistry().Available(TransformationAudioToAACV3)) {
			return refusalV3(terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, "The required validated AAC conversion toolchain is unavailable.", true))
		}
	case route == candidateTranscodeV3 && p.hlsAACMissing != "":
		// Special case: likewise when an HLS copy remux lacked only AAC.
		return refusalV3(terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, p.hlsAACMissing, true))
	}
	return nil
}

func (p *candidatePlannerV3) progressiveAACOK() bool {
	return p.video.remuxAudio.progressiveConverts && deliveryAvailableV3(p.input.Request, DeliveryClassProgressiveV3) &&
		p.input.progressiveRemuxRegistry().Available(TransformationAudioToAACV3)
}

func (p *candidatePlannerV3) evaluateVideo(c candidateV3) (*PlannerResultV3, *PlannerResultV3) {
	switch {
	case c.route == candidateTranscodeV3:
		return p.transcodeCandidate(c)
	case c.route == candidateOriginalV3:
		return p.originalCandidate(c), nil
	case c.delivery == candidateHLSV3:
		return p.hlsRemuxCandidate(c), nil
	}
	return p.progressiveCandidate(c), nil
}
