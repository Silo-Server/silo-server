package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

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

type upcomingContractRepo struct {
	fakeSeasonEpisodeRepo
	since              time.Time
	filter             catalog.AccessFilter
	offset, limit      int
	seriesID, seasonID string
	libraryID, calls   int
	hasFiles           map[string]bool
	availabilityIDs    []string
	availabilityErr    error
	includeTotal       bool
}

func (r *upcomingContractRepo) HasFilesByIDs(_ context.Context, ids []string) (map[string]bool, error) {
	r.availabilityIDs = ids
	return r.hasFiles, r.availabilityErr
}

func (r *upcomingContractRepo) ListUpcoming(_ context.Context, since time.Time, seriesID, seasonID string, libraryID int, limit, offset int, filter catalog.AccessFilter, includeTotal bool) ([]*models.Episode, int, error) {
	r.calls++
	r.seriesID, r.seasonID, r.libraryID = seriesID, seasonID, libraryID
	r.since = since
	r.filter = filter
	r.offset = offset
	r.limit = limit
	r.includeTotal = includeTotal
	total := 0
	if includeTotal {
		total = 5
	}
	return []*models.Episode{{ContentID: "future", SeriesID: "s", SeasonNumber: 3, EpisodeNumber: 1, Title: "Tomorrow"}}, total, nil
}
func TestUpcomingUsesPremiereWindowAndProfileScope(t *testing.T) {
	codec := NewResourceIDCodec()
	repo := &upcomingContractRepo{}
	h := &ItemsHandler{episodeRepo: repo, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}, accessFilter: func(context.Context, int, string) catalog.AccessFilter {
		return catalog.AccessFilter{AllowedLibraryIDs: []int{3}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
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

func TestUpcomingSeriesScope(t *testing.T) {
	codec := NewResourceIDCodec()
	series := codec.EncodeStringID(EncodedIDItem, "series")
	season := codec.EncodeStringID(EncodedIDSeason, "season")
	library := codec.EncodeIntID(EncodedIDLibrary, 3)
	for _, tt := range []struct {
		name, query, wantSeries, wantSeason string
		wantLibrary, wantStatus             int
	}{
		{"series", "SeriesId=" + series, "series", "", 0, 200},
		{"parent series", "ParentId=" + series, "series", "", 0, 200},
		{"matching parent", "SeriesId=" + series + "&ParentId=" + series, "series", "", 0, 200},
		{"library intersection", "SeriesId=" + series + "&ParentId=" + library, "series", "", 3, 200},
		{"season intersection", "SeriesId=" + series + "&ParentId=" + season, "series", "season", 0, 200},
		{"malformed series", "SeriesId=invalid", "", "", 0, 404},
		{"wrong kind", "SeriesId=" + season, "", "", 0, 404},
		{"conflicting parent", "SeriesId=" + series + "&ParentId=" + codec.EncodeStringID(EncodedIDItem, "other"), "", "", 0, 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &upcomingContractRepo{}
			h := &ItemsHandler{episodeRepo: repo, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}}
			req := httptest.NewRequest("GET", "/Shows/Upcoming?"+tt.query+"&StartIndex=2&Limit=1", nil)
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
			rec := httptest.NewRecorder()
			h.HandleUpcoming(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != 200 {
				if repo.calls != 0 {
					t.Fatal("invalid scope reached catalog")
				}
				return
			}
			if repo.calls != 1 || repo.seriesID != tt.wantSeries || repo.seasonID != tt.wantSeason || repo.libraryID != tt.wantLibrary || repo.offset != 2 || repo.limit != 1 {
				t.Fatalf("upcoming query scope lost: %+v", repo)
			}
		})
	}
}

type boundedEpisodeContractRepo struct {
	fakeSeasonEpisodeRepo
	filters            catalog.BrowseFilters
	access             catalog.AccessFilter
	seriesID, seasonID string
	seasonNumber       *int
	includeTotal       bool
}

func (r *boundedEpisodeContractRepo) BrowseEpisodes(_ context.Context, seriesID, seasonID string, seasonNumber *int, _ string, filters catalog.BrowseFilters, access catalog.AccessFilter, includeTotal bool) ([]*models.Episode, int, error) {
	r.filters = filters
	r.access = access
	r.seriesID = seriesID
	r.seasonID = seasonID
	r.seasonNumber = seasonNumber
	r.includeTotal = includeTotal
	total := 0
	if includeTotal {
		total = 6
	}
	return []*models.Episode{{ContentID: "selected", SeriesID: seriesID, SeasonNumber: 2, EpisodeNumber: 4}}, total, nil
}
func TestParentEpisodesComposeFiltersBeforePage(t *testing.T) {
	for _, tc := range []struct {
		name, totalParam string
		includeTotal     bool
	}{
		{name: "default", includeTotal: true},
		{name: "disabled", totalParam: "&EnableTotalRecordCount=false"},
	} {
		for _, route := range []string{"items", "shows"} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				codec := NewResourceIDCodec()
				repo := &boundedEpisodeContractRepo{}
				svc := &countingContentService{seasons: []upstreamSeason{{ContentID: "season2", SeasonNumber: 2, EpisodeCount: 20}}}
				h := &ItemsHandler{catalogUserState: true, content: svc, episodeRepo: repo, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}, images: NewImageCache(time.Hour, time.Now), accessFilter: func(context.Context, int, string) catalog.AccessFilter {
					return catalog.AccessFilter{AllowedLibraryIDs: []int{3}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
				}}
				seriesID := codec.EncodeStringID(EncodedIDItem, "series")
				params := "IncludeItemTypes=Episode&Genres=Drama&Years=2024&IsFavorite=true&IsPlayed=false&StartIndex=3&Limit=1&Season=2" + tc.totalParam
				var result queryResultDTO
				if route == "shows" {
					result = performEpisodesRequest(t, h, "/Shows/"+seriesID+"/Episodes?"+params, seriesID)
				} else {
					result = performItemsRequest(t, h, "/Items?ParentId="+seriesID+"&"+params)
				}
				if repo.seriesID != "series" || repo.seasonNumber == nil || *repo.seasonNumber != 2 || repo.filters.IsPlayed == nil || *repo.filters.IsPlayed || !repo.filters.IsFavorite || repo.filters.ProfileID == "" || repo.filters.Offset != 3 || repo.filters.Limit != 1 || len(repo.filters.Genres) != 1 || len(repo.filters.Years) != 1 || repo.access.MaxContentRating != "PG" || repo.includeTotal != tc.includeTotal {
					t.Fatalf("lost parent predicate or count option: %+v", repo)
				}
				wantTotal := 0
				if tc.includeTotal {
					wantTotal = 6
				}
				if len(result.Items) != 1 || result.TotalRecordCount != wantTotal || result.StartIndex != 3 {
					t.Fatalf("incorrect bounded page: %+v", result)
				}
			})
		}
	}
}

