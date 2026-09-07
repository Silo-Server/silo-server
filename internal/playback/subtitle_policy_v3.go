package playback

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

type SubtitlePolicyResultV3 struct {
	Decision             SubtitleDecisionV3
	Claims               SubtitleClaimsV3
	RequiresBurn         bool
	SelectedIndex        int
	TransportIndex       int
	Codec                string
	Source               string
	DownloadedSubtitleID int
	Terminal             *TerminalV3
}

// SubtitleInventoryEntryV3 is one subtitle track that exists for the file but
// is not carried on models.MediaFile — today, downloaded and AI-generated
// tracks. Callers supply them in the order that defines their ordinals (see
// BuildSubtitleInventoryV3); CombinedIndex records the ordinal each entry was
// assigned so a caller holding an entry alone can still address it.
type SubtitleInventoryEntryV3 struct {
	CombinedIndex        int
	Codec                string
	Source               string
	Language             string
	Label                string
	Forced               bool
	HearingImpaired      bool
	DownloadedSubtitleID int `json:"-"`
}

// ResolveSubtitlePolicyV3 decides how the selected subtitle is delivered when
// the plan executes on the given delivery class. Renderability is
// delivery-specific, so callers must resolve the policy against the delivery
// class that will actually run the plan rather than assuming the
// original_http capabilities.
func ResolveSubtitlePolicyV3(file *models.MediaFile, request StartRequestV3, transcodeAllowed bool, deliveryClass string, additional []SubtitleInventoryEntryV3) SubtitlePolicyResultV3 {
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
	entry, ok := subtitleEntryAtCombinedIndexV3(file, index, additional)
	if !ok {
		return subtitleTerminalV3("subtitle_track_unavailable", "The selected subtitle track is unavailable.")
	}
	codec, source := entry.Codec, entry.Source
	trackID := TrackIDV3(file.ID, "subtitle", index)
	transportIndex := -1
	if source == "embedded" {
		transportIndex = index - len(file.ExternalSubtitles)
	}
	deliveryCaps := request.ClientPlaybackContext.Deliveries[deliveryClass]
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
	if source == SubtitleSourceEmbeddedV3 && (text || clientBitmap) {
		if native, nativeCaps, ok := nativeEmbeddedSubtitleV3(file, request, deliveryClass, transportIndex); ok {
			return SubtitlePolicyResultV3{
				Decision:      SubtitleDecisionV3{Mode: SubtitleRenderV3, TrackID: trackID, Embedded: native},
				Claims:        SubtitleClaimsV3{ASSStylingPreserved: !ass || nativeCaps.ASSStyling, Reason: "client_embedded_subtitle_supported"},
				SelectedIndex: index, TransportIndex: transportIndex, Codec: codec, Source: source,
			}
		}
	}
	if text {
		renderable := deliveryCaps.Subtitles.SidecarText
		if ass && request.SubtitleFidelityPreference == SubtitleFidelityPreserveV3 {
			renderable = renderable && deliveryCaps.Subtitles.ASSStyling && deliveryCaps.Subtitles.FontAttachments
		}
		if renderable {
			return SubtitlePolicyResultV3{
				Decision:      SubtitleDecisionV3{Mode: SubtitleRenderV3, TrackID: trackID},
				Claims:        SubtitleClaimsV3{ASSStylingPreserved: !ass || deliveryCaps.Subtitles.ASSStyling, Reason: "client_render_supported"},
				SelectedIndex: index, TransportIndex: transportIndex, Codec: codec, Source: source,
				DownloadedSubtitleID: entry.DownloadedSubtitleID,
			}
		}
		if request.SubtitleFidelityPreference == SubtitleFidelityCompatibleV3 && deliveryCaps.Subtitles.SidecarText {
			return SubtitlePolicyResultV3{
				Decision:      SubtitleDecisionV3{Mode: SubtitleConvertV3, TrackID: trackID},
				Claims:        SubtitleClaimsV3{Reason: "server_text_conversion"},
				SelectedIndex: index, TransportIndex: transportIndex, Codec: codec, Source: source,
				DownloadedSubtitleID: entry.DownloadedSubtitleID,
			}
		}
	}
	// The stream handler raw-serves exactly one bitmap sidecar shape: an
	// embedded PGS track as .sup. External/downloaded bitmap tracks go through
	// WebVTT conversion (impossible for bitmaps) and embedded DVD/DVB have no
	// client-renderable representation at all, so promising a sidecar for
	// anything broader publishes an artifact URL that always fails at fetch.
	// Everything else falls through to burn-in or its terminal.
	if clientBitmap && source == "embedded" && IsPGS(codec) && deliveryCaps.Subtitles.EmbeddedBitmap {
		return SubtitlePolicyResultV3{
			Decision:      SubtitleDecisionV3{Mode: SubtitleRenderV3, TrackID: trackID},
			Claims:        SubtitleClaimsV3{BitmapSidecar: true, Reason: "client_bitmap_render_supported"},
			SelectedIndex: index, TransportIndex: transportIndex, Codec: codec, Source: source,
			DownloadedSubtitleID: entry.DownloadedSubtitleID,
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
			DownloadedSubtitleID: entry.DownloadedSubtitleID,
		}
	}
	return subtitleTerminalV3("subtitle_conversion_unsupported", fmt.Sprintf("Subtitle format %s cannot meet the selected fidelity policy.", codec))
}

