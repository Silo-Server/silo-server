package access

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ViewerPreferences are the canonical preferences needed while constructing
// an access scope. They are resolved together because this path runs on nearly
// every authenticated request and one candidate read can answer every key.
type ViewerPreferences struct {
	DisabledLibraryIDs        []int
	PreferredMetadataLanguage string
	MetadataLanguageOverrides map[string]string
	// NextUpMode is ui.next_up_mode. It rides along so home-section requests
	// can read it from the resolved scope instead of resolving it again.
	NextUpMode string
	// NextUpNewReleasesFirst is ui.next_up_new_releases_first, carried for the
	// same reason.
	NextUpNewReleasesFirst bool
	// HideWatchedItemsOnHome is home.hide_watched_items, carried so Home
	// requests read it from the scope too.
	HideWatchedItemsOnHome bool
	// CardAvailability reports that the profile's poster badges are on and
	// include the availability badge (ui.card_overlays_enabled and
	// ui.card_overlays, each falling back to the server-wide default). Card
	// reads count seasons and episodes only for such a profile.
	CardAvailability bool
	// Degraded reports that the read failed and the values are the contract
	// defaults rather than the profile's own.
	Degraded bool
}

// ResolveViewerPreferences resolves the profile's viewer-scope preferences in
// one canonical store read. Every key it reads is profile-scoped, so a request
// without a profile has none and costs no read. A failed read degrades to the
// contract defaults: hidden libraries are a browsing preference, not an access
// control.
//
// adminDefaults supplies the administrator's server-wide defaults for the keys
// that declare admin_default (the Next Up keys); nil resolves every default
// from the contract. Production callers pass the node's shared
// *config.AdminDefaults so Home, Jellyfin clients and the settings pages agree.
func ResolveViewerPreferences(
	ctx context.Context, store ViewerPreferenceReader, adminDefaults settingsresolve.ServerSettings, profileID string,
) ViewerPreferences {
	profileID = strings.TrimSpace(profileID)
	if store == nil || profileID == "" {
		return ViewerPreferences{}
	}

	preferences, err := ResolveViewerPreferencesStrict(ctx, store, adminDefaults, profileID)
	if err != nil {
		slog.WarnContext(ctx, "viewer preference resolution degraded", "component", "access", "profile_id", profileID, "error", err)
		return ViewerPreferences{Degraded: true}
	}
	return preferences
}

// ViewerPreferenceReader is the bounded settings read needed by viewer policy.
type ViewerPreferenceReader interface {
	settingsresolve.Store
}

// viewerPreferenceKeys are the settings ResolveViewerPreferencesStrict reads.
var viewerPreferenceKeys = []string{
	settingskeys.UiDisabledLibraryIds,
	settingskeys.CatalogMetadataLanguage,
	settingskeys.CatalogMetadataLanguageOverrides,
	settingskeys.UiNextUpMode,
	settingskeys.UiNextUpNewReleasesFirst,
	settingskeys.HomeHideWatchedItems,
	settingskeys.UiCardOverlays,
	settingskeys.UiCardOverlaysEnabled,
}

// ViewerPreferenceQuery is the candidate read that answers profileID's viewer
// preferences, for a caller that sends it in a batch of its own and hands the
// result to ResolveViewerPreferences as ViewerPreferenceRows. It reads every
// key the resolution can ask for, so its rows cover the resolution's own read.
func ViewerPreferenceQuery(profileID string) userstore.SettingResolutionQuery {
	return userstore.SettingResolutionQuery{
		Keys:       viewerPreferenceKeys,
		ProfileIDs: []string{strings.TrimSpace(profileID)},
	}
}

// ViewerPreferenceRows is a ViewerPreferenceReader over the result of a
// ViewerPreferenceQuery read earlier: its rows, or the error reading them.
type ViewerPreferenceRows struct {
	Rows []userstore.SettingValue
	Err  error
}

// ListSettingValuesForResolution answers with the rows read earlier. They
// cover any read of the same profile's viewer preferences.
func (r ViewerPreferenceRows) ListSettingValuesForResolution(
	context.Context, userstore.SettingResolutionQuery,
) ([]userstore.SettingValue, error) {
	return r.Rows, r.Err
}

