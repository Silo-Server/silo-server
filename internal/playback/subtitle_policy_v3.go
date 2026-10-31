package playback

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
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
	// Degraded reports that a positive selection could not be honored and
	// the policy fell back to subtitles-off instead of refusing playback.
	Degraded bool
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

// SubtitleFactV3 is something the server may or may not have learned about a
// subtitle track.
type SubtitleFactV3 uint8

const (
	SubtitleFactUnknownV3 SubtitleFactV3 = iota
	SubtitleFactNoV3
	SubtitleFactYesV3
)

// SubtitleTrackFactsV3 reports what the server learned about an embedded
// track by reading its text. Planning consults it only for a limitation a
// client declares, and an unknown fact never disqualifies a track.
type SubtitleTrackFactsV3 interface {
	// OverrideTags reports whether the track's cues carry ASS override tags
	// such as {\an8}.
	OverrideTags(file *models.MediaFile, track models.SubtitleTrack) SubtitleFactV3
}

// UnknownSubtitleTrackFactsV3 knows nothing about any track. Planning uses it
// when the caller supplies no facts.
type UnknownSubtitleTrackFactsV3 struct{}

func (UnknownSubtitleTrackFactsV3) OverrideTags(*models.MediaFile, models.SubtitleTrack) SubtitleFactV3 {
	return SubtitleFactUnknownV3
}

