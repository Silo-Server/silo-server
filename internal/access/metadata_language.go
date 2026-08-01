package access

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// PreferredMetadataLanguage resolves catalog.metadata_language canonically for
// one profile: the stored profile-scope value, else the contract default. The
// legacy user_profiles.preferred_metadata_language column is deliberately not
// consulted — it migrated to the canonical store, and reading both would let
// them disagree.
//
// Resolution is unconstrained on purpose. The manifest gives this key no
// constrained_by because the policy input that could constrain it
// (profile_preferred_metadata_language) is populated from this very
// preference; a constraint here would be circular. See the key's notes in
// contracts/settings/v1/manifest.json.
//
// A resolution failure degrades to "" — the contract default, meaning "inherit
// the library's metadata language" — rather than failing scope resolution: the
// language is a presentation preference, not an access boundary. The failure
// itself is logged, though: before the cutover this value rode on the profile
// row whose load failure was a hard error, and a store outage that silently
// degrades every profile's metadata language would otherwise be
// indistinguishable from "nobody set a preference".
func PreferredMetadataLanguage(ctx context.Context, store userstore.UserStore, profileID string) string {
	if store == nil || profileID == "" {
		return ""
	}
	resolved, _ := resolveCanonicalViewerPreferences(ctx, store, profileID)
	return resolved.preferences.PreferredMetadataLanguage
}
