package playback

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/stream"
)

// Subtitle source classes in the combined-ordinal space.
const (
	SubtitleSourceExternalV3   = "external"
	SubtitleSourceEmbeddedV3   = "embedded"
	SubtitleSourceDownloadedV3 = "downloaded"
	// subtitleCodecAribCaption is the ARIB caption codec, which has no
	// validated client render path.
	subtitleCodecAribCaption = "arib_caption"
)

// Sidecar file extensions the stream handler serves a subtitle track as. The
// extension is part of the published URL, so it belongs to the contract rather
// than to whichever call site happens to build a path.
const (
	SubtitleExtASSV3 = ".ass"
	SubtitleExtPGSV3 = ".sup"
	SubtitleExtSRTV3 = ".srt"
	SubtitleExtVTTV3 = ".vtt"
	// DownloadedSubtitleIDParamV3 pins a generated/downloaded sidecar URL to
	// its stable database row instead of resolving a mutable inventory ordinal.
	DownloadedSubtitleIDParamV3 = "downloaded_subtitle_id"
	// SubtitleOriginalParamV3=1 marks a .srt URL that asks for the original
	// SRT bytes. The frozen v1 route answers a bare .srt with WebVTT, so the
	// extension alone cannot opt in.
	SubtitleOriginalParamV3 = "original"
)

// WebVTT is the format every convert-mode subtitle lands in, so its format
// token and media type travel together with the extension above: a plan that
// advertises one of the three and serves another hands the client an artifact
// its parser rejects.
const (
	SubtitleFormatVTTV3 = "vtt"
	SubtitleMIMEVTTV3   = "text/vtt"
)

// Subtitle delivery classes describing how a track reaches the screen.
const (
	// SubtitleDeliverySidecarV3 marks a track the client can fetch directly
	// from its published URL.
	SubtitleDeliverySidecarV3 = "sidecar"
	// SubtitleDeliveryBurnInOnlyV3 marks a track with no client-fetchable
	// representation: DVD/DVB bitmap streams have no sidecar shape the stream
	// handler can serve, so server-side burn-in is the only route. The track
	// still occupies its combined ordinal and stays selectable; it carries no
	// URL.
	SubtitleDeliveryBurnInOnlyV3 = "burn_in_only"
)

// SubtitleInventoryItemV3 is one selectable subtitle track at its frozen
// combined ordinal.
type SubtitleInventoryItemV3 struct {
	TrackID       string `json:"track_id"`
	CombinedIndex int    `json:"combined_index"`
	Source        string `json:"source"`
	Codec         string `json:"codec,omitempty"`
	Language      string `json:"language,omitempty" doc:"Subtitle language: BCP 47 tag (en, pt-BR), empty when unknown; older rows may carry a legacy value"`
	Label         string `json:"label,omitempty"`
	Forced        bool   `json:"forced"`
	// Default marks the track the source container flags as its default
	// selection. Only embedded and external tracks can carry it.
	Default          bool   `json:"default"`
	HearingImpaired  bool   `json:"hearing_impaired"`
	Delivery         string `json:"delivery"`
	NativeSelectable bool   `json:"native_selectable,omitempty"`
	Unplayable       bool   `json:"unplayable,omitempty"`
	StreamIndex      *int   `json:"stream_index,omitempty"`
	ContainerTrackID string `json:"container_track_id,omitempty"`
	// URL is the session-scoped sidecar URL, present only on
	// SubtitleDeliverySidecarV3 tracks and only once a session exists.
	URL string `json:"url,omitempty" format:"uri-reference"`
	// FontBundleURL is the attachment bundle needed to render an embedded
	// ASS/SSA track with its authored typesetting.
	FontBundleURL string `json:"font_bundle_url,omitempty" format:"uri-reference"`
	// SyncKey names the track to the subtitle sync operations. Present on
	// external and downloaded tracks whose timing can be corrected (SRT,
	// WebVTT, ASS, SSA); stable across sessions and inventory reordering.
	SyncKey      string                           `json:"sync_key,omitempty"`
	HLSRendition *SubtitleInventoryHLSRenditionV3 `json:"hls_rendition,omitempty"`
	// downloadedSubtitleID is deliberately not serialized: clients address the
	// opaque session URL, while the server uses the row ID to make that URL
	// stable across inventory reordering and seek reanchors.
	downloadedSubtitleID int
	// sourceCombinedIndex is the ordinal this entry's track occupies in the
	// un-deduplicated source ranges: externals, then embedded, then downloaded.
	// The published CombinedIndex is dense, so the two differ whenever the
	// de-duplication below suppressed an earlier track. The stream route and
	// the selection resolver need the source ordinal to find the underlying
	// track; it is deliberately not serialized, because a plan restored from
	// JSON recovers the same mapping by re-running the de-duplication.
	sourceCombinedIndex int
	// sourceCombinedIndexSet distinguishes a genuine sourceCombinedIndex of 0
	// from the zero value of an item decoded from JSON.
	sourceCombinedIndexSet bool
}

