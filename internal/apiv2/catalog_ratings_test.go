package apiv2

import (
	"context"
	"net/http"
	"testing"

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
