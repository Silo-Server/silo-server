package jellycompat

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// mixedBrowseMovies is the media_items browse: it pages two movies, at most
// pageCap per call when set, as a played filter's scan budget can end a page
// short. It records the types and whether each call asked for a count.
type mixedBrowseMovies struct {
	countingContentService
	pageCap int
	types   []string
	counted []bool
}

func (s *mixedBrowseMovies) BrowseItems(_ context.Context, _ *Session, params url.Values) (*upstreamBrowseResponse, error) {
	s.types = append(s.types, params.Get("type"))
	s.counted = append(s.counted, params.Get("include_total") == "true")
	all := []upstreamListItem{{ContentID: "movie-1", Type: "movie", Title: "Movie 1"}, {ContentID: "movie-2", Type: "movie", Title: "Movie 2"}}
	offset, _ := strconv.Atoi(params.Get("offset"))
	limit, _ := strconv.Atoi(params.Get("limit"))
	if s.pageCap > 0 {
		limit = min(limit, s.pageCap)
	}
	page := slicePage(all, offset, limit)
	total := 0
	if params.Get("include_total") == "true" {
		total = len(all)
	}
	return &upstreamBrowseResponse{Items: page, Total: total, HasMore: offset+len(page) < len(all)}, nil
}

// mixedBrowseEpisodes is the episode browse over three episodes of one series.
type mixedBrowseEpisodes struct {
	fakeSeasonEpisodeRepo
	all   []*models.Episode
	pages []catalog.BrowseFilters
}

func (r *mixedBrowseEpisodes) BrowseEpisodes(_ context.Context, _, _ string, _ *int, _ string, f catalog.BrowseFilters, _ catalog.AccessFilter, includeTotal bool) ([]*models.Episode, int, error) {
	r.pages = append(r.pages, f)
	total := 0
	if includeTotal {
		total = len(r.all)
	}
	if f.Limit <= 0 {
		return []*models.Episode{}, total, nil
	}
	return slicePage(r.all, f.Offset, f.Limit), total, nil
}

func (r *mixedBrowseEpisodes) GetByIDs(_ context.Context, ids []string) ([]*models.Episode, error) {
	var out []*models.Episode
	for _, ep := range r.all {
		if slices.Contains(ids, ep.ContentID) {
			out = append(out, ep)
		}
	}
	return out, nil
}

// IncludeItemTypes=Episode,Movie is a union. The media_items browse holds no
// episodes, so the episodes are paged after the other types and counted in
// TotalRecordCount (#4257). The other types are counted only when the total or
// the episodes' offset needs it, and episodes join a page only once the other
// types have run out.
func TestBrowseItemsWithEpisodeAndOtherTypesIncludesEpisodes(t *testing.T) {
	type episodePage struct{ offset, limit int }
	for _, tc := range []struct {
		params       string
		pageCap      int
		wantIDs      []string
		wantTotal    int
		wantCounted  []bool
		wantEpisodes []episodePage
	}{
		{"", 0, []string{"movie-1", "movie-2", "ep-1", "ep-2", "ep-3"}, 5, []bool{true}, []episodePage{{0, 22}}},
		{"&StartIndex=1&Limit=2", 0, []string{"movie-2", "ep-1"}, 5, []bool{true}, []episodePage{{0, 1}}},
		{"&StartIndex=3&Limit=10", 0, []string{"ep-2", "ep-3"}, 5, []bool{true}, []episodePage{{1, 10}}},
		{"&Limit=2", 0, []string{"movie-1", "movie-2"}, 5, []bool{true}, []episodePage{{0, 0}}},
		{"&Limit=0", 0, nil, 5, []bool{true}, []episodePage{{0, 0}}},
		{"&Limit=2&EnableTotalRecordCount=false", 0, []string{"movie-1", "movie-2"}, 0, []bool{false}, nil},
		{"&Limit=10&EnableTotalRecordCount=false", 0, []string{"movie-1", "movie-2", "ep-1", "ep-2", "ep-3"}, 0, []bool{false}, []episodePage{{0, 8}}},
		{"&StartIndex=3&Limit=1&EnableTotalRecordCount=false", 0, []string{"ep-2"}, 0, []bool{false, true}, []episodePage{{1, 1}}},
		{"&Limit=10", 1, []string{"movie-1"}, 5, []bool{true}, []episodePage{{0, 0}}},
		{"&StartIndex=1&Limit=10", 1, []string{"movie-2", "ep-1", "ep-2", "ep-3"}, 5, []bool{true}, []episodePage{{0, 9}}},
		{"&Limit=2000", 0, []string{"movie-1", "movie-2", "ep-1", "ep-2", "ep-3"}, 5, []bool{true}, []episodePage{{0, 998}}},
	} {
		t.Run(fmt.Sprintf("%s/cap=%d", tc.params, tc.pageCap), func(t *testing.T) {
			movies := &mixedBrowseMovies{pageCap: tc.pageCap}
			h, episodes := newMixedBrowseHandler(movies)

			result := performItemsRequest(t, h, "/Items?IncludeItemTypes=Episode,Movie&Recursive=true"+tc.params)

			gotIDs := decodedItemIDs(t, h.codec, result)
			if !slices.Equal(gotIDs, tc.wantIDs) || result.TotalRecordCount != tc.wantTotal {
				t.Fatalf("items = %v total = %d, want %v total %d", gotIDs, result.TotalRecordCount, tc.wantIDs, tc.wantTotal)
			}
			if slices.ContainsFunc(movies.types, func(types string) bool { return types != "movie" }) {
				t.Fatalf("media_items browse types = %v, want only movie", movies.types)
			}
			if !slices.Equal(movies.counted, tc.wantCounted) {
				t.Fatalf("media_items calls counted = %v, want %v", movies.counted, tc.wantCounted)
			}
			var gotEpisodes []episodePage
			for _, page := range episodes.pages {
				gotEpisodes = append(gotEpisodes, episodePage{page.Offset, page.Limit})
			}
			if !slices.Equal(gotEpisodes, tc.wantEpisodes) {
				t.Fatalf("episode pages = %v, want %v", gotEpisodes, tc.wantEpisodes)
			}
		})
	}
}

