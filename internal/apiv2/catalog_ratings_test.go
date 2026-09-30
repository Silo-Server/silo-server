package apiv2

import (
	"context"
	"net/http"
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
}

// A viewer who does not curate metadata gets only the ratings clients show,
// on every member of the detail, so a client that reads rating_rt_critic or
// rating_sources directly still follows the administrator's choice.
func TestCatalogItemDetailHidesSourcesAnAdministratorHasNotTurnedOn(t *testing.T) {
	out := catalogItemDetailOf(ratedDetail(), ratingsources.Selection{})
	if out.RatingIMDB == nil || out.RatingRTCritic != nil || out.RatingRTAudience != nil || out.RatingSources != nil {
		t.Fatalf("imdb=%v rt=%v audience=%v sources=%+v; want IMDb and TMDB only", out.RatingIMDB, out.RatingRTCritic, out.RatingRTAudience, out.RatingSources)
	}

	out = catalogItemDetailOf(ratedDetail(), ratingsources.NewSelection("metacritic").WithDeclared(mdblistSources))
	if len(out.RatingSources) != 1 || out.RatingSources[0].Source != "metacritic" || out.RatingRTCritic != nil {
		t.Fatalf("with Metacritic on: rt=%v sources=%+v; want the Metacritic row only", out.RatingRTCritic, out.RatingSources)
	}
}

// The metadata editor reads the detail, so a curator gets every stored rating.
func TestCatalogItemDetailKeepsStoredRatingsForCurators(t *testing.T) {
	d := ratedDetail()
	d.ViewerCurates = true
	out := catalogItemDetailOf(d, ratingsources.Selection{})
	if out.RatingRTCritic == nil || out.RatingRTAudience == nil || len(out.RatingSources) != 1 {
		t.Fatalf("rt=%v audience=%v sources=%+v; want every stored rating", out.RatingRTCritic, out.RatingRTAudience, out.RatingSources)
	}
	if len(out.Ratings) != 2 {
		t.Fatalf("ratings = %+v; a curator's title page still shows only IMDb and TMDB", out.Ratings)
	}
}

func TestCatalogItemDetailAddsTurnedOnSources(t *testing.T) {
	out := catalogItemDetailOf(ratedDetail(), ratingsources.NewSelection("rt_critic", "metacritic").WithDeclared(mdblistSources))

	var got []string
	for _, r := range out.Ratings {
		got = append(got, r.Name+" "+r.Display)
	}
	want := []string{"IMDb 8.5", "TMDB 8.3", "RT 93%"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ratings = %q, want %q", got, want)
	}
}

func TestCatalogItemDetailRatingsAreNeverNull(t *testing.T) {
	out := catalogItemDetailOf(&catalogpkg.ItemDetail{ContentID: "movie:x", Type: "movie"}, ratingsources.Selection{})
	if out.Ratings == nil {
		t.Fatal("ratings is nil; the member is empty, never null")
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

// A stored row of a source no plugin declares any more, or one the
// administrator has not turned on, stays out of rating_sources, and the rest
// follow the order of ratings.
func TestCatalogItemDetailRatingSourcesFollowTheSelection(t *testing.T) {
	d := ratedDetail()
	d.RatingSources = []catalogpkg.ItemRatingSourceInfo{
		{Source: "letterboxd", Score: 84},
		{Source: "metacritic", Score: 87},
		{Source: "rt_critic", Score: 93},
		{Source: "tmdb", Score: 82.5},
	}
	var got []string
	for _, source := range catalogItemDetailOf(d, ratingsources.NewSelection("metacritic", "rt_critic", "letterboxd").WithDeclared(mdblistSources)).RatingSources {
		got = append(got, source.Source)
	}
	if want := []string{"tmdb", "rt_critic", "metacritic"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rating_sources = %q, want %q", got, want)
	}
	if out := catalogItemDetailOf(d, ratingsources.Selection{}); len(out.RatingSources) != 1 {
		t.Fatalf("rating_sources = %+v, want TMDB only by default", out.RatingSources)
	}
}

// ratingSettings answers catalog.extra_rating_sources with its value.
type ratingSettings string

func (s ratingSettings) Get(context.Context, string) (string, error) { return string(s), nil }

// declaringMDBList lists mdblistSources as the MDBList plugin's declarations.
func declaringMDBList(context.Context) ([]ratingsources.DeclaredSource, error) {
	out := make([]ratingsources.DeclaredSource, 0, len(mdblistSources))
	for _, definition := range mdblistSources {
		out = append(out, ratingsources.DeclaredSource{RatingSourceDefinition: definition, Provider: "MDBList"})
	}
	return out, nil
}

// A browse sorted by a Rotten Tomatoes score the cards leave out would rank
// titles by a hidden number, so it orders as if no sort was asked for.
func TestListCatalogItemsIgnoresASortByAHiddenRating(t *testing.T) {
	deps, fake := catalogDeps(t)
	deps.RatingSources = ratingsources.NewPolicy(ratingSettings("rt_audience"), declaringMDBList)
	h := newTestHandler(t, deps)

	if rec := do(t, h, http.MethodGet, "/api/v2/catalog?sort=-rating_rt_critic", "", viewerHeaders()); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if fake.lastReq.Query.Sort.Field == "rating_rt_critic" {
		t.Fatalf("seam sort = %+v; want the hidden critic score dropped", fake.lastReq.Query.Sort)
	}

	if rec := do(t, h, http.MethodGet, "/api/v2/catalog?sort=-rating_rt_audience", "", viewerHeaders()); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if got := fake.lastReq.Query.Sort; got.Field != "rating_rt_audience" || got.Order != "desc" {
		t.Fatalf("seam sort = %+v; want the shown audience score kept", got)
	}
}

func TestRatingsCapabilityListsTheShownSources(t *testing.T) {
	deps, _ := catalogDeps(t)
	deps.RatingSources = ratingsources.NewPolicy(ratingSettings("rt_critic"), declaringMDBList)
	rec := do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/capabilities/ratings", "", viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	var body RatingsCapability
	decodeJSON(t, rec.Body, &body)
	want := []RatingsCapabilitySource{{Source: "imdb", Name: "IMDb"}, {Source: "tmdb", Name: "TMDB"}, {Source: "rt_critic", Name: "RT"}}
	if body.State != StateAvailable || !reflect.DeepEqual(body.Sources, want) {
		t.Fatalf("capability = %+v, want available with %+v", body, want)
	}
}
