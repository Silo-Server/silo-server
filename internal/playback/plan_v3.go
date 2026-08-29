package playback

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

type PlannerSettingsV3 struct {
	TranscodeEnabled       bool
	Allow4KTranscode       bool
	HardwareToneMapEnabled bool
	SoftwareToneMapEnabled bool
}

const (
	TerminalMessage4KTranscodeDisabledV3   = "A lower-resolution source is required because 4K transcoding is disabled."
	containerMP4V3                         = "mp4"
	mimeVideoMP4V3                         = "video/mp4"
	degradationAudioConvertedV3            = "audio_converted"
	audioCodecAACV3                        = "aac"
	serverAudioAdaptationReasonV3          = "server_audio_adaptation"
	decisionReasonAudioAdaptationV3        = "audio_adaptation"
	hlsAudioAdaptationReasonV3             = "hls_audio_adaptation"
	decisionReasonContainerNormalizationV3 = "container_normalization"
	audioLayoutMonoV3                      = "mono"
	audioLayoutStereoV3                    = "stereo"
	audioLayoutSurround51V3                = "5.1"
)

type PlannerInputV3 struct {
	Request         StartRequestV3
	RequestedFile   *models.MediaFile
	EffectiveFile   *models.MediaFile
	AudioTrackIndex int
	Settings        PlannerSettingsV3
	// Registry holds the transformations the local binary can execute.
	// Progressive remux routes always gate on it: they run in this process.
	Registry *TransformationRegistryV3
	// HLSRegistry optionally widens transformation availability for HLS
	// deliveries, which can execute on pooled transcode nodes as well as
	// locally. Nil means HLS routes gate on Registry alone. It is a lazy
	// producer because building the widened registry can touch the network
	// (node capability fetches): the planner only invokes it when a route
	// decision genuinely depends on node capabilities, so direct-play and
	// other source-preserving starts never pay for it. Producers must
	// return a superset of Registry (local ∪ node capabilities) and should
	// memoize; the transport layer re-validates whichever executor is
	// actually selected.
	HLSRegistry func() *TransformationRegistryV3
	// ToneMapCapabilities and HLSToneMapCapabilities mirror Registry and
	// HLSRegistry for executor variants of hdr_to_sdr_tonemap. The public plan
	// names one stable transformation; these internal capabilities select a
	// validated hardware or software implementation without exposing that
	// deployment policy to clients.
	ToneMapCapabilities    tonemap.Capabilities
	HLSToneMapCapabilities func() tonemap.Capabilities
	// DVRPUStrippable reports whether this particular source survives the
	// Dolby Vision RPU strip. The registries answer whether the executor
	// carries the transformation; this answers whether the file does, which
	// no capability probe can. Nil means "assume it does", preserving the
	// pre-probe behaviour for callers that cannot run one (the shadow
	// planner, tests). Lazy for the same reason as HLSRegistry: it shells out
	// to ffmpeg, so it is consulted only once every cheap eligibility gate
	// has already passed and a strip route is genuinely on the table.
	DVRPUStrippable     func() bool
	Now                 time.Time
	AttemptedKeys       []string
	AdditionalSubtitles []SubtitleInventoryEntryV3
}

// SourceExecutionMetadataV3 is the immutable source probe snapshot used to
// reopen a frozen playback recipe without consuming later catalog drift.
type SourceExecutionMetadataV3 struct {
	VideoCodec                 string
	VideoProfile               string
	VideoBitDepth              int
	SoftwareVideoDecode        bool
	DurationSeconds            float64
	ToneMapSourceKind          tonemap.SourceKind
	ToneMapPreflightRequired   bool
	ToneMapSourceRevision      tonemap.SourceRevision
	ToneMapDVConfigPresent     bool
	ToneMapDVBLCompatIDPresent bool
	ToneMapDVBLPresent         bool
	ToneMapDVRPUPresent        bool
}

// dvRPUStrippable resolves the per-source strip verdict, defaulting to true
// when no probe is wired in.
func (input PlannerInputV3) dvRPUStrippable() bool {
	return input.DVRPUStrippable == nil || input.DVRPUStrippable()
}

// hlsRegistry resolves the registry HLS deliveries gate on: the widened
// local∪node registry when provided, otherwise the local one. Callers must
// keep it behind short-circuits so transformation-free routes never force
// the lazy producer to run.
func (input PlannerInputV3) hlsRegistry() *TransformationRegistryV3 {
	if input.HLSRegistry != nil {
		if widened := input.HLSRegistry(); widened != nil {
			return widened
		}
	}
	return input.Registry
}

type PlannerResultV3 struct {
	Plan             *PlanV3
	Terminal         *TerminalV3
	PlayMethod       PlayMethod
	TranscodeAudio   bool
	TargetVideoCodec string
	TargetAudioCodec string
	// SourceAudioChannels freezes the selected input track's channel count for
	// source-sensitive encode recipes such as multichannel-to-stereo downmixing.
	SourceAudioChannels int
	// TargetAudioChannels caps the transcode's re-encoded channel count;
	// 0 keeps the historical stereo downmix.
	TargetAudioChannels         int
	TargetAudioBitrateKbps      int
	TargetResolution            string
	TargetBitrateKbps           int
	SubtitleTrackIndex          int
	SubtitleTransportTrackIndex int
	SubtitleBurnIn              bool
	SubtitleCodec               string
	// DownloadedSubtitleID comes from the same inventory snapshot used for
	// planning. Freezing must not re-list a mutable ordinal inventory after the
	// route has already been accepted.
	DownloadedSubtitleID int
	// FrozenSourceMetadata is set only when a durable executable recipe is
	// thawed for a seek reanchor. Transport construction must then use this
	// captured source snapshot instead of a freshly probed media row.
	FrozenSourceMetadata     *SourceExecutionMetadataV3
	ToneMapPolicy            tonemap.Policy
	ToneMapMode              tonemap.Mode
	ToneMapSourceKind        tonemap.SourceKind
	ToneMapRecipeVersion     string
	ToneMapPreflightRequired bool
	ToneMapSourceRevision    tonemap.SourceRevision
}

// hlsToneMapCapabilities resolves the lazy pooled inventory when present and
// otherwise uses the eagerly supplied local capability set.
func (input PlannerInputV3) hlsToneMapCapabilities() tonemap.Capabilities {
	if input.HLSToneMapCapabilities != nil {
		if capabilities := input.HLSToneMapCapabilities(); capabilities != nil {
			return capabilities
		}
	}
	return input.ToneMapCapabilities
}