// Without Recursive, Jellyfin lists a library ParentId's direct children,
// which are never episodes, when Episode comes only from an ExcludeItemTypes
// filter (here the movie, series and episode default). An IncludeItemTypes
// that names Episode there, and any type filter at the user root, searches
// recursively and pages the episodes too.
func TestNonRecursiveMixedBrowseFollowsJellyfinScope(t *testing.T) {
	for _, tc := range []struct {
		name         string
		params       string
		library      bool
		wantIDs      []string
		wantEpisodes bool
	}{
		{"library exclude only", "ExcludeItemTypes=Season", true, []string{"movie-1", "movie-2"}, false},
		{"library include", "IncludeItemTypes=Movie,Episode", true, []string{"movie-1", "movie-2", "ep-1", "ep-2", "ep-3"}, true},
		{"user root exclude only", "ExcludeItemTypes=Season", false, []string{"movie-1", "movie-2", "ep-1", "ep-2", "ep-3"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			movies := &mixedBrowseMovies{}
			h, episodes := newMixedBrowseHandler(movies)
			path := "/Items?" + tc.params
			if tc.library {
				path += "&ParentId=" + h.codec.EncodeIntID(EncodedIDLibrary, 1)
			}

			result := performItemsRequest(t, h, path)

			if gotIDs := decodedItemIDs(t, h.codec, result); !slices.Equal(gotIDs, tc.wantIDs) || result.TotalRecordCount != len(tc.wantIDs) {
				t.Fatalf("items = %v total = %d, want %v total %d", gotIDs, result.TotalRecordCount, tc.wantIDs, len(tc.wantIDs))
			}
			if got := len(episodes.pages) > 0; got != tc.wantEpisodes {
				t.Fatalf("episode pages = %v, want episodes browsed %v", episodes.pages, tc.wantEpisodes)
			}
		})
	}
}

// newMixedBrowseHandler serves movies' media_items browse and three episodes
// of one series.
func newMixedBrowseHandler(movies *mixedBrowseMovies) (*ItemsHandler, *mixedBrowseEpisodes) {
	codec := NewResourceIDCodec()
	episodes := &mixedBrowseEpisodes{all: []*models.Episode{
		{ContentID: "ep-1", SeriesID: "series", SeasonNumber: 1, EpisodeNumber: 1, Title: "One"},
		{ContentID: "ep-2", SeriesID: "series", SeasonNumber: 1, EpisodeNumber: 2, Title: "Two"},
		{ContentID: "ep-3", SeriesID: "series", SeasonNumber: 1, EpisodeNumber: 3, Title: "Three"},
	}}
	return &ItemsHandler{
		catalogUserState: true,
		content:          movies,
		episodeRepo:      episodes,
		itemRepo:         &fakeBatchItemRepo{items: map[string]*models.MediaItem{"series": {ContentID: "series", Type: "series", Title: "Series"}}},
		codec:            codec,
		mapper:           newMapper(codec, &config.Config{}),
		userData:         &mockUserDataService{},
		images:           NewImageCache(time.Hour, time.Now),
	}, episodes
}

func decodedItemIDs(t *testing.T, codec *ResourceIDCodec, result queryResultDTO) []string {
	t.Helper()
	var ids []string
	for _, item := range result.Items {
		id, err := codec.DecodeStringID(EncodedIDItem, item.ID)
		if err != nil {
			t.Fatalf("item id %q: %v", item.ID, err)
		}
		ids = append(ids, id)
	}
	return ids
}