// The returned items carry no URLs; use SubtitleInventoryV3 once a session
// exists.
func BuildSubtitleInventoryV3(file *models.MediaFile, additional []SubtitleInventoryEntryV3) []SubtitleInventoryItemV3 {
	if file == nil {
		return nil
	}
	items := make([]SubtitleInventoryItemV3, 0, len(file.ExternalSubtitles)+len(file.SubtitleTracks)+len(additional))
	seen := make(map[string]struct{})
	externalCount := len(file.ExternalSubtitles)
	base := externalCount + len(file.SubtitleTracks)
	for index, sub := range file.ExternalSubtitles {
		if subtitleInventoryDuplicateV3(seen, SubtitleSourceExternalV3, externalSubtitleIdentityV3(sub), sub.Format, sub.Language, sub.Forced, sub.HearingImpaired) {
			continue
		}
		item := subtitleInventoryItemV3(file.ID, len(items), index, SubtitleSourceExternalV3, sub.Format,
			sub.Language, firstNonEmptySubtitleLabelV3(sub.Title, sub.EmbeddedTitle, fspath.Display(filepath.Base(sub.Path)), sub.Language),
			sub.Forced, sub.Default, sub.HearingImpaired)
		if subtitles.SupportsRetime(subtitles.SubtitleFormat(sub.Format)) {
			item.SyncKey = subtitles.ExternalSyncKey(sub.Path)
		}
		items = append(items, item)
	}
	for index, track := range file.SubtitleTracks {
		if subtitleInventoryDuplicateV3(seen, SubtitleSourceEmbeddedV3, embeddedSubtitleIdentityV3(track), track.Codec, track.Language, track.Forced, track.HearingImpaired) {
			continue
		}
		item := subtitleInventoryItemV3(file.ID, len(items), externalCount+index, SubtitleSourceEmbeddedV3, track.Codec,
			track.Language, firstNonEmptySubtitleLabelV3(track.Title, track.EmbeddedTitle, track.Language),
			track.Forced, track.Default, track.HearingImpaired)
		streamIndex := track.Index
		item.StreamIndex = &streamIndex
		item.ContainerTrackID = track.ContainerTrackID
		item.Unplayable = normalizeCodecV3(track.Codec) == subtitleCodecAribCaption || normalizeCodecV3(track.Codec) == ""
		items = append(items, item)
	}
	for index, entry := range additional {
		source := entry.Source
		if source == "" {
			source = SubtitleSourceDownloadedV3
		}
		identity := ""
		if source == SubtitleSourceDownloadedV3 && entry.DownloadedSubtitleID > 0 {
			identity = strconv.Itoa(entry.DownloadedSubtitleID)
		}
		if subtitleInventoryDuplicateV3(seen, source, identity, entry.Codec, entry.Language, entry.Forced, entry.HearingImpaired) {
			continue
		}
		sourceIndex := entry.CombinedIndex
		if sourceIndex <= 0 {
			sourceIndex = base + index
		}
		item := subtitleInventoryItemV3(file.ID, len(items), sourceIndex, source, entry.Codec,
			entry.Language, firstNonEmptySubtitleLabelV3(entry.Label, entry.Language),
			entry.Forced, false, entry.HearingImpaired)
		item.downloadedSubtitleID = entry.DownloadedSubtitleID
		if source == SubtitleSourceDownloadedV3 && entry.DownloadedSubtitleID > 0 && subtitles.SupportsRetime(subtitles.SubtitleFormat(entry.Codec)) {
			item.SyncKey = subtitles.StoredSyncKey(entry.DownloadedSubtitleID)
		}
		items = append(items, item)
	}
	return items
}

