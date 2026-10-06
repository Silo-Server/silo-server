package apiv2

import (
	"encoding/json"
	"strings"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
)

// The native detail names the season of its play target so a client can open
// that season without fetching the episode first. The frozen v1 detail
// serializes catalog.ItemDetail directly and must not gain the field.
func TestCatalogItemDetailPlaySeasonNumber(t *testing.T) {
	season := 0
	detail := &catalogpkg.ItemDetail{
		ContentID: "series:1", Type: "series", Title: "Series",
		PlayContentID: "episode:s00e01", PlaySeasonNumber: &season,
	}

	out := catalogItemDetailOf(detail, ratingsources.Selection{})
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"play_season_number":0`) {
		t.Fatalf("v2 detail = %s, want play_season_number 0 beside play_content_id", body)
	}

	v1, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v1), "play_season_number") {
		t.Fatalf("v1 detail = %s, must not carry play_season_number", v1)
	}

	detail.PlayContentID, detail.PlaySeasonNumber = "", nil
	body, err = json.Marshal(catalogItemDetailOf(detail, ratingsources.Selection{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "play_season_number") {
		t.Fatalf("v2 detail without a target = %s, want no play_season_number", body)
	}
}
