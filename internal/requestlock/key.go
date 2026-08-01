// Package requestlock defines the shared database advisory-lock identity used
// by request creation and virtual-media cleanup. It intentionally owns no
// database code so catalog and requests can use the same key without an import
// cycle.
package requestlock

import (
	"fmt"
	"strconv"
	"strings"
)

// MediaKey returns the strongest canonical external-ID key available. Callers
// skip the advisory lock when no external ID exists; using a synthetic zero ID
// would unnecessarily serialize every unidentified title on the server.
func MediaKey(mediaType string, tmdbID int, tvdbID, imdbID string) (string, bool) {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		return "", false
	}
	if tmdbID > 0 {
		return fmt.Sprintf("silo:request-media:%s:tmdb:%d", mediaType, tmdbID), true
	}
	if parsedTVDBID, err := strconv.Atoi(strings.TrimSpace(tvdbID)); err == nil && parsedTVDBID > 0 {
		return fmt.Sprintf("silo:request-media:%s:tvdb:%d", mediaType, parsedTVDBID), true
	}
	imdbID = strings.ToLower(strings.TrimSpace(imdbID))
	if imdbID != "" {
		return fmt.Sprintf("silo:request-media:%s:imdb:%s", mediaType, imdbID), true
	}
	return "", false
}
