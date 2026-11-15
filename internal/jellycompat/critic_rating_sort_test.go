package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
)

// criticSortContent records the browse parameters a list request reaches the
// catalog with.
type criticSortContent struct {
	stubContentService
	params url.Values
}

func (s *criticSortContent) BrowseItems(_ context.Context, _ *Session, params url.Values) (*upstreamBrowseResponse, error) {
	s.params = params
	return &upstreamBrowseResponse{Items: []upstreamListItem{}}, nil
}

// Jellyfin Web's Critic Rating sort and a minimum critic score reach the
// catalog while the administrator shows the Rotten Tomatoes critic score.
// While it is hidden they are dropped, so neither the order nor the filter
// reveals the score: the request sorts as Silo did before it knew the key.
func TestCriticRatingSortAndFilterFollowShownRatingSources(t *testing.T) {
	const grid = "/Items?Recursive=true&IncludeItemTypes=Movie" +
		"&SortBy=CriticRating,SortName,ProductionYear&SortOrder=Descending&MinCriticRating=60"
	for _, tc := range []struct {
		name                     string
		path                     string
		shown                    bool
		wantSort, wantOrder, min string
	}{
		{name: "shown", path: grid, shown: true, wantSort: catalog.BrowseSortRatingRTCritic, wantOrder: "desc", min: "60"},
		{name: "hidden", path: grid, wantSort: catalog.BrowseSortTitle, wantOrder: "asc"},
		// A minimum is a filter, so it browses the catalog rather than the
		// root views, as it did before; hidden, it narrows nothing.
		{name: "hidden minimum alone", path: "/Items?MinCriticRating=60", wantSort: catalog.BrowseSortCreatedAt, wantOrder: "desc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := ""
			if tc.shown {
				extra = models.RatingSourceRTCritic
			}
			policy := ratingsources.NewPolicy(stubSettingsReader{values: map[string]string{config.CatalogExtraRatingSourcesSettingKey: extra}},
				func(context.Context) ([]ratingsources.DeclaredSource, error) {
					return []ratingsources.DeclaredSource{{RatingSourceDefinition: models.RatingSourceDefinition{Source: models.RatingSourceRTCritic, Name: "RT", Scale: 100, Percent: true}}}, nil
				})
			sessions := NewSessionStore(time.Hour, time.Now)
			if err := sessions.Put(Session{Token: "critic", StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}); err != nil {
				t.Fatal(err)
			}
			content := &criticSortContent{}
			router := NewRouter(Dependencies{Config: &config.Config{}, IDCodec: NewResourceIDCodec(), SessionStore: sessions, ContentService: content, UserDataService: &mockUserDataService{}, RatingSources: policy})

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("X-Emby-Token", "critic")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if content.params == nil {
				t.Fatal("the request did not reach the catalog browse")
			}
			if got := content.params.Get("sort"); got != tc.wantSort {
				t.Errorf("sort = %q, want %q", got, tc.wantSort)
			}
			if got := content.params.Get("order"); got != tc.wantOrder {
				t.Errorf("order = %q, want %q", got, tc.wantOrder)
			}
			if got := content.params.Get("min_critic_rating"); got != tc.min {
				t.Errorf("min_critic_rating = %q, want %q", got, tc.min)
			}
		})
	}
}

// The minimum critic score reaches the catalog's browse filters.
func TestMinCriticRatingReachesCatalog(t *testing.T) {
	query := parseItemsQuery(httptest.NewRequest(http.MethodGet, "/Items?IncludeItemTypes=Movie&Recursive=true&MinCriticRating=60", nil), NewResourceIDCodec())
	browse := &stubBrowseSource{}
	svc := newDirectContentServiceForTest(browse, nil)
	if _, err := svc.BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, buildBrowseParams(query)); err != nil {
		t.Fatal(err)
	}
	if len(browse.calls) != 1 || browse.calls[0].filters.MinCriticRating != 60 {
		t.Fatalf("catalog calls: %+v", browse.calls)
	}
}
