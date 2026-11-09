package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The list card carries the external IDs to the v2 renderer, but /api/v1 is
// frozen and must not gain them.
func TestItemListResponseCarriesExternalIDsOnlyToV2(t *testing.T) {
	resp := itemListResponseShell(&models.MediaItem{
		ContentID: "movie-1", Type: "movie", Title: "Heat", Status: "matched",
		ImdbID: "tt0113277", TmdbID: "949", TvdbID: "81189",
	}, nil, nil)
	if resp.ImdbID != "tt0113277" || resp.TmdbID != "949" || resp.TvdbID != "81189" {
		t.Fatalf("external IDs not carried: %+v", resp)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"imdb_id", "tmdb_id", "tvdb_id"} {
		if strings.Contains(string(body), key) {
			t.Errorf("v1 list JSON gained %s: %s", key, body)
		}
	}
}
