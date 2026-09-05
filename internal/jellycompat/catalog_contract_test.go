package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestItemsResponseImageAndUserDataControls(t *testing.T) {
	q := parseItemsQuery(httptest.NewRequest("GET", "/Items?EnableImages=false&EnableUserData=false", nil), NewResourceIDCodec())
	items := []baseItemDTO{{ImageTags: map[string]string{"Primary": "p"}, BackdropImageTags: []string{"b"}, ParentBackdropImageTags: []string{"b"}, SeriesPrimaryImageTag: "p", UserData: &itemUserDataDTO{}}}
	applyItemsResponseOptions(items, q)
	if len(items[0].ImageTags) != 0 || len(items[0].ParentBackdropImageTags) != 0 || items[0].SeriesPrimaryImageTag != "" || items[0].UserData != nil {
		t.Fatalf("presentation controls lost: %+v", items[0])
	}
}

func TestItemsQueryCombinedCatalogFilters(t *testing.T) {
	q := parseItemsQuery(httptest.NewRequest("GET", "/Items?Genres=Drama%7CComedy&Years=2020,2024&SearchTerm=Film&Filters=IsFavorite,IsPlayed&StartIndex=2&Limit=1", nil), NewResourceIDCodec())
	params := buildBrowseParams(q)
	for key, want := range map[string]string{"genres": "Drama|Comedy", "years": "2020,2024", "search_term": "Film", "is_favorite": "true", "is_played": "true", "offset": "2", "limit": "1"} {
		if params.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, params.Get(key), want)
		}
	}
	if !q.hasIntersectingFilters() {
		t.Fatal("combined request must use shared catalog predicates")
	}
}

type composedBrowseContent struct {
	countingContentService
	params url.Values
}

func (s *composedBrowseContent) BrowseItems(_ context.Context, _ *Session, params url.Values) (*upstreamBrowseResponse, error) {
	s.params = params
	return &upstreamBrowseResponse{Items: []upstreamListItem{}}, nil
}
func TestCombinedSearchPlayedFavoriteUsesBrowse(t *testing.T) {
	svc := &composedBrowseContent{}
	codec := NewResourceIDCodec()
	h := &ItemsHandler{content: svc, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}, images: NewImageCache(time.Hour, time.Now)}
	performItemsRequest(t, h, "/Items?SearchTerm=Drama&IsPlayed=true&IsFavorite=true&Limit=1&StartIndex=2")
	if svc.params.Get("search_term") != "Drama" || svc.params.Get("is_favorite") != "true" || svc.params.Get("is_played") != "true" {
		t.Fatalf("lost combined predicates: %v", svc.params)
	}
}

func TestEpisodePageFiltersNumericSeasonBeforeHydration(t *testing.T) {
	codec := NewResourceIDCodec()
	svc := &countingContentService{}
	h := &ItemsHandler{content: svc, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}, images: NewImageCache(time.Hour, time.Now)}
	q := parseItemsQuery(httptest.NewRequest("GET", "/Shows/s/Episodes?Season=2&StartIndex=1&Limit=1", nil), codec)
	episodes := []*models.Episode{{ContentID: "a", SeasonNumber: 1, EpisodeNumber: 1}, {ContentID: "b", SeasonNumber: 2, EpisodeNumber: 1}, {ContentID: "c", SeasonNumber: 2, EpisodeNumber: 2}}
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.writeEpisodeModelsPage(rec, req, collectionsTestSession(), q, "s", nil, episodes, true)
	if rec.Code != 200 {
		t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
	}
	var result queryResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.TotalRecordCount != 2 || result.StartIndex != 1 || result.Items[0].ID != codec.EncodeStringID(EncodedIDItem, "c") {
		t.Fatalf("wrong season page: %+v", result)
	}
	if svc.getItemDetailCalls > 1 {
		t.Fatalf("hydrated %d entries for one-item page", svc.getItemDetailCalls)
	}
}

type upcomingContractRepo struct {
	fakeSeasonEpisodeRepo
	since         time.Time
	filter        catalog.AccessFilter
	offset, limit int
}

