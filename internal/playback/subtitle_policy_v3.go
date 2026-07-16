package playback

import (
	"fmt"

	"github.com/Silo-Server/silo-server/internal/models"
)

type SubtitlePolicyResultV3 struct {
	Decision       SubtitleDecisionV3
	Claims         SubtitleClaimsV3
	RequiresBurn   bool
	SelectedIndex  int
	TransportIndex int
	Codec          string
	Source         string
	Terminal       *TerminalV3
}

type SubtitleInventoryEntryV3 struct {
	CombinedIndex int
	Codec         string
	Source        string
}

// ResolveSubtitlePolicyV3 decides how the selected subtitle is delivered when
// the plan executes on the given engine. Renderability is engine-specific, so
// callers must resolve the policy against the engine that will actually run
// the plan rather than assuming the direct engine's capabilities.
func ResolveSubtitlePolicyV3(file *models.MediaFile, request StartRequestV3, transcodeAllowed bool, engine EngineV3, additional []SubtitleInventoryEntryV3) SubtitlePolicyResultV3 {
	index := -1
	if request.SubtitleTrackIndex != nil {
		index = *request.SubtitleTrackIndex
	} else if request.SubtitleTrackID != "" {
		fileID, kind, ordinal, ok := ParseTrackIDV3(request.SubtitleTrackID)
		if !ok || kind != "subtitle" || file == nil || fileID != file.ID {
			return subtitleTerminalV3("subtitle_track_invalid", "The selected subtitle identity is invalid.")
		}
		index = ordinal
	}
	if index < 0 {
		return SubtitlePolicyResultV3{Decision: SubtitleDecisionV3{Mode: SubtitleOffV3}, SelectedIndex: -1, TransportIndex: -1}
	}
	if file == nil {
		return subtitleTerminalV3("subtitle_track_unavailable", "The selected subtitle inventory is unavailable.")
	}
	codec, source, ok := subtitleCodecAtCombinedIndexV3(file, index, additional)
	if !ok {
		return subtitleTerminalV3("subtitle_track_unavailable", "The selected subtitle track is unavailable.")
	}
	trackID := TrackIDV3(file.ID, "subtitle", index)
	transportIndex := -1
	if source == "embedded" {
		transportIndex = index - len(file.ExternalSubtitles)
	}
	engineCaps := request.ClientPlaybackContext.Engines[string(engine)]
	text := isTextSubtitleV3(codec)
	ass := IsASS(codec)
	clientBitmap := isClientRenderableBitmapSubtitleV3(codec)
	burnInBitmap := clientBitmap || normalizeCodecV3(codec) == "dvb_teletext"
	if !text && !burnInBitmap {
		// Unknown codecs (arib_caption, ...) have no validated client render
		// path and no validated burn-in filter; promising a silent burn-in
		// would produce a plan the transcoder cannot honor.
		return subtitleTerminalV3("subtitle_codec_unsupported", fmt.Sprintf("Subtitle format %s has no validated rendering or burn-in route.", codec))
	}
	if text {
		renderable := source != "embedded" && engineCaps.Subtitles.SidecarText || source == "embedded" && engineCaps.Subtitles.EmbeddedText
		if ass && request.SubtitleFidelityPreference == SubtitleFidelityPreserveV3 {
			renderable = renderable && engineCaps.Subtitles.ASSStyling && engineCaps.Subtitles.FontAttachments
		}
		if renderable {
			return SubtitlePolicyResultV3{
				Decision:      SubtitleDecisionV3{Mode: SubtitleRenderV3, TrackID: trackID},
				Claims:        SubtitleClaimsV3{ASSStylingPreserved: !ass || engineCaps.Subtitles.ASSStyling, Reason: "client_render_supported"},
				SelectedIndex: index, TransportIndex: transportIndex, Codec: codec, Source: source,
			}
		}
		if request.SubtitleFidelityPreference == SubtitleFidelityCompatibleV3 {
			return SubtitlePolicyResultV3{
				Decision:      SubtitleDecisionV3{Mode: SubtitleConvertV3, TrackID: trackID},
				Claims:        SubtitleClaimsV3{Reason: "server_text_conversion"},
				SelectedIndex: index, TransportIndex: transportIndex, Codec: codec, Source: source,
			}
		}
	}
	// The stream handler raw-serves exactly one bitmap sidecar shape: an
	// embedded PGS track as .sup. External/downloaded bitmap tracks go through
	// WebVTT conversion (impossible for bitmaps) and embedded DVD/DVB have no
	// client-renderable representation at all, so promising a sidecar for
	// anything broader publishes an artifact URL that always fails at fetch.
	// Everything else falls through to burn-in or its terminal.
	if clientBitmap && source == "embedded" && IsPGS(codec) && engineCaps.Subtitles.EmbeddedBitmap {
		return SubtitlePolicyResultV3{
			Decision:      SubtitleDecisionV3{Mode: SubtitleRenderV3, TrackID: trackID},
			Claims:        SubtitleClaimsV3{BitmapSidecar: true, Reason: "client_bitmap_render_supported"},
			SelectedIndex: index, TransportIndex: transportIndex, Codec: codec, Source: source,
		}
	}
	if transcodeAllowed {
		if source != "embedded" {
			return subtitleTerminalV3("subtitle_burn_in_source_unsupported", "The selected subtitle source cannot be burned in by the installed transport.")
		}
		return SubtitlePolicyResultV3{
			Decision:     SubtitleDecisionV3{Mode: SubtitleBurnInV3, TrackID: trackID},
			Claims:       SubtitleClaimsV3{BitmapOverlay: burnInBitmap, Reason: "server_burn_in_required"},
			RequiresBurn: true, SelectedIndex: index, TransportIndex: transportIndex, Codec: codec, Source: source,
		}
	}
	return subtitleTerminalV3("subtitle_conversion_unsupported", fmt.Sprintf("Subtitle format %s cannot meet the selected fidelity policy.", codec))
}

