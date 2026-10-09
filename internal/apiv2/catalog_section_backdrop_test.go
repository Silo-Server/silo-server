package apiv2

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
)

func TestCatalogItemOfSectionCarriesSeriesBackdrop(t *testing.T) {
	card := catalogItemOfSection(handlers.SectionItemView{
		Type:                    "episode",
		BackdropURL:             "still",
		SeriesBackdropURL:       "series",
		SeriesBackdropThumbhash: "series-hash",
	}, ratingsources.Selection{})
	if card.BackdropURL != "still" || card.SeriesBackdropURL != "series" || card.SeriesBackdropThumbhash != "series-hash" {
		t.Fatalf("card backdrops = %q, %q, %q; want still, series, series-hash", card.BackdropURL, card.SeriesBackdropURL, card.SeriesBackdropThumbhash)
	}
}
