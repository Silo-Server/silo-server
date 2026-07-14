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

func ResolveSubtitlePolicyV3(file *models.MediaFile, request StartRequestV3, transcodeAllowed bool, additional []SubtitleInventoryEntryV3) SubtitlePolicyResultV3 {
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
	engine := request.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)]
	text := isTextSubtitleV3(codec)
	ass := IsASS(codec)
	clientBitmap := isClientRenderableBitmapSubtitleV3(codec)
	burnInBitmap := clientBitmap || normalizeCodecV3(codec) == "dvb_teletext"
	if text {
		renderable := source != "embedded" && engine.Subtitles.SidecarText || source == "embedded" && engine.Subtitles.EmbeddedText
		if ass && request.SubtitleFidelityPreference == SubtitleFidelityPreserveV3 {
			renderable = renderable && engine.Subtitles.ASSStyling && engine.Subtitles.FontAttachments
		}
		if renderable {
			return SubtitlePolicyResultV3{
				Decision:      SubtitleDecisionV3{Mode: SubtitleRenderV3, TrackID: trackID},
				Claims:        SubtitleClaimsV3{ASSStylingPreserved: !ass || engine.Subtitles.ASSStyling, Reason: "client_render_supported"},
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
	if clientBitmap {
		sidecar := source != "embedded" && engine.Subtitles.SidecarBitmap || source == "embedded" && engine.Subtitles.EmbeddedBitmap
		if sidecar {
			return SubtitlePolicyResultV3{
				Decision:      SubtitleDecisionV3{Mode: SubtitleRenderV3, TrackID: trackID},
				Claims:        SubtitleClaimsV3{BitmapSidecar: true, Reason: "client_bitmap_render_supported"},
				SelectedIndex: index, TransportIndex: transportIndex, Codec: codec, Source: source,
			}
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
	case "pgs", "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "vobsub":
		return true
	default:
		return false
	}
}

func subtitleTerminalV3(reason, message string) SubtitlePolicyResultV3 {
	return SubtitlePolicyResultV3{Decision: SubtitleDecisionV3{Mode: SubtitleOffV3}, SelectedIndex: -1, TransportIndex: -1, Terminal: &TerminalV3{Reason: reason, Message: message}}
}