// HideSidecarFileNamesV3 relabels the external sidecars of inventory for a
// viewer who may not see file names. A sidecar keeps its label only when that
// label is the title of the sidecar file currently holds at its ordinal; any
// other label, its file name or one left in a restored plan after the
// sidecars changed, is replaced. The replacement is the sidecar's embedded
// title, or its language numbered by models.SidecarLabels, as the catalog
// labels the same track.
func HideSidecarFileNamesV3(inventory []SubtitleInventoryItemV3, file *models.MediaFile) {
	labels := models.SidecarLabels{}
	for i, item := range inventory {
		if item.Source != SubtitleSourceExternalV3 {
			continue
		}
		var sub *models.ExternalSubtitle
		if file != nil && item.CombinedIndex >= 0 && item.CombinedIndex < len(file.ExternalSubtitles) {
			sub = &file.ExternalSubtitles[item.CombinedIndex]
		}
		switch {
		case sub != nil && !sub.TitleIsFileName() && item.Label == sub.Title:
			// Its own title: keep it.
		case sub != nil && sub.TitleIsFileName() && strings.TrimSpace(sub.EmbeddedTitle) != "":
			inventory[i].Label = sub.EmbeddedTitle
		default:
			inventory[i].Label = labels.Next(item.Language, item.Codec, item.Forced, item.HearingImpaired)
		}
	}
}

// SubtitleInventoryV3 returns the combined-ordinal inventory with
// session-scoped stream URLs attached to every sidecar-deliverable track. It
// knows no client features, so every URL takes its default representation.
func SubtitleInventoryV3(sessionID string, file *models.MediaFile, additional []SubtitleInventoryEntryV3) []SubtitleInventoryItemV3 {
	return ScopeSubtitleInventoryV3(sessionID, file, BuildSubtitleInventoryV3(file, additional), nil)
}

