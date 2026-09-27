package jellycompat

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Jellyfin provider keys. These strings are the contract: clients match on the
// exact casing (jellyfin-sdk-kotlin and the .NET SDK both index ProviderIds by
// these literals), so they are not free to change.
const (
	providerKeyIMDB = "Imdb"
	providerKeyTMDB = "Tmdb"
	providerKeyTVDB = "Tvdb"
)

// providerIDMap builds a Jellyfin ProviderIds map from the external ids Silo
// stores on an item, season, episode, or person. Empty ids are left out
// entirely rather than emitted as empty strings, because clients treat a
// present-but-empty id as a real one and match against it.
//
// TMDB ids get NormalizeTMDBID applied: media_items.tmdb_id holds TMDB's
// "id-slug" URL form ("1931-disney-s-adventures-of-the-gummi-bears") often
// enough that handing it to a client straight from the column produces an id
// no provider lookup resolves.
func providerIDMap(imdbID, tmdbID, tvdbID string) map[string]string {
	ids := map[string]string{}
	if v := strings.TrimSpace(imdbID); v != "" {
		ids[providerKeyIMDB] = v
	}
	if v := catalog.NormalizeTMDBID(tmdbID); v != "" {
		ids[providerKeyTMDB] = v
	}
	if v := strings.TrimSpace(tvdbID); v != "" {
		ids[providerKeyTVDB] = v
	}
	return ids
}
