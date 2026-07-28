package access

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
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
	contract, err := settingscontract.Load()
	if err != nil {
		slog.WarnContext(ctx, "metadata language resolution degraded to default: loading settings contract failed",
			"component", "access", "profile_id", profileID, "error", err)
		return ""
	}
	resolved, err := settingsresolve.New(contract).Resolve(ctx, store,
		settingsresolve.Context{ProfileID: profileID},
		[]string{settingskeys.CatalogMetadataLanguage}, nil)
	if err != nil {
		slog.WarnContext(ctx, "metadata language resolution degraded to default: reading setting values failed",
			"component", "access", "profile_id", profileID, "error", err)
		return ""
	}
	if len(resolved) == 0 {
		return ""
	}
	// The contract default is null — "no preference" — which unmarshals to "",
	// the same spelling the legacy column used for unset.
	var language string
	if json.Unmarshal(resolved[0].Value, &language) != nil {
		return ""
	}
	return strings.TrimSpace(language)
}
