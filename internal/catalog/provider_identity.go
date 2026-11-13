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
	for _, key := range contentid.AnchorPrecedence(itemType) {
		left, right := strings.TrimSpace(a[key]), strings.TrimSpace(b[key])
		if key == contentid.ProviderTMDB {
			left, right = NormalizeTMDBID(left), NormalizeTMDBID(right)
		}
		if left == "" || right == "" {
			continue
		}
		return !strings.EqualFold(left, right)
	}
	return false
}