// ResolveViewerPreferencesStrict retains canonical precedence but propagates
// dependency failures instead of weakening authority through a fallback.
func ResolveViewerPreferencesStrict(
	ctx context.Context, store ViewerPreferenceReader, adminDefaults settingsresolve.ServerSettings, profileID string,
) (ViewerPreferences, error) {
	contract, err := settingscontract.Load()
	if err != nil {
		return ViewerPreferences{}, err
	}
	values, err := settingsresolve.New(contract).WithAdminDefaults(adminDefaults).Resolve(ctx, store,
		settingsresolve.Context{ProfileID: profileID}, viewerPreferenceKeys, nil)
	if err != nil {
		return ViewerPreferences{}, err
	}

	var out ViewerPreferences
	var cardOverlays json.RawMessage
	var cardOverlaysEnabled *bool
	for _, value := range values {
		switch value.Key {
		case settingskeys.UiDisabledLibraryIds:
			out.DisabledLibraryIDs = parseLibraryIDList(value.Value)
		case settingskeys.CatalogMetadataLanguage:
			var language string
			if json.Unmarshal(value.Value, &language) == nil {
				out.PreferredMetadataLanguage = strings.TrimSpace(language)
			}
		case settingskeys.CatalogMetadataLanguageOverrides:
			out.MetadataLanguageOverrides = parseMetadataLanguageOverrides(value.Value)
		case settingskeys.UiNextUpMode:
			var mode string
			if json.Unmarshal(value.Value, &mode) == nil {
				out.NextUpMode = strings.TrimSpace(mode)
			}
		case settingskeys.UiNextUpNewReleasesFirst:
			_ = json.Unmarshal(value.Value, &out.NextUpNewReleasesFirst)
		case settingskeys.HomeHideWatchedItems:
			_ = json.Unmarshal(value.Value, &out.HideWatchedItemsOnHome)
		case settingskeys.UiCardOverlays:
			cardOverlays = value.Value
		case settingskeys.UiCardOverlaysEnabled:
			_ = json.Unmarshal(value.Value, &cardOverlaysEnabled)
		}
	}
	out.CardAvailability = cardAvailabilityEnabled(ctx, adminDefaults, cardOverlays, cardOverlaysEnabled)
	return out, nil
}

// Server settings holding the server-wide poster badge defaults. They are not
// contract admin defaults (the stored names predate the contract), so the
// resolver does not apply them; getOverlayConfig serves the same two values
// to clients.
const (
	OverlaysEnabledSettingKey     = "overlays.enabled"
	DefaultCardOverlaysSettingKey = "defaults.card_overlays"
)

// cardAvailabilityEnabled reports whether a client following the profile's
// poster badge preferences draws the availability badge. It applies the
// precedence clients do: the profile's own choice wins, and a profile that
// has not chosen (null) follows the server-wide default. The badge itself is
// off unless a preferences document turns it on.
func cardAvailabilityEnabled(
	ctx context.Context, adminDefaults settingsresolve.ServerSettings, overlays json.RawMessage, enabled *bool,
) bool {
	serverDefault := func(key string) string {
		if adminDefaults == nil {
			return ""
		}
		value, _ := adminDefaults.Get(ctx, key)
		return strings.TrimSpace(value)
	}
	// The server-wide switch is on unless it is stored as false.
	on := serverDefault(OverlaysEnabledSettingKey) != strconv.FormatBool(false)
	if enabled != nil {
		on = *enabled
	}
	if !on {
		return false
	}
	if isJSONNull(overlays) {
		overlays = json.RawMessage(serverDefault(DefaultCardOverlaysSettingKey))
	}
	return overlayDocumentEnables(overlays, "availability")
}

func isJSONNull(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

// overlayDocumentEnables reports whether a card-overlay preferences document
// turns the badge id on. The current document keeps badges under "items"; the
// first version was a flat map of badge ids, which clients still read.
func overlayDocumentEnables(raw json.RawMessage, id string) bool {
	if isJSONNull(raw) {
		return false
	}
	type badge struct {
		Enabled bool `json:"enabled"`
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	var items map[string]json.RawMessage
	if json.Unmarshal(doc["items"], &items) != nil || items == nil {
		items = doc
	}
	var item badge
	return json.Unmarshal(items[id], &item) == nil && item.Enabled
}

// OriginalMetadataLanguage is a private-use BCP 47 tag stored in
// catalog.metadata_language (or as an override target) to mean "resolve this
// media item's original language." Keeping the sentinel a valid language tag
// preserves the existing setting's wire type.
const OriginalMetadataLanguage = "x-silo-original"

// parseMetadataLanguageOverrides normalizes source aliases and target tag
// casing before the map reaches catalog serving. The contract only accepts
// canonical source keys from normal clients, but normalization also makes old
// or manually-authored rows deterministic.
func parseMetadataLanguageOverrides(raw json.RawMessage) map[string]string {
	var stored map[string]string
	if json.Unmarshal(raw, &stored) != nil || len(stored) == 0 {
		return nil
	}

	keys := make([]string, 0, len(stored))
	for source := range stored {
		keys = append(keys, source)
	}
	sort.Strings(keys)

	out := make(map[string]string, len(stored))
	for _, source := range keys {
		canonicalSource := lang.Canonical(source)
		if canonicalSource == "" {
			continue
		}
		// When aliases collapse (for example en and eng), the canonical key
		// wins because it sorts first and is never overwritten.
		if _, exists := out[canonicalSource]; exists {
			continue
		}
		target, ok := settingscontract.NormalizeLanguageTag(stored[source])
		if !ok {
			continue
		}
		out[canonicalSource] = target
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