// PlanPlaybackV3 chooses a playable protocol-v3 route from source and client facts.
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
	// A source without any video track is audio-only (audiobooks, future
	// music): the video, HDR, and subtitle-burn gates below have nothing to
	// gate, and requiring complete video metadata would terminal a perfectly
	// playable file. It gets its own reduced route family instead.
	if file.IsAudioOnly() {
		return planAudioOnlyV3(input, file, source)
	}
	// Subtitle renderability is delivery-specific, so every candidate route is
	// validated against the capabilities of the delivery class that would
	// execute it. The original_http delivery remains the canonical policy for
	// source-preserving routes and for the up-front terminal decision.
	subtitle := ResolveSubtitlePolicyV3(file, input.Request, input.Settings.TranscodeEnabled, DeliveryClassOriginalHTTPV3, input.AdditionalSubtitles)
	if subtitle.Terminal != nil {
		return PlannerResultV3{Terminal: subtitle.Terminal, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
	}
	remuxSubtitle := ResolveSubtitlePolicyV3(file, input.Request, input.Settings.TranscodeEnabled, DeliveryClassProgressiveV3, input.AdditionalSubtitles)
	hlsSubtitle := ResolveSubtitlePolicyV3(file, input.Request, input.Settings.TranscodeEnabled, DeliveryClassHLSV3, input.AdditionalSubtitles)
	// A remux route cannot burn subtitles, so it is only viable when its own
	// delivery can present the selected subtitle without one.
	remuxSubtitleOK := remuxSubtitle.Terminal == nil && !remuxSubtitle.RequiresBurn
	hlsRemuxSubtitleOK := hlsSubtitle.Terminal == nil && !hlsSubtitle.RequiresBurn
	quality := ResolveQualityPolicyV3(input.Request, source)
	videoOK, videoEvidenceInsufficient := videoEligibleV3(source, input.Request)
	var high10Quirk *AppliedQuirkV3
	if !videoOK {
		if quirk, ok := high10DecodeOverrideV3(source, input.Request); ok {
			videoOK = true
			high10Quirk = quirk
		}
	}
	rangeOK, videoClaims := outputRangeEligibleV3(source, input.Request)
	clientManagedRange := clientManagesOriginalDynamicRangeV3(source, input.Request)
	originalRangeOK := rangeOK || clientManagedRange
	audioOK, passthrough, audioClaims := audioEligibilityV3(source, input.Request)
	originalAudioSelectionOK := audioSelectionUsesContainerDefaultV3(file, input.AudioTrackIndex) ||
		clientSelectsOriginalAudioTrackV3(input.Request)
	noAudioTrack := source.AudioCodec == "" && (file == nil || len(file.AudioTracks) == 0)
	if !audioOK && noAudioTrack {
		// Video-only media has no audio stream to adapt: treating the absence
		// as an unsupported codec would force a pointless AAC conversion — or
		// a terminal when conversion is unavailable — on a playable file. An
		// audio track whose codec merely failed to probe keeps the codec
		// gate: converting unknown audio is safer than copying it.
		audioOK = true
		audioClaims.Reason = "no_audio_track"
	}
	containerOK := containsFoldV3(input.Request.Capabilities.Containers, source.Container)
	hlsDeliveryOK := deliveryAvailableV3(input.Request, DeliveryClassHLSV3)
	// DV strip eligibility is split by executor pool: a progressive remux
	// executes on this process's ffmpeg, while an HLS remux may run on a
	// pooled transcode node advertising the transformation. Node capability
	// only counts when the client can actually run an HLS delivery, and the
	// widened registry is consulted lazily so non-DV sources never touch it.
	dvStripEligibleLocal := canStripDolbyVisionToHDR10V3(source, input.Request, input.Registry)
	dvStripEligible := dvStripEligibleLocal
	if !dvStripEligible && hlsDeliveryOK && source.DynamicRange == DynamicRangeDolbyVisionV3 {
		dvStripEligible = canStripDolbyVisionToHDR10V3(source, input.Request, input.hlsRegistry())
	}
	// A source whose RPU ffmpeg cannot parse must lose the strip here rather
	// than at the transport, so that the plan's HDR10 promise, the durable
	// session's RemuxDVMode and every restart derived from it stay consistent
	// with what the pipeline can actually produce. Ordered last: the probe
	// only runs once an executor has been found for a strip this client wants.
	dvStripUnsupportedBySource := false
	if dvStripEligible && !input.dvRPUStrippable() {
		dvStripUnsupportedBySource = true
		dvStripEligible = false
		dvStripEligibleLocal = false
	}
	clientDV81Eligible := canClientTransformDV7ToDV81V3(source, input.Request)
	clientHDR10Eligible := canClientTransformDV7ToHDR10V3(source, input.Request)
	// With the server strip gone, a client that cannot take the source range
	// (either natively or by managing it itself) and cannot run its own DV
	// transformation is out of source-preserving routes. Terminate only when no
	// enabled tone-map route remains either: a validated HDR-to-SDR executor can
	// still decode the compatible base layer without preserving the Dolby Vision
	// metadata. Without one, every remaining branch funnels into
	// planVideoTranscodeV3's hdr_transcode_unsupported, so terminate here
	// instead — the client is then told the actual cause, a source whose Dolby
	// Vision metadata cannot be removed, rather than a generic HDR message that
	// sends the user looking for a missing encoder.
	if dvStripUnsupportedBySource && !originalRangeOK && !clientDV81Eligible && !clientHDR10Eligible {
		_, _, _, _, toneMapEligible := toneMapRecipeV3(input, source)
		if !toneMapEligible || !videoTranscodeExecutableV3(input, source) {
			return terminalPlannerResultV3(TerminalDVConversionUnsupportedV3,
				"This source's Dolby Vision metadata cannot be removed cleanly, and this device cannot play the source as it is.", false)
		}
	}

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
	base.AvailableQualities = availableQualitiesV3(input, source)
	base.Subtitle.Inventory = BuildSubtitleInventoryV3(file, input.AdditionalSubtitles)
	base.Claims.Audio.Passthrough = passthrough
	if source.DynamicRange == DynamicRangeHDRUnknownV3 && (rangeOK || clientManagedRange) {
		base.DegradationWarnings = append(base.DegradationWarnings, DegradationWarningV3{
			Code:    "hdr_range_assumed_hdr10",
			Message: "The source is flagged HDR without precise range metadata; playback treats it as HDR10 unless the client resolves a more precise presentation.",
		})
	}
	if dvStripUnsupportedBySource {
		// Say why the HDR10 route this client is capable of was not taken;
		// otherwise the fallback looks like an unexplained quality drop.
		base.DegradationWarnings = append(base.DegradationWarnings, DegradationWarningV3{
			Code:    "dolby_vision_strip_unsupported_by_source",
			Message: "This source's Dolby Vision metadata cannot be removed cleanly, so the validated HDR10 route is unavailable for it.",
		})
	}
	if !routeVideoMetadataCompleteV3(source) {
		return terminalPlannerResultV3("source_metadata_incomplete", "The source is missing video metadata required for a validated playback route.", true)
	}
	if !videoOK && videoEvidenceInsufficient {
		// The client's flat codec lists claim this stream, but its evidence
		// tier could not validate it for a direct route. Distinguish that from
		// a device that genuinely cannot play the stream so lower-tier clients
		// see an actionable degradation instead of a mystery transcode.
		base.DegradationWarnings = append(base.DegradationWarnings, DegradationWarningV3{
			Code:    EvidenceInsufficientForDirectV3,
			Message: "The client's capability evidence tier cannot validate this stream for a direct route; an adapted route is used instead.",
		})
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
		(originalRangeOK || dvStripEligible || clientDV81Eligible || clientHDR10Eligible) &&
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
		(!originalRangeOK && !dvStripEligible && !clientDV81Eligible && !clientHDR10Eligible) ||
		(subtitle.RequiresBurn && !remuxSubtitleOK && !hlsRemuxSubtitleOK) {
		reasonOverride := ""
		if !quality.RequiresTranscode && !videoOK && videoEvidenceInsufficient {
			// The only reason this route adapts is the evidence tier, not a
			// negative device fact; name that in the decision and in any
			// resulting terminal.
			reasonOverride = EvidenceInsufficientForDirectV3
		}
		// True when the burn requirement is the sole disjunct that fired: every
		// other route condition still permits a source-preserving delivery.
		subtitleForcedAdaptation := !quality.RequiresTranscode && videoOK &&
			(originalRangeOK || dvStripEligible || clientDV81Eligible || clientHDR10Eligible) &&
			subtitle.RequiresBurn && !remuxSubtitleOK && !hlsRemuxSubtitleOK
		return planVideoTranscodeV3(input, base, source, quality, hlsSubtitle, reasonOverride, subtitleForcedAdaptation)
	}

	// Profile 7 is normalized on the client against the original range-capable
	// source. A decoder profile/max-instance claim alone is not proof of native
	// dual-layer output, so the default Android route mirrors Silo Apple: P8.1
	// base-layer Dolby Vision first, then same-file HDR10.
	if source.DVProfile == 7 && quality.PreservesSource && videoOK && containerOK && audioOK && originalAudioSelectionOK && !subtitle.RequiresBurn {
		if clientDV81Eligible {
			plan := base
			plan.Delivery = DeliveryOriginalHTTPV3
			plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: source.Container, MIMEType: MimeFromExtension(file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
			plan.DecisionReason = "client_dv7_to_dv81"
			plan.EffectiveRecipe.DynamicRange = DynamicRangeDolbyVisionV3
			plan.Claims.Video = VideoClaimsV3{DolbyVision: true, DolbyVisionReason: "client_profile7_to_profile81"}
			plan.Transformations = append(plan.Transformations, TransformationV3{
				Name: ClientDV7ToDV81V3, Executor: ExecutorClientV3, RecipeVersion: ClientDVTransformVersionV3,
				ValidatedClaims: []string{"profile7_rpu_converted_to_profile81", "hdr10_base_layer_preserved", "enhancement_layer_discarded"},
			})
			plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{
				Code:    "dolby_vision_enhancement_layer_discarded",
				Message: "Dolby Vision Profile 7 is played as Profile 8.1 base-layer Dolby Vision; enhancement-layer pixel data is discarded.",
			})
			finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID, input.Request.ClientPlaybackContext.Output.OutputContextID)
			if deliverySupportsPlanV3(input.Request, DeliveryClassOriginalHTTPV3, plan) && !planAttemptedV3(plan, input.Request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
				return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: subtitle.SelectedIndex, SubtitleTransportTrackIndex: subtitle.TransportIndex, SubtitleCodec: subtitle.Codec, DownloadedSubtitleID: subtitle.DownloadedSubtitleID}
			}
		}
		if clientHDR10Eligible {
			plan := base
			plan.Delivery = DeliveryOriginalHTTPV3
			plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: source.Container, MIMEType: MimeFromExtension(file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
			plan.DecisionReason = "client_dv7_to_hdr10"
			plan.EffectiveRecipe.DynamicRange = DynamicRangeHDR10V3
			plan.Claims.Video = VideoClaimsV3{HDR10: true}
			plan.Transformations = append(plan.Transformations, TransformationV3{
				Name: ClientDV7ToHDR10V3, Executor: ExecutorClientV3, RecipeVersion: ClientDVTransformVersionV3,
				ValidatedClaims: DV7ToHDR10ClaimsV3(),
			})
			plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{
				Code:    "dolby_vision_removed",
				Message: "Dolby Vision Profile 7 is played from the same 4K file as its HDR10 base layer.",
			})
			finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID, input.Request.ClientPlaybackContext.Output.OutputContextID)
			if deliverySupportsPlanV3(input.Request, DeliveryClassOriginalHTTPV3, plan) && !planAttemptedV3(plan, input.Request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
				return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: subtitle.SelectedIndex, SubtitleTransportTrackIndex: subtitle.TransportIndex, SubtitleCodec: subtitle.Codec, DownloadedSubtitleID: subtitle.DownloadedSubtitleID}
			}
		}
		if clientManagedRange {
			plan := base
			plan.Delivery = DeliveryOriginalHTTPV3
			plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: source.Container, MIMEType: MimeFromExtension(file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
			plan.DecisionReason = decisionReasonClientManagedDynamicRangeV3
			finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID, input.Request.ClientPlaybackContext.Output.OutputContextID)
			if deliverySupportsPlanV3(input.Request, DeliveryClassOriginalHTTPV3, plan) && !planAttemptedV3(plan, input.Request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
				return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: subtitle.SelectedIndex, SubtitleTransportTrackIndex: subtitle.TransportIndex, SubtitleCodec: subtitle.Codec, DownloadedSubtitleID: subtitle.DownloadedSubtitleID}
			}
		}
	}

	if source.DVProfile != 7 && deliveryAvailableV3(input.Request, DeliveryClassOriginalHTTPV3) && containerOK && videoOK && originalRangeOK && audioOK && originalAudioSelectionOK && quality.PreservesSource && !subtitle.RequiresBurn {
		plan := base
		plan.Delivery = DeliveryOriginalHTTPV3
		plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: source.Container, MIMEType: MimeFromExtension(file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
		plan.DecisionReason = "validated_original_playback"
		if !rangeOK && clientManagedRange {
			plan.DecisionReason = decisionReasonClientManagedDynamicRangeV3
		}
		applyCopiedVideoQuirksV3(&plan, source, input.Request, high10Quirk)
		finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID, input.Request.ClientPlaybackContext.Output.OutputContextID)
		if deliverySupportsPlanV3(input.Request, DeliveryClassOriginalHTTPV3, plan) && !planAttemptedV3(plan, input.Request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
			return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: subtitle.SelectedIndex, SubtitleTransportTrackIndex: subtitle.TransportIndex, SubtitleCodec: subtitle.Codec, DownloadedSubtitleID: subtitle.DownloadedSubtitleID}
		}
	}

	// A progressive remux maps only the base-layer video stream, so dual-layer
	// Profile 7 can never ship as native Dolby Vision here regardless of the
	// client's decoder claims; the validated HDR10 strip is the only eligible
	// P7 remux recipe.
	remuxRangeOK := rangeOK && source.DVProfile != 7
	// A copy-unsafe source (H.264 with conflicting in-band PPS) must not take a
	// video stream-copy route: the avc1/fMP4 segment would desync strict
	// decoders. Skipping the remux branch drops through to the HLS transcode.
	if videoOK && !source.VideoCopyUnsafe && (remuxRangeOK || dvStripEligible) && (remuxSubtitleOK || hlsRemuxSubtitleOK) {
		progressiveAudioOK := noAudioTrack || deliverySupportsAudioClaimV3(input.Request, DeliveryClassProgressiveV3, source.AudioCodec, audioClaims, audioOK)
		hlsAudioOK := noAudioTrack || hlsNativeAudioCodecV3(source.AudioCodec) &&
			deliverySupportsAudioClaimV3(input.Request, DeliveryClassHLSV3, source.AudioCodec, audioClaims, audioOK)
		progressiveTranscodeAudio := !progressiveAudioOK
		hlsTranscodeAudio := !hlsAudioOK
		hlsAudioQuirk, hlsAudioQuirkOK := hlsEAC3AudioCorrectionV3(source, input.Request)
		localAudioConvertOK := input.Registry.Available(TransformationAudioToAACV3)
		if progressiveTranscodeAudio && hlsTranscodeAudio {
			// The HLS remux branch below can offload the conversion to a
			// pooled node, but only for clients that can run an HLS
			// delivery: a progressive-only client must keep this terminal
			// (its retryable semantics included) rather than fall through
			// to a generic adaptation_unavailable for a route it can never
			// use. Short-circuit order keeps locally-capable planning from
			// consulting node capabilities at all.
			audioConvertOK := localAudioConvertOK ||
				hlsDeliveryOK && input.hlsRegistry().Available(TransformationAudioToAACV3)
			if !audioConvertOK {
				return terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, "The required validated AAC conversion toolchain is unavailable.", true)
			}
		}
		dvStrip := dvStripEligible && (source.DVProfile == 7 || !rangeOK)
		remuxBase := base
		if dvStrip {
			remuxBase.Transformations = append(remuxBase.Transformations, TransformationV3{Name: TransformationServerDV7HDR10V3, Executor: ExecutorServerV3, RecipeVersion: "1", ValidatedClaims: DV7ToHDR10ClaimsV3()})
			remuxBase.EffectiveRecipe.DynamicRange = DynamicRangeHDR10V3
			remuxBase.Claims.Video = VideoClaimsV3{HDR10: true}
			remuxBase.DegradationWarnings = append(remuxBase.DegradationWarnings, DegradationWarningV3{Code: "dolby_vision_removed", Message: "Dolby Vision metadata is removed and the validated HDR10 base layer is preserved."})
		}

		progressivePlan := cloneRemuxPlanCandidateV3(remuxBase)
		progressivePlan.Delivery = DeliveryRemuxProgressiveV3
		progressivePlan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: containerMP4V3, MIMEType: mimeVideoMP4V3, Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
		progressivePlan.DecisionReason = decisionReasonContainerNormalizationV3
		progressiveAudioChannels := 0
		if progressiveTranscodeAudio && localAudioConvertOK {
			progressiveAudioChannels = aacOutputChannelsV3(input.Request, DeliveryClassProgressiveV3, source.AudioChannels, false)
			progressivePlan.EffectiveRecipe.AudioCodec = audioCodecAACV3
			progressivePlan.EffectiveRecipe.AudioChannels = intPointerV3(progressiveAudioChannels)
			progressivePlan.EffectiveRecipe.AudioLayout = audioLayoutForChannelsV3(progressiveAudioChannels)
			progressivePlan.Claims.Audio = AudioClaimsV3{Codec: audioCodecAACV3, Reason: serverAudioAdaptationReasonV3}
			progressivePlan.Transformations = append(progressivePlan.Transformations, TransformationV3{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{ClaimAudioDecodeV3}})
			progressivePlan.DegradationWarnings = append(progressivePlan.DegradationWarnings, DegradationWarningV3{Code: degradationAudioConvertedV3, Message: fmt.Sprintf("The selected audio track is converted to AAC %s.", audioLayoutForChannelsV3(progressiveAudioChannels))})
			progressivePlan.DecisionReason = decisionReasonAudioAdaptationV3
		}
		if !dvStrip {
			applyCopiedVideoQuirksV3(&progressivePlan, source, input.Request, high10Quirk)
		}
		// The progressive remux executes on this process's ffmpeg, so its
		// server transformations must be locally available; when only pooled
		// nodes carry them, the HLS remux below ships the same recipe on a
		// node-offloadable delivery instead.
		progressiveExecutable := (!progressiveTranscodeAudio || localAudioConvertOK) && (!dvStrip || dvStripEligibleLocal)
		tryProgressive := func() (PlannerResultV3, bool) {
			if !remuxSubtitleOK || !progressiveExecutable {
				return PlannerResultV3{}, false
			}
			candidate := cloneRemuxPlanCandidateV3(progressivePlan)
			applySubtitleDecisionV3(&candidate, remuxSubtitle.Decision)
			candidate.Claims.Subtitles = remuxSubtitle.Claims
			finalizePlanIdentityV3(&candidate, input.Request.PlaybackAttemptID, input.Request.ClientPlaybackContext.Output.OutputContextID)
			if deliverySupportsPlanV3(input.Request, DeliveryClassProgressiveV3, candidate) && !planAttemptedV3(candidate, input.Request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
				return PlannerResultV3{Plan: &candidate, PlayMethod: PlayRemux, TranscodeAudio: progressiveTranscodeAudio, TargetAudioCodec: candidate.EffectiveRecipe.AudioCodec, SourceAudioChannels: stereoDownmixSourceChannelsV3(source.AudioChannels, progressiveAudioChannels, progressiveTranscodeAudio), TargetAudioChannels: progressiveAudioChannels, SubtitleTrackIndex: remuxSubtitle.SelectedIndex, SubtitleTransportTrackIndex: remuxSubtitle.TransportIndex, SubtitleCodec: remuxSubtitle.Codec, DownloadedSubtitleID: remuxSubtitle.DownloadedSubtitleID}, true
			}
			return PlannerResultV3{}, false
		}
		progressiveFirst := !progressiveTranscodeAudio || hlsTranscodeAudio || hlsAudioQuirkOK
		if progressiveFirst {
			if result, ok := tryProgressive(); ok {
				return result
			}
		}
		if deliveryAvailableV3(input.Request, DeliveryClassHLSV3) && hlsRemuxSubtitleOK {
			plan := cloneRemuxPlanCandidateV3(remuxBase)
			plan.Delivery = DeliveryRemuxHLSV3
			plan.Stream = StreamV3{Protocol: StreamHLSV3, Container: "hls", MIMEType: "application/vnd.apple.mpegurl", Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
			hlsAudioChannels := 0
			if hlsTranscodeAudio {
				if !input.hlsRegistry().Available(TransformationAudioToAACV3) {
					return terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, "The HLS route requires the validated AAC conversion toolchain.", true)
				}
				// HLS packaging cannot safely copy non-native codecs such as
				// DTS, TrueHD, or Opus. Preserve surround when adapting those
				// codecs; a native codec rejected by the scoped client claim
				// keeps the normal compatibility downmix policy.
				hlsAudioChannels = aacOutputChannelsV3(input.Request, DeliveryClassHLSV3, source.AudioChannels, !hlsNativeAudioCodecV3(source.AudioCodec))
				plan.EffectiveRecipe.AudioCodec = "aac"
				plan.EffectiveRecipe.AudioChannels = intPointerV3(hlsAudioChannels)
				plan.EffectiveRecipe.AudioLayout = audioLayoutForChannelsV3(hlsAudioChannels)
				plan.Claims.Audio = AudioClaimsV3{Codec: "aac", Reason: hlsAudioAdaptationReasonV3}
				plan.Transformations = append(plan.Transformations, TransformationV3{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{ClaimAudioDecodeV3}})
				plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationAudioConvertedV3, Message: "The selected audio track is converted to AAC for HLS delivery."})
			}
			if hlsAudioQuirkOK && !hlsTranscodeAudio {
				if !input.hlsRegistry().Available(TransformationAudioToAACV3) {
					return terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, "The device-specific HLS route requires the validated AAC conversion toolchain.", true)
				}
				hlsTranscodeAudio = true
				hlsAudioChannels = aacOutputChannelsV3(input.Request, DeliveryClassHLSV3, source.AudioChannels, false)
				plan.EffectiveRecipe.AudioCodec = "aac"
				plan.EffectiveRecipe.AudioChannels = intPointerV3(hlsAudioChannels)
				plan.EffectiveRecipe.AudioLayout = audioLayoutForChannelsV3(hlsAudioChannels)
				plan.Claims.Audio = AudioClaimsV3{Codec: "aac", Reason: "device_hls_audio_adaptation"}
				plan.Transformations = append(plan.Transformations, TransformationV3{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{ClaimAudioDecodeV3}})
				plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationAudioConvertedV3, Message: fmt.Sprintf("The selected audio track is converted to AAC %s for this device's HLS route.", audioLayoutForChannelsV3(hlsAudioChannels))})
				appendAppliedQuirkV3(&plan, *hlsAudioQuirk, "")
			}
			if !dvStrip {
				applyCopiedVideoQuirksV3(&plan, source, input.Request, high10Quirk)
			}
			if hlsTranscodeAudio {
				plan.DecisionReason = hlsAudioAdaptationReasonV3
			} else {
				plan.DecisionReason = "hls_packaging_required"
			}
			applySubtitleDecisionV3(&plan, hlsSubtitle.Decision)
			plan.Claims.Subtitles = hlsSubtitle.Claims
			finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID, input.Request.ClientPlaybackContext.Output.OutputContextID)
			if deliverySupportsPlanV3(input.Request, DeliveryClassHLSV3, plan) && !planAttemptedV3(plan, input.Request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
				targetAudio := "copy"
				if hlsTranscodeAudio {
					targetAudio = "aac"
				}
				return PlannerResultV3{Plan: &plan, PlayMethod: PlayRemux, TranscodeAudio: hlsTranscodeAudio, TargetVideoCodec: "copy", TargetAudioCodec: targetAudio, SourceAudioChannels: stereoDownmixSourceChannelsV3(source.AudioChannels, hlsAudioChannels, hlsTranscodeAudio), TargetAudioChannels: hlsAudioChannels, TargetResolution: resolutionLabelV3(source.Height), TargetBitrateKbps: source.BitrateKbps, SubtitleTrackIndex: hlsSubtitle.SelectedIndex, SubtitleTransportTrackIndex: hlsSubtitle.TransportIndex, SubtitleCodec: hlsSubtitle.Codec, DownloadedSubtitleID: hlsSubtitle.DownloadedSubtitleID}
			}
		}
		if !progressiveFirst {
			if result, ok := tryProgressive(); ok {
				return result
			}
		}
	}
	if deliveryAvailableV3(input.Request, DeliveryClassHLSV3) {
		return planVideoTranscodeV3(input, base, source, quality, hlsSubtitle, "copy_routes_exhausted", false)
	}

	return terminalPlannerResultV3("adaptation_unavailable", "No validated playback route is available for this source and output route.", false)
}

