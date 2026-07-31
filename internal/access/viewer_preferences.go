package access

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ViewerPreferences are the canonical preferences needed while constructing
// an access scope. They are resolved together because this path runs on nearly
// every authenticated request and one candidate read can answer both keys.
type ViewerPreferences struct {
	DisabledLibraryIDs        []int
	PreferredMetadataLanguage string
	MetadataLanguageOverrides map[string]string
}

// ResolveViewerPreferences resolves the profile's viewer-scope preferences in
// one canonical store read. The legacy disabled_library_ids account setting is
// consulted only when no canonical row decided that value.
func ResolveViewerPreferences(
	ctx context.Context, store userstore.UserStore, profileID string,
) ViewerPreferences {
	profileID = strings.TrimSpace(profileID)
	if store == nil {
		return ViewerPreferences{}
	}
	if profileID == "" {
		return ViewerPreferences{DisabledLibraryIDs: legacyDisabledLibraryIDs(ctx, store)}
	}

	resolved, ok := resolveCanonicalViewerPreferences(ctx, store, profileID)
	if !ok || !resolved.disabledLibraryIDsSet {
		resolved.preferences.DisabledLibraryIDs = legacyDisabledLibraryIDs(ctx, store)
	}
	return resolved.preferences
}

type canonicalViewerPreferences struct {
	preferences           ViewerPreferences
	disabledLibraryIDsSet bool
}

func resolveCanonicalViewerPreferences(
	ctx context.Context, store userstore.UserStore, profileID string,
) (canonicalViewerPreferences, bool) {
	contract, err := settingscontract.Load()
	if err != nil {
		slog.WarnContext(ctx, "viewer preference resolution degraded: loading settings contract failed",
			"component", "access", "profile_id", profileID, "error", err)
		return canonicalViewerPreferences{}, false
	}
	values, err := settingsresolve.New(contract).Resolve(ctx, store,
		settingsresolve.Context{ProfileID: profileID},
		[]string{
			settingskeys.UiDisabledLibraryIds,
			settingskeys.CatalogMetadataLanguage,
			settingskeys.CatalogMetadataLanguageOverrides,
		}, nil)
	if err != nil {
		slog.WarnContext(ctx, "viewer preference resolution degraded: reading setting values failed",
			"component", "access", "profile_id", profileID, "error", err)
		return canonicalViewerPreferences{}, false
	}

	var out canonicalViewerPreferences
	for _, value := range values {
		switch value.Key {
		case settingskeys.UiDisabledLibraryIds:
			out.disabledLibraryIDsSet = value.Source != settingscontract.ScopeDefault
			if out.disabledLibraryIDsSet {
				out.preferences.DisabledLibraryIDs = parseLibraryIDList(value.Value)
			}
		case settingskeys.CatalogMetadataLanguage:
			var language string
			if json.Unmarshal(value.Value, &language) == nil {
				out.preferences.PreferredMetadataLanguage = strings.TrimSpace(language)
			}
		case settingskeys.CatalogMetadataLanguageOverrides:
			out.preferences.MetadataLanguageOverrides = parseMetadataLanguageOverrides(value.Value)
		}
	}
	return out, true
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

func legacyDisabledLibraryIDs(ctx context.Context, store userstore.UserStore) []int {
	raw, err := store.GetSetting(ctx, settingKeyDisabledLibraryIDs)
	if err != nil || raw == "" {
		return nil
	}
	return parseLibraryIDList(json.RawMessage(raw))
}