// Jellyfin returns a whole series when Limit is absent. Infuse lists every
// episode of a show in one such request and builds its seasons from the
// result, so the 24-item browse default hid later seasons (#1628).
func TestParentEpisodesWithoutLimitReturnWholeSeries(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		wantLimit    int
	}{
		{name: "absent", params: "", wantLimit: catalog.MaxEpisodePageSize},
		{name: "explicit", params: "&Limit=24", wantLimit: 24},
	} {
		for _, route := range []string{"items", "shows"} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				codec := NewResourceIDCodec()
				repo := &boundedEpisodeContractRepo{}
				svc := &countingContentService{seasons: []upstreamSeason{{ContentID: "season2", SeasonNumber: 2, EpisodeCount: 20}}}
				h := &ItemsHandler{catalogUserState: true, content: svc, episodeRepo: repo, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}, images: NewImageCache(time.Hour, time.Now)}
				seriesID := codec.EncodeStringID(EncodedIDItem, "series")
				if route == "shows" {
					performEpisodesRequest(t, h, "/Shows/"+seriesID+"/Episodes?excludeLocationTypes=Virtual"+tc.params, seriesID)
				} else {
					performItemsRequest(t, h, "/Items?ParentId="+seriesID+"&IncludeItemTypes=Episode"+tc.params)
				}
				if repo.filters.Limit != tc.wantLimit {
					t.Fatalf("episode page limit = %d, want %d", repo.filters.Limit, tc.wantLimit)
				}
			})
		}
	}
}

type presentationViewsContent struct{ countingContentService }

func (*presentationViewsContent) ListUserLibraries(context.Context, *Session) ([]upstreamUserLibrary, error) {
	return []upstreamUserLibrary{{ID: 3, Name: "Movies", Type: "movies", PosterPath: "poster.jpg", PosterURL: "https://example.com/poster.jpg"}}, nil
}
