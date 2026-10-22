package playback

import (
	"slices"
	"strings"
)

// Quality reason codes. The list is open: clients tolerate codes they do not
// know.
const (
	QualityReasonOriginalV3              = "original"
	QualityReasonUserSelectedV3          = "user_selected"
	QualityReasonServerCapV3             = "server_cap"
	QualityReasonClientCapV3             = "client_cap"
	QualityReasonNetworkLearnedLimitV3   = "network_learned_limit"
	QualityReasonNetworkEstimateV3       = "network_estimate"
	QualityReasonDeviceResolutionLimitV3 = "device_resolution_limit"
	QualityReasonAlternateVersionV3      = "alternate_version"
	QualityReasonVideoCodecUnsupportedV3 = "video_codec_unsupported"
	QualityReasonSubtitleBurnInV3        = "subtitle_burn_in"
	QualityReasonDynamicRangeConvertedV3 = "dynamic_range_converted"
	// QualityReasonCompatibilityConversionV3 names a video encode the planner
	// chose for none of the other reasons: a source whose video cannot be
	// copied safely, or copy routes that already failed on this output.
	QualityReasonCompatibilityConversionV3 = "compatibility_conversion"
	QualityReasonAudioConvertedV3          = "audio_converted"
	QualityReasonContainerUnsupportedV3    = "container_unsupported"
)

// FFmpeg codec names that quality reasons spell as the shorter reason tokens.
const (
	ffmpegCodecMPEG2VideoV3 = "mpeg2video"
	ffmpegCodecDCAV3        = "dca"
	reasonCodecMPEG2V3      = "mpeg2"
	reasonCodecDTSV3        = "dts"
)

// QualityReasonV3 is one entry of PlanV3.QualityReasons: a code and the
// figures that code carries.
type QualityReasonV3 struct {
	Code        string `json:"code"`
	Kbps        int    `json:"kbps,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Height      int    `json:"height,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	MediaFileID int    `json:"media_file_id,omitempty"`
}

// qualityReasonRankV3 orders the codes. The first entry is the one that
// decided the video quality, so the codes that can decide it come first, in
// precedence order. An alternate version follows the limit that chose it.
// Audio and packaging changes never decide quality and come last.
var qualityReasonRankV3 = map[string]int{
	QualityReasonUserSelectedV3:            0,
	QualityReasonServerCapV3:               1,
	QualityReasonClientCapV3:               2,
	QualityReasonNetworkLearnedLimitV3:     3,
	QualityReasonNetworkEstimateV3:         4,
	QualityReasonDeviceResolutionLimitV3:   5,
	QualityReasonAlternateVersionV3:        6,
	QualityReasonVideoCodecUnsupportedV3:   7,
	QualityReasonSubtitleBurnInV3:          8,
	QualityReasonDynamicRangeConvertedV3:   9,
	QualityReasonCompatibilityConversionV3: 10,
	QualityReasonOriginalV3:                11,
	QualityReasonAudioConvertedV3:          12,
	QualityReasonContainerUnsupportedV3:    13,
}

// qualityFactsV3 is what the planner knew when it chose the route that
// qualityReasonsV3 explains. The plan itself supplies the rest.
type qualityFactsV3 struct {
	// clientCapKbps is the request's own bandwidth_cap_kbps, before the
	// administrator's cap is folded into it.
	clientCapKbps int
	serverCapKbps int
	estimateKbps  int
	// quality is the final quality decision for a video source; zero for
	// audio-only sources.
	quality QualityResultV3
	// capEncode reports that a bitrate cap, not the quality decision, forced
	// or bounded the encode: a source over the server cap, or an audio-only
	// source over either cap.
	capEncode        bool
	videoUnsupported bool
	versionPinned    bool
	// requested is the version the viewer asked for when another version
	// plays; nil otherwise.
	requested *SourceDescriptorV3
	// maxHeight is the device's maximum resolution for automatic quality.
	maxHeight int
}