// availableQualitiesV3 publishes the server ladder rungs a client could
// request for this source through a quality_change replan. The source rung is
// always present; transcode rungs are listed below the source resolution class,
// and at the same class when they reduce bitrate, only when the cheap transcode
// gates pass. Registry availability is deliberately not consulted: it can
// trigger lazy node-capability fetches, which source-preserving starts must
// never pay for, and a rung whose toolchain is missing degrades to a retryable
// terminal at replan time.
func availableQualitiesV3(input PlannerInputV3, source SourceDescriptorV3) []AvailableQualityV3 {
	return availableQualitiesForRouteV3(input, source)
}

// availableQualitiesForRouteV3 keeps source-preserving HDR planning lazy while
// still advertising the choices the configured policy allows. Selecting a
// lower HDR rung performs the executor capability lookup during the replan;
// building the menu itself never probes local or pooled executors.
func availableQualitiesForRouteV3(input PlannerInputV3, source SourceDescriptorV3) []AvailableQualityV3 {
	qualities := []AvailableQualityV3{{
		Label:           QualityOriginalV3,
		Height:          source.Height,
		BitrateKbps:     source.BitrateKbps,
		PreservesSource: true,
	}}
	if source.Height <= 0 {
		// Fixed rungs must sit strictly below a known source height; unknown
		// probe metadata cannot prove that any advertised rung avoids upscaling.
		return qualities
	}
	if !deliveryAvailableV3(input.Request, DeliveryClassHLSV3) || !input.Settings.TranscodeEnabled {
		return qualities
	}
	if is4KSourceV3(input.EffectiveFile, source) && !input.Settings.Allow4KTranscode {
		return qualities
	}
	if source.DynamicRange != "" && source.DynamicRange != DynamicRangeSDRV3 &&
		tonemap.NewPolicy(input.Settings.HardwareToneMapEnabled, input.Settings.SoftwareToneMapEnabled) == tonemap.PolicyNone {
		return qualities
	}
	for _, rung := range ladderRungsV3 {
		if !ladderRungPublishableV3(rung, source) {
			continue
		}
		qualities = append(qualities, AvailableQualityV3{
			Label:       rung.Label,
			DisplayName: rung.DisplayName,
			Height:      rung.Height,
			BitrateKbps: rung.BitrateKbps,
		})
	}
	return qualities
}

