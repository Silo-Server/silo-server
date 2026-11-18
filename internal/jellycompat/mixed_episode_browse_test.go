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

// mixedBrowseMovies is the media_items browse: it pages two movies and records
// the types it was asked for.
type mixedBrowseMovies struct {
	countingContentService
	types []string
}

func (s *mixedBrowseMovies) BrowseItems(_ context.Context, _ *Session, params url.Values) (*upstreamBrowseResponse, error) {
	s.types = append(s.types, params.Get("type"))
	all := []upstreamListItem{{ContentID: "movie-1", Type: "movie", Title: "Movie 1"}, {ContentID: "movie-2", Type: "movie", Title: "Movie 2"}}
	offset, _ := strconv.Atoi(params.Get("offset"))
	limit, _ := strconv.Atoi(params.Get("limit"))
	total := 0
	if params.Get("include_total") == "true" {
		total = len(all)
	}
	return &upstreamBrowseResponse{Items: slicePage(all, offset, limit), Total: total}, nil
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
// TotalRecordCount (#4257).
func TestBrowseItemsWithEpisodeAndOtherTypesIncludesEpisodes(t *testing.T) {
	for _, tc := range []struct {
		params            string
		wantIDs           []string
		wantTotal         int
		wantEpisodeOffset int
		wantEpisodeLimit  int
	}{
		{"", []string{"movie-1", "movie-2", "ep-1", "ep-2", "ep-3"}, 5, 0, 22},
		{"&StartIndex=1&Limit=2", []string{"movie-2", "ep-1"}, 5, 0, 1},
		{"&StartIndex=3&Limit=10", []string{"ep-2", "ep-3"}, 5, 1, 10},
		{"&Limit=2", []string{"movie-1", "movie-2"}, 5, 0, 0},
		{"&Limit=0", nil, 5, 0, 0},
		{"&StartIndex=3&Limit=1&EnableTotalRecordCount=false", []string{"ep-2"}, 0, 1, 1},
	} {
		t.Run(tc.params, func(t *testing.T) {
			codec := NewResourceIDCodec()
			movies := &mixedBrowseMovies{}
			episodes := &mixedBrowseEpisodes{all: []*models.Episode{
				{ContentID: "ep-1", SeriesID: "series", SeasonNumber: 1, EpisodeNumber: 1, Title: "One"},
				{ContentID: "ep-2", SeriesID: "series", SeasonNumber: 1, EpisodeNumber: 2, Title: "Two"},
				{ContentID: "ep-3", SeriesID: "series", SeasonNumber: 1, EpisodeNumber: 3, Title: "Three"},
			}}
			h := &ItemsHandler{
				catalogUserState: true,
				content:          movies,
				episodeRepo:      episodes,
				itemRepo:         &fakeBatchItemRepo{items: map[string]*models.MediaItem{"series": {ContentID: "series", Type: "series", Title: "Series"}}},
				codec:            codec,
				mapper:           newMapper(codec, &config.Config{}),
				userData:         &mockUserDataService{},
				images:           NewImageCache(time.Hour, time.Now),
			}

			result := performItemsRequest(t, h, "/Items?IncludeItemTypes=Episode,Movie&Recursive=true"+tc.params)

			var gotIDs []string
			for _, item := range result.Items {
				id, err := codec.DecodeStringID(EncodedIDItem, item.ID)
				if err != nil {
					t.Fatalf("item id %q: %v", item.ID, err)
				}
				gotIDs = append(gotIDs, id)
			}
			if !slices.Equal(gotIDs, tc.wantIDs) || result.TotalRecordCount != tc.wantTotal {
				t.Fatalf("items = %v total = %d, want %v total %d", gotIDs, result.TotalRecordCount, tc.wantIDs, tc.wantTotal)
			}
			if !slices.Equal(movies.types, []string{"movie"}) {
				t.Fatalf("media_items browse types = %v, want [movie]", movies.types)
			}
			if len(episodes.pages) != 1 || episodes.pages[0].Offset != tc.wantEpisodeOffset || episodes.pages[0].Limit != tc.wantEpisodeLimit {
				t.Fatalf("episode pages = %s, want one at offset %d limit %d", fmt.Sprint(episodes.pages), tc.wantEpisodeOffset, tc.wantEpisodeLimit)
			}
		})
	}
}
