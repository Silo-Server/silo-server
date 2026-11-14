package catalog

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/contentid"
)

// IdentityProviderIDsContradict reports whether two ID sets of itemType name
// different titles: they disagree on the first ID both carry, in the order
// the type's content IDs anchor (contentid.AnchorPrecedence). For a movie
// that is TMDB, IMDb, then TVDB, whose movie mappings are the cross-IDs
// providers most often get wrong; for a series it is TVDB, TMDB, then IMDb.
// Later IDs do not decide, so a movie whose TVDB mapping changed since it was
// matched still matches on its TMDB ID, and a series whose TMDB mapping
// changed still matches on its TVDB ID. A stored TMDB ID in TMDB's "id-slug"
// URL form is the same ID as its number. Matching uses it to accept an
// existing item for a file's tags; the scanner uses it to tell whether a
// renamed file's tags still resolve to the title it is linked to.
func IdentityProviderIDsContradict(itemType string, a, b map[string]string) bool {
	return identityProviderIDSetsContradict(itemType, providerIDSet(a), providerIDSet(b))
}

// identityProviderIDSetsContradict is IdentityProviderIDsContradict for sides
// that may carry several IDs per provider (a title's current and former IDs,
// an item's column and durable rows): at the first provider both carry, they
// name the same title when any ID on one side equals one on the other.
func identityProviderIDSetsContradict(itemType string, a, b map[string][]string) bool {
	for _, key := range contentid.AnchorPrecedence(itemType) {
		if len(a[key]) == 0 || len(b[key]) == 0 {
			continue
		}
		for _, left := range a[key] {
			for _, right := range b[key] {
				if sameProviderID(key, left, right) {
					return false
				}
			}
		}
		return true
	}
	return false
}

func sameProviderID(provider, left, right string) bool {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if provider == contentid.ProviderTMDB {
		left, right = NormalizeTMDBID(left), NormalizeTMDBID(right)
	}
	return strings.EqualFold(left, right)
}

// providerIDSet turns one ID per provider into identityProviderIDSetsContradict's
// form, dropping blank IDs.
func providerIDSet(ids map[string]string) map[string][]string {
	out := make(map[string][]string, len(ids))
	for provider, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out[provider] = []string{id}
		}
	}
	return out
}
