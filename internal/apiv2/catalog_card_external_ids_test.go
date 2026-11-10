package apiv2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
)

// A browse card carries the external IDs so a client can match titles across
// servers without fetching every item's detail.
func TestCatalogListingCardCarriesExternalIDs(t *testing.T) {
	card := catalogItemOfListing(handlers.CollectionItemView{
		ContentID: "movie-1", Type: "movie", Title: "Heat",
		ImdbID: "tt0113277", TmdbID: "949", TvdbID: "81189",
	}, ratingsources.Selection{})
	body, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"imdb_id":"tt0113277"`, `"tmdb_id":"949"`, `"tvdb_id":"81189"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("card JSON lacks %s: %s", want, body)
		}
	}

	bare, err := json.Marshal(catalogItemOfListing(handlers.CollectionItemView{ContentID: "movie-2", Type: "movie", Title: "Untitled"}, ratingsources.Selection{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "_id\":\"\"") {
		t.Errorf("card without IDs emitted empty ID members: %s", bare)
	}
}

// The detail document now takes its IDs from the embedded card; it must still
// emit each exactly once with the detail's values.
func TestCatalogItemDetailEmitsExternalIDsOnce(t *testing.T) {
	d := &catalogpkg.ItemDetail{ContentID: "movie-1", Type: "movie", Title: "Heat", ImdbID: "tt0113277", TmdbID: "949", TvdbID: "81189"}
	body, err := json.Marshal(catalogItemDetailOf(d, ratingsources.Selection{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"imdb_id":"tt0113277"`, `"tmdb_id":"949"`, `"tvdb_id":"81189"`} {
		if n := strings.Count(string(body), want); n != 1 {
			t.Errorf("detail JSON has %s %d times, want 1: %s", want, n, body)
		}
	}
}
