package apiv2

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
)

// mdblistSources are the sources the MDBList plugin declares.
var mdblistSources = []models.RatingSourceDefinition{
	{Source: "rt_critic", Name: "RT", Label: "Rotten Tomatoes critics", Scale: 100, Percent: true},
	{Source: "rt_audience", Name: "RT Audience", Label: "Rotten Tomatoes audience", Scale: 100, Percent: true},
	{Source: "metacritic", Name: "Metacritic", Label: "Metacritic", Scale: 100},
}

func ratedDetail() *catalogpkg.ItemDetail {
	imdb, tmdb, critic, audience := 8.5, 8.25, 93, 95
	return &catalogpkg.ItemDetail{
		ContentID: "movie:back-to-the-future", Type: "movie", Title: "Back to the Future",
		RatingIMDB: &imdb, RatingTMDB: &tmdb, RatingRTCritic: &critic, RatingRTAudience: &audience,
		RatingSources: []catalogpkg.ItemRatingSourceInfo{{Source: "metacritic", Score: 87}},
	}
}

func TestCatalogItemDetailShowsIMDbAndTMDBByDefault(t *testing.T) {
	out := catalogItemDetailOf(ratedDetail(), ratingsources.Selection{})

	want := []CatalogRating{
		{Source: "imdb", Name: "IMDb", Score: 85, Display: "8.5"},
		{Source: "tmdb", Name: "TMDB", Score: 82.5, Display: "8.3"},
	}
	if !reflect.DeepEqual(out.Ratings, want) {
		t.Fatalf("ratings = %+v\nwant %+v", out.Ratings, want)
	}
	// Detail keeps the stored values: metadata editors read and edit them.
	if out.RatingRTCritic == nil || out.RatingRTAudience == nil {
		t.Fatal("item detail dropped its stored Rotten Tomatoes values")
	}
}

func TestCatalogItemDetailAddsTurnedOnSources(t *testing.T) {
	out := catalogItemDetailOf(ratedDetail(), ratingsources.NewSelection("rt_critic", "metacritic").WithDeclared(mdblistSources))

	var got []string
	for _, r := range out.Ratings {
		got = append(got, r.Name+" "+r.Display)
	}
	want := []string{"IMDb 8.5", "TMDB 8.3", "RT 93%", "Metacritic 87"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ratings = %q, want %q", got, want)
	}
}

func TestCatalogItemDetailRatingsAreNeverNull(t *testing.T) {
	out := catalogItemDetailOf(&catalogpkg.ItemDetail{ContentID: "movie:x", Type: "movie"}, ratingsources.Selection{})
	if out.Ratings == nil {
		t.Fatal("ratings is nil; clients tell an older server apart by the field's absence")
	}
}

// Cards feed poster badges, so they carry a Rotten Tomatoes score only when
// an administrator shows Rotten Tomatoes.
func TestCardsDropRatingsAnAdministratorHasNotTurnedOn(t *testing.T) {
	imdb, critic, audience := 8.5, 93, 95
	section := handlers.SectionItemView{ContentID: "movie:x", Type: "movie", RatingIMDB: &imdb, RatingRTCritic: &critic, RatingRTAudience: &audience}
	listing := handlers.CollectionItemView{ContentID: "movie:x", Type: "movie", RatingIMDB: &imdb, RatingRTCritic: &critic, RatingRTAudience: &audience}

	for name, card := range map[string]CatalogItem{
		"section": catalogItemOfSection(section, ratingsources.Selection{}),
		"listing": catalogItemOfListing(listing, ratingsources.Selection{}),
	} {
		if card.RatingIMDB == nil || card.RatingRTCritic != nil || card.RatingRTAudience != nil {
			t.Errorf("%s card: imdb=%v rt=%v audience=%v; want IMDb only", name, card.RatingIMDB, card.RatingRTCritic, card.RatingRTAudience)
		}
	}

	shown := ratingsources.NewSelection("rt_critic").WithDeclared(mdblistSources)
	card := catalogItemOfSection(section, shown)
	if card.RatingRTCritic == nil || card.RatingRTAudience != nil {
		t.Errorf("with RT critics on: rt=%v audience=%v; want the critic score only", card.RatingRTCritic, card.RatingRTAudience)
	}
}