// ScopeSubtitleInventoryV3 attaches session URLs to an already planned
// inventory without resolving it again against mutable subtitle repositories.
// This keeps the advertised menu and the planner's selected ordinal on the
// same snapshot. clientFeatures selects feature-gated representations (see
// SubtitleSidecarExtV3).
func ScopeSubtitleInventoryV3(sessionID string, file *models.MediaFile, inventory []SubtitleInventoryItemV3, clientFeatures []string) []SubtitleInventoryItemV3 {
	// Inventory is required by the v3 wire contract and must always encode as
	// an array. Copy into a non-nil slice so an empty inventory remains []
	// instead of becoming JSON null while session URLs are attached.
	items := append([]SubtitleInventoryItemV3{}, inventory...)
	if sessionID == "" || file == nil {
		return items
	}
	// ownPrefix lazily rebuilds the file's external+embedded inventory so a
	// plan decoded from JSON (which drops the unexported source ordinals) can
	// still translate a published position back to the source track the pin
	// names. Downloaded entries always come last, so the file's own entries
	// occupy the same leading positions in both.
	var ownPrefix []SubtitleInventoryItemV3
	sourceIndexAt := func(i int) (int, bool) {
		if items[i].sourceCombinedIndexSet {
			return items[i].sourceCombinedIndex, true
		}
		if ownPrefix == nil {
			ownPrefix = BuildSubtitleInventoryV3(file, nil)
		}
		if i >= 0 && i < len(ownPrefix) && ownPrefix[i].Source == items[i].Source && ownPrefix[i].sourceCombinedIndexSet {
			return ownPrefix[i].sourceCombinedIndex, true
		}
		return 0, false
	}
	for i := range items {
		if items[i].Delivery != SubtitleDeliverySidecarV3 {
			continue
		}
		previousURL := items[i].URL
		ext := SubtitleSidecarExtV3(items[i].Codec, items[i].Source, clientFeatures)
		items[i].URL = SubtitleStreamURLV3(sessionID, items[i].CombinedIndex, ext, file.ID)
		if items[i].Source == SubtitleSourceDownloadedV3 && items[i].downloadedSubtitleID > 0 {
			items[i].URL = DownloadedSubtitleStreamURLV3(
				sessionID,
				items[i].CombinedIndex,
				ext,
				file.ID,
				items[i].downloadedSubtitleID,
			)
		}
		switch items[i].Source {
		case SubtitleSourceExternalV3:
			key := subtitleURLIdentityV3(previousURL, ExternalSubtitleKeyParamV3, file.ID)
			if key == "" {
				if source, ok := sourceIndexAt(i); ok && source >= 0 && source < len(file.ExternalSubtitles) {
					key = ExternalSubtitlePathKeyV3(file.ExternalSubtitles[source].Path)
				}
			}
			if key != "" {
				items[i].URL += "&" + ExternalSubtitleKeyParamV3 + "=" + key
			}
		case SubtitleSourceEmbeddedV3:
			items[i].FontBundleURL = SubtitleFontBundleURLV3(sessionID, items[i].CombinedIndex, items[i].Codec, file.ID)
			index := subtitleURLIdentityV3(previousURL, EmbeddedSubtitleStreamIndexParamV3, file.ID)
			if index == "" {
				if source, ok := sourceIndexAt(i); ok {
					ordinal := source - len(file.ExternalSubtitles)
					if ordinal >= 0 && ordinal < len(file.SubtitleTracks) {
						index = strconv.Itoa(file.SubtitleTracks[ordinal].Index)
					}
				}
			}
			if index != "" {
				identity := "&" + EmbeddedSubtitleStreamIndexParamV3 + "=" + index
				items[i].URL += identity
				if items[i].FontBundleURL != "" {
					items[i].FontBundleURL += identity
				}
			}
		}
	}
	return items
}

// SubtitleInventoryNeedsDownloadedIdentityV3 reports whether an inventory was
// decoded from its wire form and therefore lost the server-only row IDs needed
// to mint stable downloaded-subtitle URLs. Freshly planned inventories retain
// the IDs in memory; durable JSON plans are rebuilt from the repository.
func SubtitleInventoryNeedsDownloadedIdentityV3(items []SubtitleInventoryItemV3) bool {
	for _, item := range items {
		if item.Source == SubtitleSourceDownloadedV3 && item.downloadedSubtitleID <= 0 {
			return true
		}
	}
	return false
}

// SubtitleInventoryItemAtV3 finds the inventory entry at a combined ordinal.
func SubtitleInventoryItemAtV3(items []SubtitleInventoryItemV3, combinedIndex int) (SubtitleInventoryItemV3, bool) {
	for _, item := range items {
		if item.CombinedIndex == combinedIndex {
			return item, true
		}
	}
	return SubtitleInventoryItemV3{}, false
}

// SubtitleURLExtV3 returns the sidecar file extension the stream handler serves
// a subtitle codec as.
func SubtitleURLExtV3(codec string) string {
	switch {
	case IsASS(codec):
		return SubtitleExtASSV3
	case IsPGS(codec):
		return SubtitleExtPGSV3
	}
	return SubtitleExtVTTV3
}

// SubtitleSidecarExtV3 returns the extension a sidecar URL publishes for a
// track of the given codec and source. It is SubtitleURLExtV3, except that a
// client which negotiated subrip_sidecar_v1 receives external and downloaded
// SRT as the original file. Only those two sources hold original SRT bytes;
// an embedded track would need an extraction path of its own.
func SubtitleSidecarExtV3(codec, source string, clientFeatures []string) string {
	if IsSubRip(codec) && source != SubtitleSourceEmbeddedV3 && HasFeatureV3(clientFeatures, FeatureSubripSidecarV3) {
		return SubtitleExtSRTV3
	}
	return SubtitleURLExtV3(codec)
}