// audioAvailableQualitiesV3 is the audio-only menu: quality rungs are a video
// concept, so the only entry is the source itself.
func audioAvailableQualitiesV3(source SourceDescriptorV3) []AvailableQualityV3 {
	return []AvailableQualityV3{{Label: QualityOriginalV3, BitrateKbps: source.BitrateKbps, PreservesSource: true}}
}

const (
	// decisionReasonBandwidthCapV3 marks a plan whose recipe was constrained by
	// the request's bandwidth cap rather than by decode capability.
	decisionReasonBandwidthCapV3 = "quality_bandwidth_cap"

	// decisionReasonClientManagedDynamicRangeV3 marks an original-file plan
	// whose executor owns source-to-output dynamic-range presentation.
	decisionReasonClientManagedDynamicRangeV3 = "client_managed_dynamic_range"
)

// planAudioOnlyV3 plans sources without a video track (audiobooks, music).
// The route family is deliberately small: the original container over
// progressive HTTP when the client decodes the audio codec, otherwise a
// progressive AAC conversion remux. Video, HDR, quality-ladder, and
// subtitle-burn gates do not apply.
func planAudioOnlyV3(input PlannerInputV3, file *models.MediaFile, source SourceDescriptorV3) PlannerResultV3 {
	request := input.Request
	audioOK, _, audioClaims := audioEligibilityV3(source, request)
	originalAudioSelectionOK := audioSelectionUsesContainerDefaultV3(file, input.AudioTrackIndex) ||
		clientSelectsOriginalAudioTrackV3(request)
	bandwidthCapKbps := optionalValueV3(request.BandwidthCapKbps)
	bandwidthCapExceeded := bandwidthCapKbps > 0 && source.BitrateKbps > bandwidthCapKbps
	if source.AudioCodec == "" {
		// A file with neither a video track nor a probed audio codec has no
		// stream the planner can validate a route for.
		return terminalPlannerResultV3("source_metadata_incomplete", "The source is missing audio metadata required for a validated playback route.", true)
	}
	base := PlanV3{
		ProtocolVersion: ProtocolV3,
		ExpiresAt:       NewPlanExpiryV3(input.Now),
		SelectedTracks:  selectedTracksForPlanV3(file, input.AudioTrackIndex, SubtitlePolicyResultV3{SelectedIndex: -1, TransportIndex: -1}),
		EffectiveRecipe: recipeFromSourceV3(source),
		Claims:          ValidationClaimsV3{Audio: audioClaims},
		// Audio-only routes bypass every subtitle gate, so the inventory is
		// empty rather than a list of tracks no route on this plan can deliver.
		Subtitle:               SubtitleDecisionV3{Mode: SubtitleOffV3, Inventory: []SubtitleInventoryItemV3{}},
		Transformations:        []TransformationV3{},
		AppliedQuirks:          []AppliedQuirkV3{},
		RuntimeCorrections:     []string{},
		AvailableQualities:     audioAvailableQualitiesV3(source),
		DegradationWarnings:    []DegradationWarningV3{},
		RequestedMediaFileID:   input.RequestedFile.ID,
		EffectiveMediaFileID:   file.ID,
		Source:                 source,
		SubtitleFidelityPolicy: subtitlePolicyNameV3(request.SubtitleFidelityPreference),
		Timeline:               TimelineV3{SourceStartSeconds: floatOrZeroV3(request.StartPosition), PlayerStartSeconds: floatOrZeroV3(request.StartPosition), CanSeekAnywhere: true, SeekRestoration: "player_position"},
	}
	containerOK := containsFoldV3(request.Capabilities.Containers, source.Container)
	if audioOK && containerOK && !bandwidthCapExceeded && originalAudioSelectionOK && deliveryAvailableV3(request, DeliveryClassOriginalHTTPV3) {
		plan := base
		plan.Delivery = DeliveryOriginalHTTPV3
		plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: source.Container, MIMEType: MimeFromExtension(file.FilePath), Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
		plan.DecisionReason = "validated_original_playback"
		finalizePlanIdentityV3(&plan, request.PlaybackAttemptID, request.ClientPlaybackContext.Output.OutputContextID)
		if deliverySupportsPlanV3(request, DeliveryClassOriginalHTTPV3, plan) && !planAttemptedV3(plan, request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
			return PlannerResultV3{Plan: &plan, PlayMethod: PlayDirect, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
		}
	}
	if !deliveryAvailableV3(request, DeliveryClassProgressiveV3) {
		return terminalPlannerResultV3("adaptation_unavailable", "No validated playback route is available for this audio source.", false)
	}
	transcodeAudio := !audioOK || bandwidthCapExceeded
	if transcodeAudio && (input.Registry == nil || !input.Registry.Available(TransformationAudioToAACV3)) {
		return terminalPlannerResultV3(TerminalAudioConversionUnsupportedV3, "The required validated AAC conversion toolchain is unavailable.", true)
	}
	plan := base
	plan.Delivery = DeliveryRemuxProgressiveV3
	// The remux muxes an audio-only fMP4, so the plan must promise audio/mp4:
	// a declared-tier client probes the advertised MIME with isTypeSupported
	// before it will attach a source buffer, and "video/mp4" with no video
	// track is exactly the mismatch that makes that probe lie.
	plan.Stream = StreamV3{Protocol: StreamHTTPProgressiveV3, Container: containerMP4V3, MIMEType: AudioOnlyRemuxMIMEV3, Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
	plan.DecisionReason = decisionReasonContainerNormalizationV3
	targetAudioChannels := audioOnlyAACOutputChannelsV3(request, source)
	targetAudioBitrateKbps := 0
	if transcodeAudio {
		targetAudioBitrateKbps = audioOnlyAACBitrateKbpsV3(bandwidthCapKbps)
		applyAudioOnlyAACConversionV3(&plan, targetAudioChannels, targetAudioBitrateKbps, bandwidthCapExceeded)
	} else if !deliverySupportsPlanV3(request, DeliveryClassProgressiveV3, plan) && input.Registry != nil && input.Registry.Available(TransformationAudioToAACV3) {
		converted := plan
		targetAudioBitrateKbps = audioOnlyAACBitrateKbpsV3(bandwidthCapKbps)
		applyAudioOnlyAACConversionV3(&converted, targetAudioChannels, targetAudioBitrateKbps, false)
		if deliverySupportsPlanV3(request, DeliveryClassProgressiveV3, converted) {
			plan = converted
			transcodeAudio = true
		}
	}
	if !deliverySupportsPlanV3(request, DeliveryClassProgressiveV3, plan) {
		return terminalPlannerResultV3("adaptation_unavailable", "The progressive delivery cannot decode the planned audio recipe.", false)
	}
	finalizePlanIdentityV3(&plan, request.PlaybackAttemptID, request.ClientPlaybackContext.Output.OutputContextID)
	if planAttemptedV3(plan, request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
		return terminalPlannerResultV3("adaptation_exhausted", "All compatible playback recipes have already failed for this output route.", false)
	}
	if !transcodeAudio {
		targetAudioChannels = 0
		targetAudioBitrateKbps = 0
	}
	return PlannerResultV3{Plan: &plan, PlayMethod: PlayRemux, TranscodeAudio: transcodeAudio, TargetAudioCodec: plan.EffectiveRecipe.AudioCodec, SourceAudioChannels: stereoDownmixSourceChannelsV3(source.AudioChannels, targetAudioChannels, transcodeAudio), TargetAudioChannels: targetAudioChannels, TargetAudioBitrateKbps: targetAudioBitrateKbps, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
}

func stereoDownmixSourceChannelsV3(sourceChannels, targetChannels int, transcodeAudio bool) int {
	if !transcodeAudio || sourceChannels <= 2 || (targetChannels != 0 && targetChannels != 2) {
		return 0
	}
	return sourceChannels
}

func audioOnlyAACOutputChannelsV3(request StartRequestV3, source SourceDescriptorV3) int {
	return aacOutputChannelsV3(request, DeliveryClassProgressiveV3, source.AudioChannels, false)
}

// aacOutputChannelsV3 picks an encoder-supported AAC layout that does not
// exceed the active delivery's ceiling. FFmpeg's planned AAC recipes support
// mono, stereo, and 5.1; an intermediate ceiling therefore falls back from
// 5.1 to stereo rather than advertising an output the encoder never creates.
func aacOutputChannelsV3(request StartRequestV3, deliveryClass string, sourceChannels int, preserveSurround bool) int {
	channels := 2
	if sourceChannels == 1 {
		channels = 1
	} else if preserveSurround && sourceChannels >= 6 {
		channels = 6
	}
	capability, ok := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !ok || capability.MaxChannels == nil || *capability.MaxChannels <= 0 || channels <= *capability.MaxChannels {
		return channels
	}
	if *capability.MaxChannels == 1 {
		return 1
	}
	return 2
}

func audioLayoutForChannelsV3(channels int) string {
	switch channels {
	case 1:
		return audioLayoutMonoV3
	case 6:
		return audioLayoutSurround51V3
	default:
		return audioLayoutStereoV3
	}
}

func audioOnlyAACBitrateKbpsV3(bandwidthCapKbps int) int {
	const defaultAACBitrateKbps = 192
	if bandwidthCapKbps > 0 && bandwidthCapKbps < defaultAACBitrateKbps {
		return bandwidthCapKbps
	}
	return defaultAACBitrateKbps
}

func applyAudioOnlyAACConversionV3(plan *PlanV3, targetChannels, targetBitrateKbps int, bandwidthCapExceeded bool) {
	layout := audioLayoutStereoV3
	warning := "The selected audio track is converted to AAC stereo."
	if targetChannels == 1 {
		layout = audioLayoutMonoV3
		warning = "The selected audio track is converted to AAC mono."
	}
	plan.EffectiveRecipe.AudioCodec = "aac"
	plan.EffectiveRecipe.AudioChannels = intPointerV3(targetChannels)
	plan.EffectiveRecipe.AudioLayout = layout
	plan.EffectiveRecipe.BitrateKbps = intPointerV3(targetBitrateKbps)
	plan.Claims.Audio = AudioClaimsV3{Codec: "aac", Reason: "server_audio_adaptation"}
	plan.Transformations = append(plan.Transformations, TransformationV3{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{ClaimAudioDecodeV3}})
	plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: degradationAudioConvertedV3, Message: warning})
	plan.DecisionReason = "audio_adaptation"
	if bandwidthCapExceeded {
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: "bandwidth_cap_applied", Message: "Delivery quality is limited by the configured bandwidth cap."})
		plan.DecisionReason = decisionReasonBandwidthCapV3
	}
}