// subtitleEntryAtCombinedIndexV3 resolves a combined ordinal through the same
// three inventory ranges the plan publishes while retaining the stable row ID
// of a downloaded subtitle for frozen seek recipes.
func subtitleEntryAtCombinedIndexV3(file *models.MediaFile, index int, additional []SubtitleInventoryEntryV3) (SubtitleInventoryEntryV3, bool) {
	if file == nil || index < 0 {
		return SubtitleInventoryEntryV3{}, false
	}
	if index < len(file.ExternalSubtitles) {
		return SubtitleInventoryEntryV3{CombinedIndex: index, Codec: normalizeCodecV3(file.ExternalSubtitles[index].Format), Source: "external"}, true
	}
	embedded := index - len(file.ExternalSubtitles)
	if embedded >= 0 && embedded < len(file.SubtitleTracks) {
		return SubtitleInventoryEntryV3{CombinedIndex: index, Codec: normalizeCodecV3(file.SubtitleTracks[embedded].Codec), Source: "embedded"}, true
	}
	for _, entry := range additional {
		if entry.CombinedIndex == index {
			entry.Codec = normalizeCodecV3(entry.Codec)
			return entry, true
		}
	}
	return SubtitleInventoryEntryV3{}, false
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
	// normalizeCodecV3 centralizes the short spellings carried by older rows;
	// the bitmap policy can then use the same canonical set as burn-in.
	return NeedsBurnIn(codec)
}

func subtitleTerminalV3(reason, message string) SubtitlePolicyResultV3 {
	return SubtitlePolicyResultV3{Decision: SubtitleDecisionV3{Mode: SubtitleOffV3}, SelectedIndex: -1, TransportIndex: -1, Terminal: &TerminalV3{Reason: reason, Message: message}}
}

func nativeEmbeddedSubtitleV3(file *models.MediaFile, request StartRequestV3, deliveryClass string, ordinal int) (*EmbeddedSubtitleV3, NativeEmbeddedSubtitleCapabilityV3, bool) {
	var none NativeEmbeddedSubtitleCapabilityV3
	if deliveryClass != DeliveryClassOriginalHTTPV3 || !HasFeatureV3(request.ClientFeatures, FeatureEmbeddedSubtitlesV3) || ordinal < 0 || ordinal >= len(file.SubtitleTracks) {
		return nil, none, false
	}
	delivery := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !delivery.Enabled || !delivery.SupportedOnDevice {
		return nil, none, false
	}
	track := file.SubtitleTracks[ordinal]
	if track.Index < 0 {
		return nil, none, false
	}
	// Reject ambiguous source metadata instead of selecting a different stream.
	for i, other := range file.SubtitleTracks {
		if i != ordinal && other.Index == track.Index {
			return nil, none, false
		}
	}
	for _, capability := range delivery.Subtitles.NativeEmbedded {
		if !strings.EqualFold(capability.Container, file.Container) {
			continue
		}
		if !slices.ContainsFunc(capability.Codecs, func(codec string) bool {
			return normalizeNativeSubtitleCodecV3(codec) == normalizeNativeSubtitleCodecV3(track.Codec)
		}) {
			continue
		}
		if IsASS(track.Codec) && request.SubtitleFidelityPreference == SubtitleFidelityPreserveV3 && (!capability.ASSStyling || !capability.FontAttachments) {
			continue
		}
		switch capability.TrackIdentity {
		case subtitleIdentityFFmpegV3:
		case subtitleIdentityContainerV3:
			id, err := strconv.ParseUint(track.ContainerTrackID, 10, 32)
			if err != nil || id == 0 || strconv.FormatUint(id, 10) != track.ContainerTrackID {
				continue
			}
			ambiguous := false
			for i, other := range file.SubtitleTracks {
				if i != ordinal && other.ContainerTrackID == track.ContainerTrackID {
					ambiguous = true
				}
			}
			if ambiguous {
				continue
			}
		default:
			continue
		}
		return &EmbeddedSubtitleV3{StreamIndex: track.Index, ContainerTrackID: track.ContainerTrackID}, capability, true
	}
	return nil, none, false
}

func normalizeNativeSubtitleCodecV3(codec string) string {
	switch codec = normalizeCodecV3(codec); codec {
	case "srt":
		return "subrip"
	case "vtt":
		return "webvtt"
	case "tx3g":
		return "mov_text"
	case subtitleCodecPGS:
		return subtitleCodecPGSFFmpeg
	default:
		return codec
	}
}
