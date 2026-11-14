package jellycompat

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// mixedBrowseContent answers the media_items browse with one movie and records
// that it was asked.
type mixedBrowseContent struct {
	countingContentService
	browsed bool
}

func (s *mixedBrowseContent) BrowseItems(_ context.Context, _ *Session, _ url.Values) (*upstreamBrowseResponse, error) {
	s.browsed = true
	return &upstreamBrowseResponse{
		Items: []upstreamListItem{{ContentID: "movie-a", Type: "movie", Title: "A Movie"}},
		Total: 1,
	}, nil
}

// mixedBrowseEpisodes answers the episode browse and counts the calls.
type mixedBrowseEpisodes struct {
	calls int
	total int
}

func (r *mixedBrowseEpisodes) ListBySeason(context.Context, string, int) ([]*models.Episode, error) {
	return nil, nil
}

func (r *mixedBrowseEpisodes) ListBySeries(context.Context, string) ([]*models.Episode, error) {
	return nil, nil
}

func (r *mixedBrowseEpisodes) ListBySeriesIDs(context.Context, []string) (map[string][]*models.Episode, error) {
	return map[string][]*models.Episode{}, nil
}

func (r *mixedBrowseEpisodes) ListIDsBySeriesIDs(context.Context, []string) (map[string][]string, error) {
	return map[string][]string{}, nil
}

func (r *mixedBrowseEpisodes) ListIDsBySeasonIDs(context.Context, []string) (map[string][]string, error) {
	return map[string][]string{}, nil
}

func (r *mixedBrowseEpisodes) ListAdjacentInSeriesFor(context.Context, string, int, int, catalog.AccessFilter) ([]*models.Episode, error) {
	return nil, nil
}

func (r *mixedBrowseEpisodes) HasFilesByIDs(_ context.Context, _ []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (r *mixedBrowseEpisodes) GetByIDs(_ context.Context, _ []string) ([]*models.Episode, error) {
	return nil, nil
}

func (r *mixedBrowseEpisodes) BrowseEpisodes(_ context.Context, _, _ string, _ *int, _ string, _ catalog.BrowseFilters, _ catalog.AccessFilter, _ bool) ([]*models.Episode, int, error) {
	r.calls++
	return []*models.Episode{
		{ContentID: "episode-a", SeriesID: "series-a", SeasonNumber: 1, EpisodeNumber: 1},
		{ContentID: "episode-b", SeriesID: "series-a", SeasonNumber: 1, EpisodeNumber: 2},
	}, r.total, nil
}

// A query naming Episode beside another type must page both sources: the
// media_items browse holds no episodes, so serving only it silently dropped
// them.
func TestMixedItemTypesBrowseCountsEpisodes(t *testing.T) {
	codec := NewResourceIDCodec()
	content := &mixedBrowseContent{}
	episodes := &mixedBrowseEpisodes{total: 2}
	h := &ItemsHandler{
		content:     content,
		episodeRepo: episodes,
		codec:       codec,
		mapper:      newMapper(codec, &config.Config{}),
		userData:    &mockUserDataService{},
		images:      NewImageCache(time.Hour, time.Now),
	}

	result := performItemsRequest(t, h, "/Items?IncludeItemTypes=Episode,Movie&Recursive=true&Limit=10")

	if !content.browsed {
		t.Error("mixed-type query did not browse the non-episode types")
	}
	if episodes.calls == 0 {
		t.Fatal("mixed-type query did not browse episodes")
	}
	if result.TotalRecordCount != 3 {
		t.Fatalf("TotalRecordCount = %d, want 3 (1 movie + 2 episodes)", result.TotalRecordCount)
	}
}

// The episode browse must not run for a query that names only other types, and
// an episode-only query keeps its existing route.
func TestMixedBrowsePredicate(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{episodeRepo: &mixedBrowseEpisodes{}, codec: codec}

	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"/Items?IncludeItemTypes=Episode,Movie", true},
		{"/Items?IncludeItemTypes=Episode", false}, // episodeOnlyBrowser handles it
		{"/Items?IncludeItemTypes=Movie", false},
		{"/Items", false},
		{"/Items?IncludeItemTypes=Episode,Movie&Ids=" + codec.EncodeStringID(EncodedIDItem, "item-a"), false},
	} {
		req := httptest.NewRequest("GET", tc.target, nil)
		query := parseItemsQuery(req, codec)
		if _, got := h.mixedEpisodeBrowser(query); got != tc.want {
			t.Errorf("%s: mixedEpisodeBrowser = %v, want %v", tc.target, got, tc.want)
		}
	}
}