// planVideoTranscodeV3 always executes on the HLS delivery, so the caller must
// pass the subtitle policy resolved against DeliveryClassHLSV3.
//
// subtitleForcedAdaptation marks the case where only the subtitle burn
// requirement forced this adaptation. Deselecting the subtitle then restores
// playback, so a refusal must name the subtitle: an HDR or version reason
// sends the user chasing a problem that is not blocking them. Retryable
// infrastructure failures and the client-route terminal keep their own reasons.
func planVideoTranscodeV3(input PlannerInputV3, base PlanV3, source SourceDescriptorV3, quality QualityResultV3, subtitle SubtitlePolicyResultV3, reasonOverride string, subtitleForcedAdaptation bool) PlannerResultV3 {
	if !deliveryAvailableV3(input.Request, DeliveryClassHLSV3) {
		return terminalPlannerResultV3("client_hls_unsupported", "The client cannot execute the required HLS adaptation route.", false)
	}
	if subtitle.Terminal != nil {
		return PlannerResultV3{Terminal: subtitle.Terminal, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
	}
	if !input.Settings.TranscodeEnabled {
		if subtitleForcedAdaptation {
			return terminalPlannerResultV3("subtitle_conversion_unsupported", "The selected subtitle must be burned into the video, but transcoding is unavailable.", false)
		}
		return terminalPlannerResultV3("transcoding_disabled", "The source requires video adaptation, but transcoding is unavailable.", false)
	}
	var hlsRegistry *TransformationRegistryV3
	toneMapRecipe := resolvedToneMapRecipeV3{}
	if source.DynamicRange != "" && source.DynamicRange != DynamicRangeSDRV3 {
		toneMapRecipe = resolveToneMapRecipeV3(input, source, nil)
		hlsRegistry = toneMapRecipe.hlsRegistry
	}
	if source.DynamicRange != "" && source.DynamicRange != DynamicRangeSDRV3 && !toneMapRecipe.ok {
		if subtitleForcedAdaptation {
			return terminalPlannerResultV3("subtitle_conversion_unsupported", "The selected subtitle must be burned into the video, but this HDR source cannot be re-encoded.", false)
		}
		return terminalPlannerResultV3(TerminalHDRTranscodeUnsupportedV3, "This HDR source requires video encoding, but no validated HDR-preserving or tone-map recipe is installed.", false)
	}
	if is4KSourceV3(input.EffectiveFile, source) && !input.Settings.Allow4KTranscode {
		if subtitleForcedAdaptation {
			return terminalPlannerResultV3("subtitle_conversion_unsupported", "The selected subtitle must be burned into the video, but 4K transcoding is disabled.", false)
		}
		return terminalPlannerResultV3("no_alternate_version", TerminalMessage4KTranscodeDisabledV3, false)
	}
	if hlsRegistry == nil {
		hlsRegistry = input.hlsRegistry()
	}
	if hlsRegistry == nil || !hlsRegistry.Available(TransformationVideoToH264V3) || !hlsRegistry.Available(TransformationAudioToAACV3) {
		return terminalPlannerResultV3("conversion_tool_unavailable", "The required validated H.264/AAC conversion toolchain is unavailable.", true)
	}
	if source.DynamicRange != "" && source.DynamicRange != DynamicRangeSDRV3 {
		base.AvailableQualities = availableQualitiesForRouteV3(input, source)
	}
	plan := base
	plan.Delivery = DeliveryTranscodeHLSV3
	plan.Stream = StreamV3{Protocol: StreamHLSV3, Container: "hls", MIMEType: "application/vnd.apple.mpegurl", Headers: map[string]string{}, HeaderRefresh: HeaderRefreshNoneV3}
	plan.EffectiveRecipe.VideoCodec = "h264"
	plan.EffectiveRecipe.AudioCodec = "aac"
	plan.EffectiveRecipe.Width = intPointerV3(quality.Width)
	plan.EffectiveRecipe.Height = intPointerV3(quality.Height)
	plan.EffectiveRecipe.BitrateKbps = intPointerV3(quality.BitrateKbps)
	// Surround sources keep 5.1 through the AAC re-encode (universal Media3
	// decode); only stereo/mono sources — and unknown layouts — downmix to 2.0.
	targetAudioChannels := aacOutputChannelsV3(input.Request, DeliveryClassHLSV3, source.AudioChannels, true)
	audioLayout := audioLayoutForChannelsV3(targetAudioChannels)
	plan.EffectiveRecipe.AudioChannels = intPointerV3(targetAudioChannels)
	plan.EffectiveRecipe.AudioLayout = audioLayout
	plan.Transformations = append(plan.Transformations,
		TransformationV3{Name: TransformationVideoToH264V3, Executor: ExecutorServerV3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, ValidatedClaims: []string{ClaimH264DecodeV3}},
		TransformationV3{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{ClaimAudioDecodeV3}},
	)
	toneMapPolicy := toneMapRecipe.policy
	toneMapMode := toneMapRecipe.mode
	toneMapResolution := toneMapRecipe.resolution
	toneMapRevision := toneMapRecipe.revision
	toneMapOK := toneMapRecipe.ok
	toneMapSourceKind := toneMapResolution.Kind
	if source.DynamicRange != "" && source.DynamicRange != DynamicRangeSDRV3 {
		plan.Transformations = append(plan.Transformations, TransformationV3{
			Name: TransformationHDRToSDRToneMapV3, Executor: ExecutorServerV3,
			RecipeVersion:   TransformationHDRToSDRToneMapRecipeVersionV3,
			ValidatedClaims: []string{ClaimHDRMetadataRemovedV3, ClaimSDRBT709OutputV3},
		})
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{
			Code: DegradationWarningHDRToneMappedV3, Message: "HDR video is tone-mapped to SDR for this playback route.",
		})
	} else {
		// A non-tone-mapped recipe has no execution policy. Keeping PolicyNone
		// here would turn an ordinary SDR encode into a partial frozen recipe at
		// the FFmpeg execution boundary.
		toneMapPolicy = ""
		toneMapSourceKind = ""
		toneMapResolution = tonemap.SourceResolution{}
		toneMapRevision = tonemap.SourceRevision{}
	}
	plan.Claims.Audio = AudioClaimsV3{Codec: "aac", Passthrough: false, AtmosPreserved: false, Reason: "server_audio_adaptation"}
	applySubtitleDecisionV3(&plan, subtitle.Decision)
	plan.Claims.Subtitles = subtitle.Claims
	plan.DecisionReason = quality.Reason
	if reasonOverride != "" {
		plan.DecisionReason = reasonOverride
	}
	if subtitle.RequiresBurn {
		plan.DecisionReason = "subtitle_burn_in_required"
		plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: "subtitle_burn_in", Message: "The selected subtitle is rendered into the video."})
	}
	plan.EffectiveRecipe.DynamicRange = DynamicRangeSDRV3
	plan.Claims.Video = VideoClaimsV3{}
	if !deliverySupportsPlanV3(input.Request, DeliveryClassHLSV3, plan) {
		return terminalPlannerResultV3("adaptation_unavailable", "The HLS delivery cannot decode the planned transcode recipe.", false)
	}
	finalizePlanIdentityV3(&plan, input.Request.PlaybackAttemptID, input.Request.ClientPlaybackContext.Output.OutputContextID)
	if planAttemptedV3(plan, input.Request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
		return terminalPlannerResultV3("adaptation_exhausted", "All compatible playback recipes have already failed for this output route.", false)
	}
	return PlannerResultV3{Plan: &plan, PlayMethod: PlayTranscode, TranscodeAudio: true, TargetVideoCodec: "h264", TargetAudioCodec: "aac", SourceAudioChannels: stereoDownmixSourceChannelsV3(source.AudioChannels, targetAudioChannels, true), TargetAudioChannels: targetAudioChannels, TargetResolution: quality.Label, TargetBitrateKbps: quality.BitrateKbps, SubtitleTrackIndex: subtitle.SelectedIndex, SubtitleTransportTrackIndex: subtitle.TransportIndex, SubtitleBurnIn: subtitle.RequiresBurn, SubtitleCodec: subtitle.Codec, DownloadedSubtitleID: subtitle.DownloadedSubtitleID, ToneMapPolicy: toneMapPolicy, ToneMapMode: toneMapMode, ToneMapSourceKind: toneMapSourceKind, ToneMapRecipeVersion: toneMapRecipeVersionV3(toneMapOK), ToneMapPreflightRequired: toneMapResolution.PreflightRequired, ToneMapSourceRevision: toneMapRevision}
}