// qualityReasonsV3 explains a playable plan: entry 0 is what decided the video
// quality, later entries are the other changes the plan makes.
func qualityReasonsV3(facts qualityFactsV3, result PlannerResultV3) []QualityReasonV3 {
	plan := result.Plan
	reasons := qualityDecisionReasonsV3(facts, result)
	add := func(reason QualityReasonV3) { reasons = appendQualityReasonV3(reasons, reason) }
	if len(reasons) == 0 {
		code := QualityReasonOriginalV3
		if facts.versionPinned {
			code = QualityReasonUserSelectedV3
		}
		add(QualityReasonV3{Code: code})
	}
	audioChanged := false
	if from, to := audioCodecTokenV3(plan.Source.AudioCodec), audioCodecTokenV3(plan.EffectiveRecipe.AudioCodec); result.TranscodeAudio && from != "" &&
		(from != to || optionalValueV3(plan.EffectiveRecipe.AudioChannels) > 0 && optionalValueV3(plan.EffectiveRecipe.AudioChannels) < plan.Source.AudioChannels) {
		add(QualityReasonV3{Code: QualityReasonAudioConvertedV3, From: from, To: to})
		audioChanged = true
	}
	remux := plan.Delivery == DeliveryRemuxProgressiveV3 || plan.Delivery == DeliveryRemuxHLSV3
	if remux && !audioChanged && !hasQualityReasonV3(reasons, QualityReasonDynamicRangeConvertedV3) {
		add(QualityReasonV3{Code: QualityReasonContainerUnsupportedV3})
	}
	sortQualityReasonsV3(reasons)
	return reasons
}

// qualityDecisionReasonsV3 lists the reasons that can decide the video
// quality, unordered; none when the source plays as it is.
func qualityDecisionReasonsV3(facts qualityFactsV3, result PlannerResultV3) []QualityReasonV3 {
	plan := result.Plan
	var reasons []QualityReasonV3
	add := func(reason QualityReasonV3) { reasons = appendQualityReasonV3(reasons, reason) }
	encodesVideo := plan.Delivery == DeliveryTranscodeHLSV3
	audioOnly := plan.Source.VideoCodec == ""
	if facts.quality.ExplicitRung {
		add(QualityReasonV3{Code: QualityReasonUserSelectedV3})
	}
	if (encodesVideo || audioOnly) && (facts.quality.CapApplied || facts.capEncode || result.qualityCapBound) {
		add(capReasonV3(facts.clientCapKbps, facts.serverCapKbps))
	}
	if encodesVideo {
		if facts.quality.OverEstimate {
			add(QualityReasonV3{Code: QualityReasonNetworkEstimateV3, Kbps: facts.estimateKbps})
		}
		if height := max(facts.quality.DeviceLimitHeight, result.qualityDecoderHeight); height > 0 {
			add(QualityReasonV3{Code: QualityReasonDeviceResolutionLimitV3, Height: height})
		}
		if facts.videoUnsupported {
			add(QualityReasonV3{Code: QualityReasonVideoCodecUnsupportedV3, From: videoCodecTokenV3(plan.Source.VideoCodec)})
		}
	}
	if facts.requested != nil {
		// The version that plays was chosen for the requested one: name the
		// limit that version did not fit, when one did.
		for _, reason := range alternateVersionCauseV3(facts) {
			add(reason)
		}
		add(QualityReasonV3{Code: QualityReasonAlternateVersionV3, MediaFileID: plan.EffectiveMediaFileID})
	}
	if result.SubtitleBurnIn {
		add(QualityReasonV3{Code: QualityReasonSubtitleBurnInV3})
	}
	if from, to := dynamicRangeTokenV3(plan.Source.DynamicRange), dynamicRangeTokenV3(plan.EffectiveRecipe.DynamicRange); !audioOnly && from != to {
		add(QualityReasonV3{Code: QualityReasonDynamicRangeConvertedV3, From: from, To: to})
	}
	if encodesVideo && len(reasons) == 0 {
		add(QualityReasonV3{Code: QualityReasonCompatibilityConversionV3})
	}
	return reasons
}

// appendQualityReasonV3 adds reason unless its code is already listed.
func appendQualityReasonV3(reasons []QualityReasonV3, reason QualityReasonV3) []QualityReasonV3 {
	if hasQualityReasonV3(reasons, reason.Code) {
		return reasons
	}
	return append(reasons, reason)
}

func hasQualityReasonV3(reasons []QualityReasonV3, code string) bool {
	return slices.ContainsFunc(reasons, func(reason QualityReasonV3) bool { return reason.Code == code })
}

