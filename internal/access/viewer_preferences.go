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
// every authenticated request and one candidate read can answer every key.
type ViewerPreferences struct {
	DisabledLibraryIDs        []int
	PreferredMetadataLanguage string
	MetadataLanguageOverrides map[string]string
	// NextUpMode is ui.next_up_mode. It rides along so home-section requests
	// can read it from the resolved scope instead of resolving it again.
	NextUpMode string
}

// ResolveViewerPreferences resolves the profile's viewer-scope preferences in
// one canonical store read. Every key it reads is profile-scoped, so a request
// without a profile has none and costs no read. A failed read degrades to the
// contract defaults: hidden libraries are a browsing preference, not an access
// control.
func ResolveViewerPreferences(
	ctx context.Context, store userstore.UserStore, profileID string,
) ViewerPreferences {
	profileID = strings.TrimSpace(profileID)
	if store == nil || profileID == "" {
		return ViewerPreferences{}
	}

	preferences, err := ResolveViewerPreferencesStrict(ctx, store, profileID)
	if err != nil {
		slog.WarnContext(ctx, "viewer preference resolution degraded", "component", "access", "profile_id", profileID, "error", err)
		return ViewerPreferences{}
	}
	return preferences
}

// ViewerPreferenceReader is the bounded settings read needed by viewer policy.
type ViewerPreferenceReader interface {
	settingsresolve.Store
}

// ResolveViewerPreferencesStrict retains canonical precedence but propagates
// dependency failures instead of weakening authority through a fallback.
func ResolveViewerPreferencesStrict(ctx context.Context, store ViewerPreferenceReader, profileID string) (ViewerPreferences, error) {
	contract, err := settingscontract.Load()
	if err != nil {
		return ViewerPreferences{}, err
	}
	values, err := settingsresolve.New(contract).Resolve(ctx, store,
		settingsresolve.Context{ProfileID: profileID},
		[]string{
			settingskeys.UiDisabledLibraryIds,
			settingskeys.CatalogMetadataLanguage,
			settingskeys.CatalogMetadataLanguageOverrides,
			settingskeys.UiNextUpMode,
		}, nil)
	if err != nil {
		return ViewerPreferences{}, err
	}

	var out ViewerPreferences
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
		}
	}
	return out, nil
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