// applySubtitleDecisionV3 changes the delivery-specific subtitle policy without
// discarding the source inventory already frozen onto the base plan. Adapted
// routes still address the same combined ordinal space as original_http; only
// the rendering decision and claims vary by delivery capability.
func applySubtitleDecisionV3(plan *PlanV3, decision SubtitleDecisionV3) {
	if plan == nil {
		return
	}
	inventory := plan.Subtitle.Inventory
	plan.Subtitle = decision
	plan.Subtitle.Inventory = inventory
}

func canStripDolbyVisionToHDR10V3(source SourceDescriptorV3, request StartRequestV3, registry *TransformationRegistryV3) bool {
	if source.DynamicRange != DynamicRangeDolbyVisionV3 || !clientSupportsHDR10V3(request) || registry == nil || !registry.Available(TransformationServerDV7HDR10V3) {
		return false
	}
	// Profile 7 always carries an HDR10-viewable base layer. Profile 8 is
	// safe only when the DOVI compatibility id explicitly identifies HDR10.
	return source.DVProfile == 7 || source.DVProfile == 8 && source.DVBLCompatID == 1
}

func canClientTransformDV7ToDV81V3(source SourceDescriptorV3, request StartRequestV3) bool {
	if source.DynamicRange != DynamicRangeDolbyVisionV3 || source.DVProfile != 7 ||
		!clientTransformationAvailableV3(request, ClientDV7ToDV81V3, ClientDVTransformVersionV3) {
		return false
	}
	// The registered conversion recipe produces Profile 8.1.
	source.DVBLCompatID = 1
	return clientSupportsDVProfileV3(request, source, 8)
}

func canClientTransformDV7ToHDR10V3(source SourceDescriptorV3, request StartRequestV3) bool {
	return source.DynamicRange == DynamicRangeDolbyVisionV3 && source.DVProfile == 7 && clientSupportsHDR10V3(request) &&
		clientTransformationAvailableV3(request, ClientDV7ToHDR10V3, ClientDVTransformVersionV3)
}

func clientSupportsDVProfileV3(request StartRequestV3, source SourceDescriptorV3, profile int) bool {
	hdr := request.ClientPlaybackContext.Output.HDRDetails
	if hdr == nil {
		hdr = request.Capabilities.HDRDetails
	}
	source.DVProfile = profile
	return hdr != nil && hdrSupportsDolbyVisionSourceV3(*hdr, source)
}

func clientTransformationAvailableV3(request StartRequestV3, name, version string) bool {
	if !HasFeatureV3(request.ClientFeatures, FeatureClientVideoTransforms) {
		return false
	}
	delivery, ok := request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	if !ok || !delivery.Enabled || !delivery.SupportedOnDevice {
		return false
	}
	for _, transformation := range delivery.Transformations {
		if transformation.Executor == ExecutorClientV3 && transformation.Name == name && transformation.RecipeVersion == version {
			return true
		}
	}
	return false
}

// Is4KMediaFileV3 reports whether a catalog file is recorded as 4K or higher.
// Scanners and imports write the resolution label in several spellings, and the
// stored primary video track can carry dimensions that disagree with that label,
// so callers use both facts to stay aligned with the planner's 4K policy.
func Is4KMediaFileV3(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	labelWidth, labelHeight := dimensionsFromResolutionV3(file.Resolution)
	if labelWidth >= 3840 || labelHeight >= 2160 {
		return true
	}
	return len(file.VideoTracks) > 0 && (file.VideoTracks[0].Width >= 3840 || file.VideoTracks[0].Height >= 2160)
}