// capReasonV3 names the cap that bounds delivery: the lower of the
// administrator's and the request's, the administrator's on a tie.
func capReasonV3(clientCapKbps, serverCapKbps int) QualityReasonV3 {
	if serverCapKbps > 0 && (clientCapKbps <= 0 || serverCapKbps <= clientCapKbps) {
		return QualityReasonV3{Code: QualityReasonServerCapV3, Kbps: serverCapKbps}
	}
	return QualityReasonV3{Code: QualityReasonClientCapV3, Kbps: clientCapKbps}
}

// alternateVersionCauseV3 names what the requested version did not fit: the
// tightest bitrate limit it exceeds, and the device's maximum resolution. A
// version chosen for another reason, such as a refusal of the requested one,
// has no cause here.
func alternateVersionCauseV3(facts qualityFactsV3) []QualityReasonV3 {
	requested := *facts.requested
	var reasons []QualityReasonV3
	// tightest is the reason whose limit is lowest; ties keep the earlier,
	// higher-precedence one.
	var tightest *QualityReasonV3
	tightestKbps := 0
	consider := func(reason QualityReasonV3, limitKbps int) {
		if limitKbps > 0 && requested.BitrateKbps > limitKbps && (tightest == nil || limitKbps < tightestKbps) {
			tightest, tightestKbps = &reason, limitKbps
		}
	}
	consider(QualityReasonV3{Code: QualityReasonServerCapV3, Kbps: facts.serverCapKbps}, facts.serverCapKbps)
	consider(QualityReasonV3{Code: QualityReasonClientCapV3, Kbps: facts.clientCapKbps}, facts.clientCapKbps)
	if facts.estimateKbps > 0 && !facts.quality.ExplicitRung {
		// The estimate's figure is the estimate itself, not its budget.
		consider(QualityReasonV3{Code: QualityReasonNetworkEstimateV3, Kbps: facts.estimateKbps}, StreamBudgetKbpsV3(facts.estimateKbps))
	}
	if tightest != nil {
		reasons = append(reasons, *tightest)
	}
	if facts.maxHeight > 0 && requested.Height > facts.maxHeight && !facts.quality.ExplicitRung {
		reasons = append(reasons, QualityReasonV3{Code: QualityReasonDeviceResolutionLimitV3, Height: facts.maxHeight})
	}
	return reasons
}

// ApplyLearnedLimitReasonV3 names a learned limit that lowered plan, once the
// handler has settled plan.BandwidthLimit: the planner saw only an estimate.
func ApplyLearnedLimitReasonV3(plan *PlanV3) {
	if plan == nil || plan.BandwidthLimit == nil || plan.QualityReasons == nil {
		return
	}
	learned := QualityReasonV3{Code: QualityReasonNetworkLearnedLimitV3, Kbps: plan.BandwidthLimit.Kbps, ExpiresAt: plan.BandwidthLimit.ExpiresAt}
	reasons := slices.DeleteFunc(slices.Clone(plan.QualityReasons), func(reason QualityReasonV3) bool {
		// The limit replaces the estimate the planner used and lowers what
		// would otherwise have been the original.
		return reason.Code == QualityReasonNetworkEstimateV3 || reason.Code == QualityReasonOriginalV3 || reason.Code == QualityReasonNetworkLearnedLimitV3
	})
	reasons = append(reasons, learned)
	sortQualityReasonsV3(reasons)
	plan.QualityReasons = reasons
}

func sortQualityReasonsV3(reasons []QualityReasonV3) {
	slices.SortStableFunc(reasons, func(a, b QualityReasonV3) int {
		return qualityReasonRankV3[a.Code] - qualityReasonRankV3[b.Code]
	})
}

// dynamicRangeTokenV3 spells a recipe or source range as a reason token. An
// HDR source of unknown range plays as HDR10, and a missing range is SDR.
func dynamicRangeTokenV3(dynamicRange string) string {
	switch dynamicRange {
	case DynamicRangeHDRUnknownV3:
		return DynamicRangeHDR10V3
	case "":
		return DynamicRangeSDRV3
	default:
		return dynamicRange
	}
}

func videoCodecTokenV3(codec string) string {
	switch codec = normalizeCodecV3(codec); codec {
	case ffmpegCodecMPEG2VideoV3:
		return reasonCodecMPEG2V3
	default:
		return codec
	}
}

func audioCodecTokenV3(codec string) string {
	codec = normalizeCodecV3(codec)
	switch {
	case codec == ffmpegCodecDCAV3:
		return reasonCodecDTSV3
	case strings.HasPrefix(codec, "pcm_"):
		return "pcm"
	default:
		return codec
	}
}