// SubtitleFeaturesForPlanV3 returns clientFeatures with subrip_sidecar_v1 set to
// match the SRT representation an already published inventory used, or
// unchanged when the inventory published no external or downloaded SRT URL.
// Re-scoping a plan with the result reproduces the URLs its client already
// holds, even when the attempt's features were negotiated by a server that
// did not know the feature.
func SubtitleFeaturesForPlanV3(inventory []SubtitleInventoryItemV3, clientFeatures []string) []string {
	published, original := PublishedSubRipRepresentationV3(inventory)
	if !published {
		return clientFeatures
	}
	features := WithoutFeatureV3(clientFeatures, FeatureSubripSidecarV3)
	if original {
		features = append(features, FeatureSubripSidecarV3)
	}
	return features
}

// PublishedSubRipRepresentationV3 reports how an inventory published its
// external and downloaded SRT tracks: published is false when it has none with
// a URL, and original tells .srt?original=1 apart from the WebVTT conversion.
// This, not the stored subrip_sidecar_v1 token, is what an attempt negotiated:
// a server that predates the feature stored unknown client features verbatim
// but still published WebVTT.
func PublishedSubRipRepresentationV3(inventory []SubtitleInventoryItemV3) (published, original bool) {
	for _, item := range inventory {
		if item.URL == "" || item.Source == SubtitleSourceEmbeddedV3 || !IsSubRip(item.Codec) {
			continue
		}
		path, _, _ := strings.Cut(item.URL, "?")
		return true, strings.HasSuffix(path, SubtitleExtSRTV3)
	}
	return false, false
}

// SubtitleStreamURLV3 builds the session-scoped sidecar URL for a combined
// ordinal. The ordinal in the path is the combined one, not the container's
// stream index: the stream handler resolves it back through the same ranges
// BuildSubtitleInventoryV3 assigns. ext comes from SubtitleSidecarExtV3.
func SubtitleStreamURLV3(sessionID string, combinedIndex int, ext string, fileID int) string {
	url := fmt.Sprintf("/stream/%s/subtitles/%d%s?file_id=%d", sessionID, combinedIndex, ext, fileID)
	if ext == SubtitleExtSRTV3 {
		url += "&" + SubtitleOriginalParamV3 + "=1"
	}
	return url
}

// DownloadedSubtitleStreamURLV3 binds the public combined ordinal to the
// stable downloaded-subtitle row selected when the plan was accepted.
func DownloadedSubtitleStreamURLV3(sessionID string, combinedIndex int, ext string, fileID, downloadedSubtitleID int) string {
	return SubtitleStreamURLV3(sessionID, combinedIndex, ext, fileID) +
		"&" + DownloadedSubtitleIDParamV3 + "=" + strconv.Itoa(downloadedSubtitleID)
}

// SubtitleFontBundleURLV3 builds the attachment-bundle URL for an embedded
// ASS/SSA track, or "" for any other codec.
func SubtitleFontBundleURLV3(sessionID string, combinedIndex int, codec string, fileID int) string {
	if !IsASS(codec) {
		return ""
	}
	return fmt.Sprintf("/stream/%s/subtitles/%d/fonts?file_id=%d", sessionID, combinedIndex, fileID)
}

// subtitleInventoryItemV3 builds one published entry. publishedIndex is the
// dense combined ordinal the client sees; sourceIndex is the ordinal the track
// occupies in the un-deduplicated source ranges and is retained for the route
// and selection mapping.
func subtitleInventoryItemV3(fileID, publishedIndex, sourceIndex int, source, codec, language, label string, forced, isDefault, hearingImpaired bool) SubtitleInventoryItemV3 {
	return SubtitleInventoryItemV3{
		TrackID:                TrackIDV3(fileID, "subtitle", publishedIndex),
		CombinedIndex:          publishedIndex,
		Source:                 source,
		Codec:                  codec,
		Language:               language,
		Label:                  label,
		Forced:                 forced,
		Default:                isDefault,
		HearingImpaired:        hearingImpaired,
		Delivery:               subtitleDeliveryClassV3(source, codec),
		sourceCombinedIndex:    sourceIndex,
		sourceCombinedIndexSet: true,
	}
}