func (r *upcomingContractRepo) ListUpcoming(_ context.Context, since time.Time, _, _ string, _ int, limit, offset int, filter catalog.AccessFilter) ([]*models.Episode, int, error) {
	r.since = since
	r.filter = filter
	r.offset = offset
	r.limit = limit
	return []*models.Episode{{ContentID: "future", SeriesID: "s", SeasonNumber: 3, EpisodeNumber: 1, Title: "Tomorrow"}}, 5, nil
}
func TestUpcomingUsesPremiereWindowAndProfileScope(t *testing.T) {
	codec := NewResourceIDCodec()
	repo := &upcomingContractRepo{}
	h := &ItemsHandler{episodeRepo: repo, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}, accessFilter: func(context.Context, int, string) catalog.AccessFilter {
		return catalog.AccessFilter{AllowedLibraryIDs: []int{3}, MaxContentRating: "PG"}
	}}
	req := httptest.NewRequest("GET", "/Shows/Upcoming?StartIndex=2&Limit=1&EnableUserData=false", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
	rec := httptest.NewRecorder()
	h.HandleUpcoming(rec, req)
	if rec.Code != 200 {
		t.Fatalf("response %d %s", rec.Code, rec.Body.String())
	}
	want := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	if !repo.since.Equal(want) || repo.limit != 1 || repo.offset != 2 || len(repo.filter.AllowedLibraryIDs) != 1 || repo.filter.MaxContentRating != "PG" {
		t.Fatalf("query scope lost: %+v", repo)
	}
	var result queryResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TotalRecordCount != 5 || len(result.Items) != 1 || result.Items[0].UserData != nil {
		t.Fatalf("upcoming page: %+v", result)
	}
}

type boundedEpisodeContractRepo struct {
	fakeSeasonEpisodeRepo
	filters            catalog.BrowseFilters
	access             catalog.AccessFilter
	seriesID, seasonID string
	seasonNumber       *int
}

func (r *boundedEpisodeContractRepo) BrowseEpisodes(_ context.Context, seriesID, seasonID string, seasonNumber *int, _ string, filters catalog.BrowseFilters, access catalog.AccessFilter) ([]*models.Episode, int, error) {
	r.filters = filters
	r.access = access
	r.seriesID = seriesID
	r.seasonID = seasonID
	r.seasonNumber = seasonNumber
	return []*models.Episode{{ContentID: "selected", SeriesID: seriesID, SeasonNumber: 2, EpisodeNumber: 4}}, 6, nil
}
func TestParentEpisodesComposeFiltersBeforePage(t *testing.T) {
	codec := NewResourceIDCodec()
	repo := &boundedEpisodeContractRepo{}
	svc := &countingContentService{seasons: []upstreamSeason{{ContentID: "season2", SeasonNumber: 2, EpisodeCount: 20}}}
	h := &ItemsHandler{content: svc, episodeRepo: repo, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}, images: NewImageCache(time.Hour, time.Now), accessFilter: func(context.Context, int, string) catalog.AccessFilter {
		return catalog.AccessFilter{AllowedLibraryIDs: []int{3}, MaxContentRating: "PG"}
	}}
	result := performItemsRequest(t, h, "/Items?ParentId="+codec.EncodeStringID(EncodedIDItem, "series")+"&IncludeItemTypes=Episode&Genres=Drama&Years=2024&IsFavorite=true&IsPlayed=false&StartIndex=3&Limit=1&Season=2")
	if repo.seriesID != "series" || repo.seasonNumber == nil || *repo.seasonNumber != 2 || repo.filters.IsPlayed == nil || *repo.filters.IsPlayed || !repo.filters.IsFavorite || repo.filters.ProfileID == "" || repo.filters.Offset != 3 || repo.filters.Limit != 1 || len(repo.filters.Genres) != 1 || len(repo.filters.Years) != 1 || repo.access.MaxContentRating != "PG" {
		t.Fatalf("lost parent predicate: %+v", repo)
	}
	if len(result.Items) != 1 || result.TotalRecordCount != 6 || result.StartIndex != 3 {
		t.Fatalf("incorrect bounded page: %+v", result)
	}
}