func subtitleCodecAtCombinedIndexV3(file *models.MediaFile, index int, additional []SubtitleInventoryEntryV3) (codec, source string, ok bool) {
	if index < len(file.ExternalSubtitles) {
		return normalizeCodecV3(file.ExternalSubtitles[index].Format), "external", true
	}
	embedded := index - len(file.ExternalSubtitles)
	if embedded >= 0 && embedded < len(file.SubtitleTracks) {
		return normalizeCodecV3(file.SubtitleTracks[embedded].Codec), "embedded", true
	}
	for _, entry := range additional {
		if entry.CombinedIndex == index {
			return normalizeCodecV3(entry.Codec), entry.Source, true
		}
	}
	return "", "", false
}

func isTextSubtitleV3(codec string) bool {
	switch normalizeCodecV3(codec) {
	case "srt", "subrip", "vtt", "webvtt", "ass", "ssa", "mov_text", "tx3g",
		"eia_608", "eia608", "cea_608", "cea608":
		return true
	default:
		return false
	}
}

func isClientRenderableBitmapSubtitleV3(codec string) bool {
	switch normalizeCodecV3(codec) {
	// pgssub, dvdsub, and dvbsub are ffmpeg's short aliases for the same
	// bitstreams; scanners and older rows record either spelling.
	case "pgs", "pgssub", "hdmv_pgs_subtitle", "dvd_subtitle", "dvdsub", "dvb_subtitle", "dvbsub", "vobsub":
		return true
	default:
		return false
	}
}

func subtitleTerminalV3(reason, message string) SubtitlePolicyResultV3 {
	return SubtitlePolicyResultV3{Decision: SubtitleDecisionV3{Mode: SubtitleOffV3}, SelectedIndex: -1, TransportIndex: -1, Terminal: &TerminalV3{Reason: reason, Message: message}}
}
