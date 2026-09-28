package playback

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Subtitle source classes in the combined-ordinal space.
const (
	SubtitleSourceExternalV3   = "external"
	SubtitleSourceEmbeddedV3   = "embedded"
	SubtitleSourceDownloadedV3 = "downloaded"
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
	Language      string `json:"language,omitempty"`
	Label         string `json:"label,omitempty"`
	Forced        bool   `json:"forced"`
	// Default marks the track the source container flags as its default
	// selection. Only embedded and external tracks can carry it.
	Default         bool   `json:"default"`
	HearingImpaired bool   `json:"hearing_impaired"`
	Delivery        string `json:"delivery"`
	// URL is the session-scoped sidecar URL, present only on
	// SubtitleDeliverySidecarV3 tracks and only once a session exists.
	URL string `json:"url,omitempty"`
	// FontBundleURL is the attachment bundle needed to render an embedded
	// ASS/SSA track with its authored typesetting.
	FontBundleURL string `json:"font_bundle_url,omitempty"`
	// downloadedSubtitleID is deliberately not serialized: clients address the
	// opaque session URL, while the server uses the row ID to make that URL
	// stable across inventory reordering and seek reanchors.
	downloadedSubtitleID int
}

// BuildSubtitleInventoryV3 is the single normative implementation of the
// combined-ordinal ordering rule, and the only place in the server that
// assigns subtitle ordinals.
//
// Ordinals are dense and gap-free across three consecutive ranges, in this
// order:
//
//	[0, len(ExternalSubtitles))                       external sidecar files
//	[len(External), len(External)+len(SubtitleTracks)) embedded container tracks
//	[that, +len(additional))                           downloaded/generated tracks
//
// Every track occupies an ordinal, including bitmap tracks that have no
// client-fetchable sidecar: a track that cannot be delivered as a sidecar is
// published with SubtitleDeliveryBurnInOnlyV3 and no URL rather than omitted.
// Omitting it would leave a hole in the sequence, and any client deriving the
// downloaded-track base by counting published tracks would then undercount and
// address the wrong track.
//
// Within each range the order is the source order: catalog order for
// externals, container stream order for embedded tracks, and
// ListDownloadedSubtitles' created_at order for downloaded ones. Ordinals are
// therefore stable for as long as the file's track set is, which is what makes
// the `file:{id}:subtitle:{ordinal}` identity meaningful.
//
// The returned items carry no URLs; use SubtitleInventoryV3 once a session
// exists.
func BuildSubtitleInventoryV3(file *models.MediaFile, additional []SubtitleInventoryEntryV3) []SubtitleInventoryItemV3 {
	if file == nil {
		return nil
	}
	items := make([]SubtitleInventoryItemV3, 0, len(file.ExternalSubtitles)+len(file.SubtitleTracks)+len(additional))
	for _, sub := range file.ExternalSubtitles {
		items = append(items, subtitleInventoryItemV3(file.ID, len(items), SubtitleSourceExternalV3, sub.Format,
			sub.Language, firstNonEmptySubtitleLabelV3(sub.Title, sub.EmbeddedTitle, filepath.Base(sub.Path), sub.Language),
			sub.Forced, sub.Default, sub.HearingImpaired))
	}
	for _, track := range file.SubtitleTracks {
		items = append(items, subtitleInventoryItemV3(file.ID, len(items), SubtitleSourceEmbeddedV3, track.Codec,
			track.Language, firstNonEmptySubtitleLabelV3(track.Title, track.EmbeddedTitle, track.Language),
			track.Forced, track.Default, track.HearingImpaired))
	}
	for _, entry := range additional {
		source := entry.Source
		if source == "" {
			source = SubtitleSourceDownloadedV3
		}
		item := subtitleInventoryItemV3(file.ID, len(items), source, entry.Codec,
			entry.Language, firstNonEmptySubtitleLabelV3(entry.Label, entry.Language),
			entry.Forced, false, entry.HearingImpaired)
		item.downloadedSubtitleID = entry.DownloadedSubtitleID
		items = append(items, item)
	}
	return items
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
			if index := items[i].CombinedIndex; key == "" && index >= 0 && index < len(file.ExternalSubtitles) {
				key = ExternalSubtitlePathKeyV3(file.ExternalSubtitles[index].Path)
			}
			if key != "" {
				items[i].URL += "&" + ExternalSubtitleKeyParamV3 + "=" + key
			}
		case SubtitleSourceEmbeddedV3:
			items[i].FontBundleURL = SubtitleFontBundleURLV3(sessionID, items[i].CombinedIndex, items[i].Codec, file.ID)
			index := subtitleURLIdentityV3(previousURL, EmbeddedSubtitleStreamIndexParamV3, file.ID)
			if ordinal := items[i].CombinedIndex - len(file.ExternalSubtitles); index == "" && ordinal >= 0 && ordinal < len(file.SubtitleTracks) {
				index = strconv.Itoa(file.SubtitleTracks[ordinal].Index)
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

func subtitleInventoryItemV3(fileID, combinedIndex int, source, codec, language, label string, forced, isDefault, hearingImpaired bool) SubtitleInventoryItemV3 {
	return SubtitleInventoryItemV3{
		TrackID:         TrackIDV3(fileID, "subtitle", combinedIndex),
		CombinedIndex:   combinedIndex,
		Source:          source,
		Codec:           codec,
		Language:        language,
		Label:           label,
		Forced:          forced,
		Default:         isDefault,
		HearingImpaired: hearingImpaired,
		Delivery:        subtitleDeliveryClassV3(source, codec),
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