func is4KSourceV3(file *models.MediaFile, source SourceDescriptorV3) bool {
	return Is4KMediaFileV3(file) || source.Width >= 3840 || source.Height >= 2160
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
// quality mode: source-preserving delivery is degraded when the source bitrate
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
	if quality == QualityOriginalV3 && !capExceededBySource {
		result := originalQualityResultV3(source)
		result.Warnings = warnings
		return result
	}
	if rung, ok := ladderRungForLabelV3(quality); ok {
		return compoundRungQualityResultV3(rung, source, capKbps, warnings)
	}
	targetHeight := source.Height
	reason := "quality_auto_source"
	explicitRung := false
	capApplied := false
	switch {
	case quality == QualityOriginalV3:
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
		reason = decisionReasonBandwidthCapV3
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

// compoundRungQualityResultV3 resolves one explicit menu step. Its resolution
// class never changes under a bandwidth cap; only the bitrate is clamped. For
// cinema-aspect sources (for example 3840x1540 UHD), a same-class rung keeps
// the probed dimensions instead of upscaling to the class's nominal height.
func compoundRungQualityResultV3(rung ladderRungV3, source SourceDescriptorV3, capKbps int, warnings []DegradationWarningV3) QualityResultV3 {
	sourceClassHeight := sourceLadderHeightV3(source)
	height := rung.Height
	sameResolutionClass := sourceClassHeight == rung.Height
	if source.Height > 0 && (sameResolutionClass || source.Height < height) {
		height = source.Height
	}
	bitrate := rung.BitrateKbps
	if source.BitrateKbps > 0 && source.BitrateKbps < bitrate {
		bitrate = source.BitrateKbps
	}
	capApplied := capKbps > 0 && bitrate > capKbps
	if capApplied {
		bitrate = capKbps
	}
	reason := "quality_fixed_rung"
	if capApplied {
		reason = decisionReasonBandwidthCapV3
		warnings = append(warnings, DegradationWarningV3{Code: "bandwidth_cap_applied", Message: "Delivery quality is limited by the configured bandwidth cap."})
	}
	fitsRung := sourceClassHeight > 0 && sourceClassHeight <= rung.Height && source.BitrateKbps > 0 && source.BitrateKbps <= rung.BitrateKbps
	if fitsRung && !capApplied {
		return QualityResultV3{
			Label:           strconv.Itoa(source.Height) + "p",
			Width:           source.Width,
			Height:          source.Height,
			BitrateKbps:     source.BitrateKbps,
			PreservesSource: true,
			ExplicitRung:    true,
			Reason:          reason,
			Warnings:        warnings,
		}
	}
	width := 0
	if source.Width > 0 && source.Height > 0 {
		width = source.Width * height / source.Height
		width -= width % 2
	}
	if width == 0 {
		width, _ = dimensionsFromResolutionV3(resolutionLabelV3(height))
	}
	targetLabel := resolutionLabelV3(height)
	if sameResolutionClass && source.Height > 0 && source.Height != rung.Height {
		// The transcoder treats an unknown exact-height label as "do not scale",
		// which preserves the source's cinema crop while still applying the
		// selected bitrate and tone-map recipe.
		targetLabel = strconv.Itoa(source.Height) + "p"
	}
	return QualityResultV3{
		Label:             targetLabel,
		Width:             width,
		Height:            height,
		BitrateKbps:       bitrate,
		RequiresTranscode: true,
		ExplicitRung:      true,
		Reason:            reason,
		Warnings:          warnings,
	}
}

func originalQualityResultV3(source SourceDescriptorV3) QualityResultV3 {
	return QualityResultV3{Label: resolutionLabelV3(source.Height), Width: source.Width, Height: source.Height, BitrateKbps: source.BitrateKbps, PreservesSource: true, Reason: "quality_original"}
}

// hlsNativeAudioCodecV3 reports whether an audio codec can be stream-copied
// into an HLS delivery. The allowlist follows the HLS authoring spec (AAC,
// AC-3, E-AC-3, MP3); everything else — DTS, TrueHD, PCM, Opus — must be
// converted even when the client can decode it in a progressive container.
func hlsNativeAudioCodecV3(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "aac", "ac3", "eac3", "mp3":
		return true
	}
	return false
}

// hdrTranscodeUnavailableV3 mirrors planVideoTranscodeV3's terminal
// condition: no validated HDR-preserving or tone-map transcode recipe exists.
func hdrTranscodeUnavailableV3(input PlannerInputV3, source SourceDescriptorV3) bool {
	if source.DynamicRange == "" || source.DynamicRange == DynamicRangeSDRV3 {
		return false
	}
	return !resolveToneMapRecipeV3(input, source, nil).ok
}

type resolvedToneMapRecipeV3 struct {
	policy      tonemap.Policy
	mode        tonemap.Mode
	resolution  tonemap.SourceResolution
	revision    tonemap.SourceRevision
	hlsRegistry *TransformationRegistryV3
	ok          bool
}

// toneMapRecipeV3 freezes the policy, preferred validated executor, safe source
// resolution, and source revision required by an HDR video transcode plan.
func toneMapRecipeV3(input PlannerInputV3, source SourceDescriptorV3) (tonemap.Policy, tonemap.Mode, tonemap.SourceResolution, tonemap.SourceRevision, bool) {
	recipe := resolveToneMapRecipeV3(input, source, nil)
	return recipe.policy, recipe.mode, recipe.resolution, recipe.revision, recipe.ok
}

func resolveToneMapRecipeV3(input PlannerInputV3, source SourceDescriptorV3, hlsRegistry *TransformationRegistryV3) resolvedToneMapRecipeV3 {
	policy := tonemap.NewPolicy(input.Settings.HardwareToneMapEnabled, input.Settings.SoftwareToneMapEnabled)
	file := input.EffectiveFile
	if file == nil {
		file = input.RequestedFile
	}
	resolution := toneMapSourceResolutionV3(file, source)
	revision := tonemap.RevisionForFile(file)
	recipe := resolvedToneMapRecipeV3{policy: policy, resolution: resolution, revision: revision}
	if policy == tonemap.PolicyNone || resolution.Kind == "" {
		return recipe
	}
	if hlsRegistry == nil {
		hlsRegistry = input.hlsRegistry()
	}
	recipe.hlsRegistry = hlsRegistry
	if hlsRegistry == nil || !hlsRegistry.Available(TransformationHDRToSDRToneMapV3) {
		return recipe
	}
	recipe.mode = input.hlsToneMapCapabilities().PreferredMode(policy, resolution.Kind)
	recipe.ok = recipe.mode != ""
	return recipe
}

// toneMapSourceResolutionV3 combines protocol source facts with the scanner's
// richer primary-track metadata before resolving a safe base signal.
func toneMapSourceResolutionV3(file *models.MediaFile, source SourceDescriptorV3) tonemap.SourceResolution {
	metadata := tonemap.SourceMetadata{
		DynamicRange: source.DynamicRange,
		DVProfile:    source.DVProfile,
		DVBLCompatID: source.DVBLCompatID,
	}
	if file != nil && len(file.VideoTracks) > 0 {
		track := file.VideoTracks[0]
		metadata.DVConfigPresent = track.DVConfigPresent
		metadata.DVBLCompatIDPresent = track.DVBLCompatIDPresent
		metadata.DVBLPresent = track.DVBLPresent
		metadata.DVRPUPresent = track.DVRPUPresent
		metadata.ColorRange = track.ColorRange
		metadata.ColorPrimaries = track.ColorPrimaries
		metadata.ColorTransfer = track.ColorTransfer
		metadata.ColorSpace = track.ColorSpace
	}
	return tonemap.ResolveSource(metadata)
}

// toneMapRecipeVersionV3 includes the transformation recipe version only when
// tone mapping is part of the executable plan.
func toneMapRecipeVersionV3(enabled bool) string {
	if enabled {
		return TransformationHDRToSDRToneMapRecipeVersionV3
	}
	return ""
}

// videoTranscodeExecutableV3 mirrors planVideoTranscodeV3's terminal
// preconditions: it reports whether a validated video transcode of this
// source could actually run for this client and configuration.
func videoTranscodeExecutableV3(input PlannerInputV3, source SourceDescriptorV3) bool {
	if !deliveryAvailableV3(input.Request, DeliveryClassHLSV3) || !input.Settings.TranscodeEnabled {
		return false
	}
	if is4KSourceV3(input.EffectiveFile, source) && !input.Settings.Allow4KTranscode {
		return false
	}
	if hdrTranscodeUnavailableV3(input, source) {
		return false
	}
	return input.hlsRegistry().Available(TransformationVideoToH264V3) && input.hlsRegistry().Available(TransformationAudioToAACV3)
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

// audioSelectionUsesContainerDefaultV3 reports whether an untouched source
// stream can realize the selected audio track without client-side selection.
// Clients that explicitly claim client_selected_audio_track_v1 on
// original_http may select another stream after probing the complete source.
func audioSelectionUsesContainerDefaultV3(file *models.MediaFile, audioIndex int) bool {
	if file == nil || len(file.AudioTracks) == 0 {
		return true
	}
	defaultIndex := 0
	for index, track := range file.AudioTracks {
		if track.Default {
			defaultIndex = index
			break
		}
	}
	if audioIndex < 0 || audioIndex >= len(file.AudioTracks) {
		audioIndex = defaultIndex
	}
	return audioIndex == defaultIndex
}

func finalizePlanIdentityV3(plan *PlanV3, attemptID string, outputContextID string) {
	plan.PlanID = DeterministicPlanIDV3(attemptID, plan.RequestedMediaFileID, plan.EffectiveMediaFileID, *plan)
	plan.PlanAttemptKey = PlanAttemptKeyV3(*plan, outputContextID, nil)
}

// planAttemptedV3 compares FNV-hex attempt keys exactly after trimming
// whitespace; the keys are case-sensitive hashes, not free-form labels.
func planAttemptedV3(plan PlanV3, outputContextID string, attempted []string) bool {
	wanted := PlanAttemptKeyV3(plan, outputContextID, nil)
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

// ladderBitrateKbpsV3 retains the established bitrate for each plain
// resolution preference. Explicit menu steps use ladderRungsV3 below.
func ladderBitrateKbpsV3(height int) int {
	bitrates := map[int]int{480: 1_500, 720: 2_000, 1080: 6_000, 2160: 20_000}
	return bitrates[resolutionHeightV3(resolutionLabelV3(height))]
}

type ladderRungV3 struct {
	Label       string
	DisplayName string
	Height      int
	BitrateKbps int
}

// ladderRungsV3 is the canonical menu ladder in descending resolution and
// bitrate order. The medium values retain the existing plain-rung bitrates;
// their compound labels make the bitrate constraint explicit at same height.
var ladderRungsV3 = []ladderRungV3{
	{Label: QualityRung2160pHighV3, DisplayName: "4K High", Height: 2160, BitrateKbps: 40_000},
	{Label: QualityRung2160pMediumV3, DisplayName: "4K Medium", Height: 2160, BitrateKbps: 20_000},
	{Label: QualityRung2160pLowV3, DisplayName: "4K Low", Height: 2160, BitrateKbps: 10_000},
	{Label: QualityRung1080pHighV3, DisplayName: "1080p High", Height: 1080, BitrateKbps: 10_000},
	{Label: QualityRung1080pMediumV3, DisplayName: "1080p Medium", Height: 1080, BitrateKbps: 6_000},
	{Label: QualityRung1080pLowV3, DisplayName: "1080p Low", Height: 1080, BitrateKbps: 3_000},
	{Label: QualityRung720pHighV3, DisplayName: "720p High", Height: 720, BitrateKbps: 4_000},
	{Label: QualityRung720pMediumV3, DisplayName: "720p Medium", Height: 720, BitrateKbps: 2_000},
	{Label: QualityRung720pLowV3, DisplayName: "720p Low", Height: 720, BitrateKbps: 1_500},
	{Label: "480p", DisplayName: "480p", Height: 480, BitrateKbps: 1_500},
}

func ladderRungForLabelV3(label string) (ladderRungV3, bool) {
	normalized := strings.ToLower(strings.TrimSpace(label))
	for _, rung := range ladderRungsV3 {
		if rung.Label == normalized {
			return rung, true
		}
	}
	return ladderRungV3{}, false
}

// sourceLadderHeightV3 classifies cinema-aspect encodes by width as well as
// height. A 3840x1540 source is still a 4K source for menu purposes.
func sourceLadderHeightV3(source SourceDescriptorV3) int {
	switch {
	case source.Width >= 3840 || source.Height >= 2160:
		return 2160
	case source.Width >= 1920 || source.Height >= 1080:
		return 1080
	case source.Width >= 1280 || source.Height >= 720:
		return 720
	case source.Width > 0 || source.Height > 0:
		return 480
	default:
		return 0
	}
}

// ladderRungPublishableV3 avoids upscaling and pointless same-resolution
// re-encodes. Every lower resolution-class rung is useful; a same-class rung
// appears only when its bitrate is a real reduction from the source.
func ladderRungPublishableV3(rung ladderRungV3, source SourceDescriptorV3) bool {
	sourceHeight := sourceLadderHeightV3(source)
	if sourceHeight <= 0 {
		return false
	}
	if rung.Height < sourceHeight {
		return true
	}
	if rung.Height > sourceHeight || rung.Label == "480p" {
		return false
	}
	return source.BitrateKbps > 0 && rung.BitrateKbps < source.BitrateKbps
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

func deliveryAvailableV3(request StartRequestV3, deliveryClass string) bool {
	capability, ok := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !ok {
		return false
	}
	return capability.Enabled && capability.SupportedOnDevice
}

// cloneRemuxPlanCandidateV3 keeps progressive and HLS candidate mutations
// independent while preserving the common source and dynamic-range recipe.
func cloneRemuxPlanCandidateV3(plan PlanV3) PlanV3 {
	plan.Transformations = append([]TransformationV3{}, plan.Transformations...)
	plan.AppliedQuirks = append([]AppliedQuirkV3{}, plan.AppliedQuirks...)
	plan.RuntimeCorrections = append([]string{}, plan.RuntimeCorrections...)
	plan.DegradationWarnings = append([]DegradationWarningV3{}, plan.DegradationWarnings...)
	return plan
}

// deliverySupportsAudioClaimV3 narrows a device-wide audio claim to the active
// delivery when the client supplies scoped decode or passthrough lists. Empty
// scoped lists retain the legacy fallback because older clients use them to
// mean "unspecified."
func deliverySupportsAudioClaimV3(request StartRequestV3, deliveryClass, codec string, claim AudioClaimsV3, fallback bool) bool {
	capability, ok := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !ok || !capability.Enabled || !capability.SupportedOnDevice {
		return false
	}
	hasAudioConstraints := len(capability.AudioDecodeCodecs) > 0 || len(capability.AudioPassthroughCodecs) > 0
	if !hasAudioConstraints {
		return fallback
	}
	supportedCodecs := capability.AudioDecodeCodecs
	if claim.Passthrough {
		supportedCodecs = capability.AudioPassthroughCodecs
	}
	return containsFoldV3(supportedCodecs, codec)
}

// deliverySupportsPlanV3 applies the capability limits scoped to the delivery
// class after a concrete recipe has been built. Empty lists preserve clients
// that only advertise class availability; non-empty lists are authoritative
// subsets of the top-level device capabilities.
func deliverySupportsPlanV3(request StartRequestV3, deliveryClass string, plan PlanV3) bool {
	capability, ok := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !ok || !capability.Enabled || !capability.SupportedOnDevice {
		return false
	}
	if len(capability.Containers) > 0 && !containsFoldV3(capability.Containers, plan.Stream.Container) {
		return false
	}
	if codec := strings.TrimSpace(plan.EffectiveRecipe.VideoCodec); codec != "" && len(capability.VideoCodecs) > 0 && !containsFoldV3(capability.VideoCodecs, codec) {
		return false
	}
	if codec := strings.TrimSpace(plan.EffectiveRecipe.AudioCodec); codec != "" {
		hasAudioConstraints := len(capability.AudioDecodeCodecs) > 0 || len(capability.AudioPassthroughCodecs) > 0
		if hasAudioConstraints {
			supportedCodecs := capability.AudioDecodeCodecs
			if plan.Claims.Audio.Passthrough {
				supportedCodecs = capability.AudioPassthroughCodecs
			}
			if !containsFoldV3(supportedCodecs, codec) {
				return false
			}
		}
	}
	if capability.MaxChannels != nil && plan.EffectiveRecipe.AudioChannels != nil && *plan.EffectiveRecipe.AudioChannels > *capability.MaxChannels {
		return false
	}
	clientManagedOriginalRange := deliveryClass == DeliveryClassOriginalHTTPV3 &&
		len(plan.Transformations) == 0 &&
		containsFoldV3(capability.ValidatedClaims, ClaimClientManagedDynamicRangeV3)
	if capability.HDRDetails != nil && !hdrDetailsSupportPlanV3(*capability.HDRDetails, plan) && !clientManagedOriginalRange {
		return false
	}
	return true
}

func hdrDetailsSupportPlanV3(hdr HDRCapabilitiesV3, plan PlanV3) bool {
	switch plan.EffectiveRecipe.DynamicRange {
	case "", DynamicRangeSDRV3:
		return true
	case DynamicRangeHDR10V3, DynamicRangeHDRUnknownV3:
		return hdr.HDR10 && hdr10LimitsSupportPlanV3(hdr, plan)
	case DynamicRangeHDR10PlusV3:
		return hdr.HDR10Plus
	case DynamicRangeHLGV3:
		return hdr.HLG
	case DynamicRangeDolbyVisionV3:
		candidate := plan.Source
		for _, transformation := range plan.Transformations {
			if transformation.Name == ClientDV7ToDV81V3 {
				candidate.DVProfile = 8
				candidate.DVBLCompatID = 1
				break
			}
		}
		return hdrSupportsDolbyVisionSourceV3(hdr, candidate)
	default:
		return false
	}
}

func hdr10LimitsSupportPlanV3(hdr HDRCapabilitiesV3, plan PlanV3) bool {
	return !(hdr.HDR10MaxWidth > 0 && (plan.EffectiveRecipe.Width == nil || *plan.EffectiveRecipe.Width > hdr.HDR10MaxWidth) ||
		hdr.HDR10MaxHeight > 0 && (plan.EffectiveRecipe.Height == nil || *plan.EffectiveRecipe.Height > hdr.HDR10MaxHeight) ||
		hdr.HDR10MaxFrameRate > 0 && (plan.EffectiveRecipe.FrameRate == nil || *plan.EffectiveRecipe.FrameRate > hdr.HDR10MaxFrameRate) ||
		hdr.HDR10MaxBitrateKbps > 0 && (plan.EffectiveRecipe.BitrateKbps == nil || *plan.EffectiveRecipe.BitrateKbps > hdr.HDR10MaxBitrateKbps))
}

func hdrSupportsDolbyVisionSourceV3(hdr HDRCapabilitiesV3, source SourceDescriptorV3) bool {
	if !containsIntV3(hdr.DolbyVisionProfiles, source.DVProfile) {
		return false
	}
	var matchedCapability DolbyVisionProfileCapabilityV3
	foundCapability := false
	for _, capability := range hdr.DolbyVisionProfileLevels {
		if capability.Profile == source.DVProfile {
			matchedCapability = capability
			foundCapability = true
			break
		}
	}
	if !foundCapability {
		// Existing clients predate level-bounded Dolby Vision claims. Once a
		// client sends any bounds, every advertised profile must have one.
		return len(hdr.DolbyVisionProfileLevels) == 0
	}
	if len(matchedCapability.BLCompatibilityIDs) > 0 &&
		!containsIntV3(matchedCapability.BLCompatibilityIDs, source.DVBLCompatID) {
		return false
	}
	if source.DVLevel > 0 {
		return source.DVLevel <= matchedCapability.MaxLevel
	}
	return dolbyVisionSourceFitsLevelV3(source, matchedCapability.MaxLevel)
}

func dolbyVisionSourceFitsLevelV3(source SourceDescriptorV3, maxLevel int) bool {
	// Dolby Vision Version 2.0 defines levels by maximum pixel rate, decoded
	// width, and high-tier bitrate. Use those physical bounds for legacy rows
	// scanned before Silo persisted ffprobe's exact dv_level.
	type levelLimit struct {
		pixelRate   float64
		width       int
		bitrateKbps int
	}
	limits := map[int]levelLimit{
		1: {22_118_400, 1280, 50_000}, 2: {27_648_000, 1280, 50_000},
		3: {49_766_400, 1920, 70_000}, 4: {62_208_000, 2560, 70_000},
		5: {124_416_000, 3840, 70_000}, 6: {199_065_600, 3840, 130_000},
		7: {248_832_000, 3840, 130_000}, 8: {398_131_200, 3840, 130_000},
		9: {497_664_000, 3840, 130_000}, 10: {995_328_000, 3840, 240_000},
		11: {995_328_000, 7680, 240_000}, 12: {1_990_656_000, 7680, 480_000},
		13: {3_981_312_000, 7680, 800_000},
	}
	limit, ok := limits[maxLevel]
	if !ok || source.Width <= 0 || source.Height <= 0 || source.FrameRate <= 0 || source.BitrateKbps <= 0 {
		return false
	}
	return source.Width <= limit.width &&
		float64(source.Width*source.Height)*source.FrameRate <= limit.pixelRate &&
		source.BitrateKbps <= limit.bitrateKbps
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