// ResolveSubtitlePolicyV3 decides how the selected subtitle is delivered when
// the plan executes on the given delivery class. Renderability is
// delivery-specific, so callers must resolve the policy against the delivery
// class that will actually run the plan rather than assuming the
// original_http capabilities. audioIndex is the audio ordinal the plan plays
// (out of range means the file's default), and facts may be nil.
func ResolveSubtitlePolicyV3(file *models.MediaFile, request StartRequestV3, transcodeAllowed bool, deliveryClass string, additional []SubtitleInventoryEntryV3, audioIndex int, facts SubtitleTrackFactsV3) SubtitlePolicyResultV3 {
	index, ok := requestedSubtitleIndexV3(file, request)
	if !ok {
		return subtitleTerminalV3("subtitle_track_invalid", "The selected subtitle identity is invalid.")
	}
	if index < 0 {
		return SubtitlePolicyResultV3{Decision: SubtitleDecisionV3{Mode: SubtitleOffV3}, SelectedIndex: -1, TransportIndex: -1}
	}
	if file == nil {
		return subtitleTerminalV3("subtitle_track_unavailable", "The selected subtitle inventory is unavailable.")
	}
	entry, ok := subtitleEntryAtCombinedIndexV3(file, index, additional)
	if !ok {
		// A selection carried over from another file — the next episode in a
		// series routinely has a different track layout — is not a reason to
		// refuse playback. Nothing is broken: the file plays, one preference
		// cannot be honored. Degrade to subtitles-off and report it, exactly
		// as the version-switch remap path does. A malformed identity still
		// errors above, and request validation rejects a negative index: those
		// are client bugs.
		return SubtitlePolicyResultV3{
			Decision:       SubtitleDecisionV3{Mode: SubtitleOffV3},
			SelectedIndex:  -1,
			TransportIndex: -1,
			Degraded:       true,
		}
	}
	codec, source := entry.Codec, entry.Source
	trackID := TrackIDV3(file.ID, trackKindSubtitleV3, index)
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
	if source == SubtitleSourceEmbeddedV3 {
		if native, nativeCaps, ok := nativeEmbeddedSubtitleV3(file, request, deliveryClass, transportIndex, audioIndex, facts); ok {
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

// trackKindSubtitleV3 is the kind segment of a subtitle track ID.
const trackKindSubtitleV3 = "subtitle"

// requestedSubtitleIndexV3 is the combined ordinal the request selects, or -1
// for none. It is false for an identity that is malformed or names another
// file.
func requestedSubtitleIndexV3(file *models.MediaFile, request StartRequestV3) (int, bool) {
	if request.SubtitleTrackIndex != nil {
		return *request.SubtitleTrackIndex, true
	}
	if request.SubtitleTrackID == "" {
		return -1, true
	}
	fileID, kind, ordinal, ok := ParseTrackIDV3(request.SubtitleTrackID)
	if !ok || kind != trackKindSubtitleV3 || file == nil || fileID != file.ID {
		return -1, false
	}
	return ordinal, true
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

// SubtitleFormatDeliverableV3 reports whether the v3 subtitle policy can
// deliver a track in this format at all: as text, or by burning in a bitmap.
func SubtitleFormatDeliverableV3(codec string) bool {
	return isTextSubtitleV3(codec) || NeedsBurnIn(codec) || normalizeCodecV3(codec) == "dvb_teletext"
}

func isClientRenderableBitmapSubtitleV3(codec string) bool {
	// normalizeCodecV3 centralizes the short spellings carried by older rows;
	// the bitmap policy can then use the same canonical set as burn-in.
	return NeedsBurnIn(codec)
}

func subtitleTerminalV3(reason, message string) SubtitlePolicyResultV3 {
	return SubtitlePolicyResultV3{Decision: SubtitleDecisionV3{Mode: SubtitleOffV3}, SelectedIndex: -1, TransportIndex: -1, Terminal: &TerminalV3{Reason: reason, Message: message}}
}

// nativeEmbeddedSubtitleV3 is the native route for the embedded track at
// ordinal: the client draws it from the original stream it already plays.
// Planning a selected track and marking inventory rows native_selectable both
// ask this, so the two cannot disagree.
func nativeEmbeddedSubtitleV3(file *models.MediaFile, request StartRequestV3, deliveryClass string, ordinal, audioIndex int, facts SubtitleTrackFactsV3) (*EmbeddedSubtitleV3, NativeEmbeddedSubtitleCapabilityV3, bool) {
	var none NativeEmbeddedSubtitleCapabilityV3
	if file == nil || deliveryClass != DeliveryClassOriginalHTTPV3 || !HasFeatureV3(request.ClientFeatures, FeatureEmbeddedSubtitlesV3) || ordinal < 0 || ordinal >= len(file.SubtitleTracks) {
		return nil, none, false
	}
	delivery := request.ClientPlaybackContext.Deliveries[deliveryClass]
	if !delivery.Enabled || !delivery.SupportedOnDevice {
		return nil, none, false
	}
	track := file.SubtitleTracks[ordinal]
	// Teletext and unknown codecs have no validated client render path.
	if track.Index < 0 || !isTextSubtitleV3(track.Codec) && !isClientRenderableBitmapSubtitleV3(track.Codec) {
		return nil, none, false
	}
	if facts == nil {
		facts = UnknownSubtitleTrackFactsV3{}
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
		if !nativeTrackIdentityResolvableV3(capability.TrackIdentity, file, ordinal) {
			continue
		}
		if !nativeEmbeddedLimitationsAllowV3(capability.Limitations, file, track, audioIndex, facts) {
			continue
		}
		return &EmbeddedSubtitleV3{StreamIndex: track.Index, ContainerTrackID: track.ContainerTrackID}, capability, true
	}
	return nil, none, false
}

// nativeTrackIdentityResolvableV3 reports whether a client addressing tracks
// by identity can find exactly the track at ordinal.
func nativeTrackIdentityResolvableV3(identity string, file *models.MediaFile, ordinal int) bool {
	switch identity {
	case subtitleIdentityFFmpegV3:
		return true
	case subtitleIdentityContainerV3:
		track := file.SubtitleTracks[ordinal]
		id, err := strconv.ParseUint(track.ContainerTrackID, 10, 32)
		if err != nil || id == 0 || strconv.FormatUint(id, 10) != track.ContainerTrackID {
			return false
		}
		for i, other := range file.SubtitleTracks {
			if i != ordinal && other.ContainerTrackID == track.ContainerTrackID {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// nativeEmbeddedLimitationsAllowV3 reports whether a player with these
// limitations shows track as selected. A limitation this server does not know
// rules its entry out: the server cannot tell what that player gets wrong, and
// the track still has a sidecar or burn-in route.
func nativeEmbeddedLimitationsAllowV3(limitations []string, file *models.MediaFile, track models.SubtitleTrack, audioIndex int, facts SubtitleTrackFactsV3) bool {
	for _, limitation := range limitations {
		switch strings.ToLower(strings.TrimSpace(limitation)) {
		case NativeEmbeddedLimitationForcedUnlistedV3:
			if track.Forced {
				return false
			}
		case NativeEmbeddedLimitationHiddenTrackPreemptionV3:
			if hiddenSubtitleTrackPreemptsV3(file, audioIndex) {
				return false
			}
		case NativeEmbeddedLimitationLiteralOverrideTagsV3:
			if facts.OverrideTags(file, track) == SubtitleFactYesV3 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// hiddenSubtitleTrackPreemptsV3 reports whether file has an embedded track
// that a hidden_track_preemption player draws instead of any selection: a
// Forced track in the language of the audio the plan plays, or a
// default-flagged bitmap track. Roku OS 16 does both; a default-flagged text
// track does not preempt.
func hiddenSubtitleTrackPreemptsV3(file *models.MediaFile, audioIndex int) bool {
	audioLanguage := ""
	if index := playedAudioIndexV3(file, audioIndex); index >= 0 {
		audioLanguage = file.AudioTracks[index].Language
	}
	for _, track := range file.SubtitleTracks {
		if track.Default && isClientRenderableBitmapSubtitleV3(track.Codec) {
			return true
		}
		if track.Forced && SameTrackLanguageV3(track.Language, audioLanguage) {
			return true
		}
	}
	return false
}

// SameTrackLanguageV3 compares two track languages by primary subtag, so eng,
// en and en-US match. Values with no readable language, such as und, match
// only when spelled the same.
func SameTrackLanguageV3(a, b string) bool {
	if primaryA, primaryB := lang.PrimaryLanguage(a), lang.PrimaryLanguage(b); primaryA != "" || primaryB != "" {
		return primaryA == primaryB
	}
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && strings.EqualFold(a, b)
}

// markNativeSelectableV3 flags the embedded inventory rows the client may
// switch to on the stream it already plays: selecting one would give this
// plan, on its delivery and audio, a native embedded subtitle.
func markNativeSelectableV3(plan *PlanV3, file *models.MediaFile, input PlannerInputV3) {
	if plan.Delivery != DeliveryOriginalHTTPV3 || file == nil {
		return
	}
	var inventory []SubtitleInventoryItemV3
	for i, item := range plan.Subtitle.Inventory {
		if item.Source != SubtitleSourceEmbeddedV3 {
			continue
		}
		ordinal := item.CombinedIndex - len(file.ExternalSubtitles)
		if _, _, ok := nativeEmbeddedSubtitleV3(file, input.Request, DeliveryClassOriginalHTTPV3, ordinal, input.AudioTrackIndex, input.SubtitleTrackFacts); !ok {
			continue
		}
		// The planner's candidate plans share one inventory; mark a copy.
		if inventory == nil {
			inventory = slices.Clone(plan.Subtitle.Inventory)
		}
		inventory[i].NativeSelectable = true
	}
	if inventory != nil {
		plan.Subtitle.Inventory = inventory
	}
}

// SubtitleCodecFamilyV3 is the canonical FFmpeg name of the codec family a
// subtitle codec belongs to: subrip for srt, ass for ssa, webvtt for vtt,
// mov_text for tx3g, hdmv_pgs_subtitle for pgs; any other codec normalized.
func SubtitleCodecFamilyV3(codec string) string {
	if IsASS(normalizeCodecV3(codec)) {
		return subtitleFormatASS
	}
	return normalizeNativeSubtitleCodecV3(codec)
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
