package playback

// What a caller of PlanPlaybackV3 may try after a refusal. These answers live
// beside the planner because they depend on which reason it reports for which
// cause: changing a refusal there changes what a caller does with it here.

// settingsRefusalV3 is a refusal that a playback setting decided.
func settingsRefusalV3(reason, message string) PlannerResultV3 {
	result := terminalPlannerResultV3(reason, message, false)
	result.SettingsDependentRefusal = true
	return result
}

// AnotherVersionMayPlay reports whether a refusal is the kind another version
// of the same item could satisfy.
//
// subtitle_conversion_unsupported belongs here because it is not only a
// subtitle-format refusal: when a burn-in requirement is the sole trigger of an
// adaptation the source cannot take, the planner reports the blocker in terms
// of the subtitle rather than the HDR pipeline or the 4K policy. Those cases
// used to surface as hdr_transcode_unsupported / no_alternate_version and were
// the exact reason this gate exists — a bitmap subtitle needing burn-in that an
// HDR source cannot support while an SDR alternate can. Leaving the new reason
// out silently retired that fallback and refused playback outright.
//
// bitrate_policy_unavailable belongs here for the same reason: it replaces the
// 4K and HDR refusals of a version that exceeds the stream's bitrate limit, and
// a lower-bitrate version may fit that limit.
//
// source_unreadable belongs here because the refusal is about one damaged
// file, not the item: a readable version of the same item still plays.
func (result PlannerResultV3) AnotherVersionMayPlay() bool {
	if result.Terminal == nil {
		return false
	}
	switch result.Terminal.Reason {
	case TerminalNoAlternateVersionV3, TerminalHDRTranscodeUnsupportedV3, TerminalSubtitleConversionUnsupportedV3,
		TerminalBitratePolicyUnavailableV3, TerminalSourceUnreadableV3:
		return true
	default:
		return false
	}
}

// SubtitleRefusal reports a refusal the planner pinned on the selected
// subtitle: a burn-in that was the only reason for an adaptation the file
// cannot take, or a subtitle the fidelity policy cannot serve. The file may
// then play with the subtitle cleared; callers re-plan it to find out.
func (result PlannerResultV3) SubtitleRefusal() bool {
	return result.Terminal != nil && result.Terminal.Reason == TerminalSubtitleConversionUnsupportedV3
}

// ToneMapDependentRefusal reports a refusal whose reason a missing tone-map
// recipe can produce: hdr_transcode_unsupported, or
// subtitle_conversion_unsupported when a burn-in forced the encode. When the
// lookup of tone-map capabilities failed, a missing recipe proves nothing, so
// the caller reports the refusal as temporary. The reason alone decides:
// every subtitle_conversion_unsupported counts, including a format the
// fidelity policy cannot serve, which no tone-map recipe would change.
func (result PlannerResultV3) ToneMapDependentRefusal() bool {
	if result.Terminal == nil {
		return false
	}
	switch result.Terminal.Reason {
	case TerminalHDRTranscodeUnsupportedV3, TerminalSubtitleConversionUnsupportedV3:
		return true
	default:
		return false
	}
}