// subtitleDeliveryClassV3 mirrors what the stream handler can actually serve:
// text tracks convert to WebVTT/ASS, and an embedded PGS track extracts
// losslessly to a .sup elementary stream. Every other bitmap shape — DVD/DVB
// embedded, and any bitmap arriving as an external or downloaded file — has no
// sidecar representation, so it is burn-in only.
func subtitleDeliveryClassV3(source, codec string) string {
	switch {
	case isTextSubtitleV3(codec):
		return SubtitleDeliverySidecarV3
	case source == SubtitleSourceEmbeddedV3 && IsPGS(codec):
		return SubtitleDeliverySidecarV3
	}
	return SubtitleDeliveryBurnInOnlyV3
}

func firstNonEmptySubtitleLabelV3(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// subtitleInventoryDuplicateV3 records an inventory entry's de-duplication key
// and reports whether an equivalent track was already seen.
//
// A bare alias — an entry carrying no per-track discriminator — collapses
// against an earlier bare alias with the same base language: the legacy
// regional-code/bare-base pair (EN-US beside ENG) describing one track. The
// first spelling wins the published slot; the survivor keeps its own language
// label and source ordinal.
//
// An entry carrying a per-track discriminator — the container stream index,
// the authored title when the container names the track, the stable sidecar
// path, or the downloaded row ID — collapses only against the exact same
// discriminator: the same track described twice (a regional code and its bare
// base for one stream index, a rescan that rewrote titles but renumbered
// nothing, one download row listed twice). It never collapses against a bare
// alias: the alias names no track, so it cannot prove it is this track.
// Concretely: untitled zh-Hans beside zh-Hant, two same-language rips on
// different stream indexes, two sidecars with different paths, two titled
// tracks, and two distinct download rows are all published. Entries with no
// language and no discriminator at all are never candidates, so
// unknown-language tracks that merely share a codec or flags are all
// published.
//
// The seen-set spans all three ranges, so a duplicate later in the combined
// list de-duplicates against an earlier one.
func subtitleInventoryDuplicateV3(seen map[string]struct{}, source, identity string, codec, language string, forced, hearingImpaired bool) bool {
	base := stream.CanonicalLanguageBase(language)
	if base == "" && identity == "" {
		return false
	}
	// The alias key deliberately drops the discriminator: it is how two bare
	// spellings of one track meet. The track key keeps it: it is how two
	// physical tracks stay apart.
	aliasKey := "alias\x00" + strings.Join([]string{
		source,
		normalizeCodecV3(codec),
		base,
		strconv.FormatBool(forced),
		strconv.FormatBool(hearingImpaired),
	}, "\x00")
	if identity == "" {
		// Bare alias: collapse only against an earlier bare alias for the
		// same track (the second spelling). It never claims a track key, so
		// a later physical track with the same language still publishes.
		if _, ok := seen[aliasKey]; ok {
			return true
		}
		seen[aliasKey] = struct{}{}
		return false
	}
	trackKey := "track\x00" + identity + "\x00" + aliasKey
	if _, ok := seen[trackKey]; ok {
		// Exact same physical track seen before.
		return true
	}
	seen[trackKey] = struct{}{}
	return false
}

// externalSubtitleIdentityV3 returns a positive per-track discriminator for a
// sidecar subtitle: its stable path. Two sidecars in the same language are
// distinct physical tracks even when their codec and flags coincide; without
// the path the second would collapse into the first and vanish from the menu.
// A sidecar with no path carries no positive identity and keeps the legacy
// language-keyed behavior.
func externalSubtitleIdentityV3(sub models.ExternalSubtitle) string {
	if path := strings.TrimSpace(sub.Path); path != "" {
		return "path:" + path
	}
	return ""
}

// embeddedSubtitleIdentityV3 returns a positive per-track discriminator for an
// embedded container subtitle, so genuinely distinct tracks survive the
// inventory de-duplication while the same track described twice still collapses.
//
// The identity always carries the container stream index (SubtitleTrack.Index
// — the ffprobe stream index; zero when the probe recorded none, in which
// case two untitled zero-index tracks share a key exactly as before): two
// streams at different indexes are different physical tracks even when they
// share a title, codec, language, and flags.
// The authored title (the raw embedded title, or the stored title when the
// probe recorded one) is appended when the container names the track: two
// entries for one stream that disagree only on title stay distinct, because a
// title rewrite without renumbering is rarer than two same-language streams
// sharing an index namespace, and merging on index alone would reintroduce
// the over-collapse for titled tracks. Two untitled tracks that share a base
// language (zh-Hans beside zh-Hant, or two same-language rips) therefore
// differ by index and are both published; two same-titled tracks on different
// indexes differ too.
func embeddedSubtitleIdentityV3(track models.SubtitleTrack) string {
	if title := strings.TrimSpace(firstNonEmptySubtitleLabelV3(track.EmbeddedTitle, track.Title)); title != "" {
		return "stream:" + strconv.Itoa(track.Index) + "\x00title:" + title
	}
	return "stream:" + strconv.Itoa(track.Index)
}

// DownloadedSubtitleIDV3 returns the durable downloaded-subtitle row id an
// inventory entry was built from, or zero for tracks that are not downloaded.
// A selection carry or remap uses it as the only stable identity for a
// downloaded track, since its language/format/label can collide across rows.
func (i SubtitleInventoryItemV3) DownloadedSubtitleIDV3() int {
	return i.downloadedSubtitleID
}

// SubtitleInventoryOwnSourceIndexV3 maps a published combined ordinal to the
// source-space ordinal of the file's own external or embedded track it names.
// It reports false for an out-of-range ordinal or one that addresses a
// downloaded entry (whose identity is a stable row ID, not a source slot). It
// is the accessor a boundary that still indexes file.ExternalSubtitles or
// file.SubtitleTracks directly must use to translate a dense published ordinal
// back to the array slot its identity and extraction pins are built from.
func SubtitleInventoryOwnSourceIndexV3(file *models.MediaFile, publishedIndex int) (int, bool) {
	if file == nil || publishedIndex < 0 {
		return 0, false
	}
	own := BuildSubtitleInventoryV3(file, nil)
	if publishedIndex >= len(own) {
		return 0, false
	}
	item := own[publishedIndex]
	if !item.sourceCombinedIndexSet {
		return 0, false
	}
	return item.sourceCombinedIndex, true
}

// SubtitleInventoryOwnPublishedIndexV3 is the inverse of
// SubtitleInventoryOwnSourceIndexV3: it maps a source-space ordinal of the
// file's own external or embedded track to the dense published combined ordinal
// the client sees. It reports false for an out-of-range source ordinal, a
// downloaded entry, or a track the de-duplication suppressed (which has no
// published ordinal of its own). A caller that finds an equivalent target track
// in the source arrays must translate it through this before writing an ordinal
// a plan or client will interpret as published.
func SubtitleInventoryOwnPublishedIndexV3(file *models.MediaFile, sourceIndex int) (int, bool) {
	if file == nil || sourceIndex < 0 {
		return 0, false
	}
	for _, item := range BuildSubtitleInventoryV3(file, nil) {
		if item.sourceCombinedIndexSet && item.sourceCombinedIndex == sourceIndex {
			return item.CombinedIndex, true
		}
	}
	return 0, false
}

// subtitleInventorySourceIndexV3 maps a published combined ordinal to the
// source-space ordinal of the track it names, including downloaded entries. It
// rebuilds the same de-duplicated inventory the plan published, so the mapping
// matches the client's view exactly.
func subtitleInventorySourceIndexV3(file *models.MediaFile, additional []SubtitleInventoryEntryV3, publishedIndex int) (int, bool) {
	for _, item := range BuildSubtitleInventoryV3(file, additional) {
		if item.CombinedIndex == publishedIndex {
			return item.sourceCombinedIndex, item.sourceCombinedIndexSet
		}
	}
	return 0, false
}
