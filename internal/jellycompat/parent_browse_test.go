package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeSeasonByIDRepo implements imageSeasonRepository for the season-parent path.
type fakeSeasonByIDRepo struct{ seasons map[string]*models.Season }

func (f *fakeSeasonByIDRepo) GetByID(_ context.Context, id string) (*models.Season, error) {
	if s, ok := f.seasons[id]; ok {
		return s, nil
	}
	return nil, errors.New("season not found")
}

// fakeSeasonEpisodeRepo implements episodeRepoForBatchLoader, serving episodes by
// (seriesID, seasonNumber) for the season-parent episode listing.
type fakeSeasonEpisodeRepo struct{ bySeason map[string][]*models.Episode }

func episodeBySeasonKey(seriesID string, seasonNum int) string {
	return fmt.Sprintf("%s|%d", seriesID, seasonNum)
}

func (f *fakeSeasonEpisodeRepo) GetByIDs(_ context.Context, contentIDs []string) ([]*models.Episode, error) {
	want := make(map[string]bool, len(contentIDs))
	for _, id := range contentIDs {
		want[id] = true
	}
	var out []*models.Episode
	for _, eps := range f.bySeason {
		for _, ep := range eps {
			if ep != nil && want[ep.ContentID] {
				out = append(out, ep)
			}
		}
	}
	return out, nil
}

func (f *fakeSeasonEpisodeRepo) HasFilesByIDs(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (f *fakeSeasonEpisodeRepo) ListBySeason(_ context.Context, seriesID string, seasonNum int) ([]*models.Episode, error) {
	return f.bySeason[episodeBySeasonKey(seriesID, seasonNum)], nil
}

func (f *fakeSeasonEpisodeRepo) ListBySeries(_ context.Context, seriesID string) ([]*models.Episode, error) {
	var out []*models.Episode
	for key, eps := range f.bySeason {
		if strings.HasPrefix(key, seriesID+"|") {
			out = append(out, eps...)
		}
	}
	return out, nil
}

// ListAdjacentInSeries returns the target episode plus its immediate
// previous/next neighbors across the whole series, mirroring the repo's
// natural (season, episode) ordering.
func (f *fakeSeasonEpisodeRepo) ListAdjacentInSeries(ctx context.Context, seriesID string, seasonNumber, episodeNumber int) ([]*models.Episode, error) {
	all, _ := f.ListBySeries(ctx, seriesID)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].SeasonNumber == all[j].SeasonNumber {
			return all[i].EpisodeNumber < all[j].EpisodeNumber
		}
		return all[i].SeasonNumber < all[j].SeasonNumber
	})
	targetIdx := -1
	for i, ep := range all {
		if ep.SeasonNumber == seasonNumber && ep.EpisodeNumber == episodeNumber {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return nil, nil
	}
	start := targetIdx - 1
	if start < 0 {
		start = 0
	}
	end := targetIdx + 2
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], nil
}

// TestHandleItems_SeriesParentWithoutTypeReturnsSeasons pins the Void fix: a
// series ParentId with no IncludeItemTypes must list the series' seasons, not
// fall through to the top-level library views. countingContentService panics in
// ListUserLibraries, so a regression that routes to handleViewsResponse would
// panic this test rather than silently pass.
func TestHandleItems_SeriesParentWithoutTypeReturnsSeasons(t *testing.T) {
	codec := NewResourceIDCodec()
	seriesContentID := "series-1"
	encodedSeriesID := codec.EncodeStringID(EncodedIDItem, seriesContentID)
	contentSvc := &countingContentService{
		seasons: []upstreamSeason{
			{ContentID: "season-1", SeasonNumber: 1, Title: "Season 1", EpisodeCount: 10},
			{ContentID: "season-2", SeasonNumber: 2, Title: "Season 2", EpisodeCount: 8},
		},
	}
	h := &ItemsHandler{
		content:  contentSvc,
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		images:   NewImageCache(time.Hour, time.Now),
	}

	result := performItemsRequest(t, h, "/Users/test/Items?ParentId="+encodedSeriesID)
	if result.TotalRecordCount != 2 || len(result.Items) != 2 {
		t.Fatalf("expected 2 seasons, got %+v", result.Items)
	}
	for _, item := range result.Items {
		if item.Type != "Season" {
			t.Fatalf("expected Season items (not library views), got %q", item.Type)
		}
	}
	if contentSvc.listSeasonsSeries != seriesContentID {
		t.Fatalf("ListSeasons series = %q, want %q", contentSvc.listSeasonsSeries, seriesContentID)
	}
}

// TestHandleItems_SeriesParentNonEpisodeTypeIsEmpty pins that a type filter a
// series parent cannot satisfy (e.g. Movie) returns an empty page rather than a
// wrong-typed seasons listing, and does not even hit ListSeasons.
func TestHandleItems_SeriesParentNonEpisodeTypeIsEmpty(t *testing.T) {
	codec := NewResourceIDCodec()
	encodedSeriesID := codec.EncodeStringID(EncodedIDItem, "series-1")
	contentSvc := &countingContentService{
		seasons: []upstreamSeason{{ContentID: "season-1", SeasonNumber: 1, Title: "Season 1", EpisodeCount: 10}},
	}
	h := &ItemsHandler{
		content:  contentSvc,
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		images:   NewImageCache(time.Hour, time.Now),
	}

	result := performItemsRequest(t, h, "/Users/test/Items?ParentId="+encodedSeriesID+"&IncludeItemTypes=Movie")
	if result.TotalRecordCount != 0 || len(result.Items) != 0 {
		t.Fatalf("expected empty result for Movie type under a series parent, got %+v", result.Items)
	}
	if contentSvc.listSeasonsCalls != 0 {
		t.Fatalf("ListSeasons must not be called for an unsatisfiable type filter; got %d", contentSvc.listSeasonsCalls)
	}
}

// TestHandleItems_SeasonParentReturnsEpisodes pins the second half of the Void
// fix: a season ParentId (no IncludeItemTypes) must list that season's episodes.
func TestHandleItems_SeasonParentReturnsEpisodes(t *testing.T) {
	codec := NewResourceIDCodec()
	seriesContentID := "series-1"
	seasonContentID := "season-1"
	encodedSeasonID := codec.EncodeStringID(EncodedIDSeason, seasonContentID)

	contentSvc := &countingContentService{
		seasons: []upstreamSeason{
			{ContentID: seasonContentID, SeasonNumber: 1, Title: "Season 1", EpisodeCount: 2},
		},
	}
	episodeRepo := &fakeSeasonEpisodeRepo{bySeason: map[string][]*models.Episode{
		episodeBySeasonKey(seriesContentID, 1): {
			{ContentID: "ep-1", SeriesID: seriesContentID, SeasonID: seasonContentID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"},
			{ContentID: "ep-2", SeriesID: seriesContentID, SeasonID: seasonContentID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Second"},
		},
	}}
	h := &ItemsHandler{
		content:     contentSvc,
		userData:    &mockUserDataService{},
		codec:       codec,
		mapper:      newMapper(codec, &config.Config{}),
		images:      NewImageCache(time.Hour, time.Now),
		episodeRepo: episodeRepo,
		seasonRepo: &fakeSeasonByIDRepo{seasons: map[string]*models.Season{
			seasonContentID: {ContentID: seasonContentID, SeriesID: seriesContentID, SeasonNumber: 1},
		}},
	}

	result := performItemsRequest(t, h, "/Users/test/Items?ParentId="+encodedSeasonID)
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 episodes, got %+v", result.Items)
	}
	for _, item := range result.Items {
		if item.Type != "Episode" {
			t.Fatalf("expected Episode items (not library views), got %q", item.Type)
		}
	}
	if result.Items[0].Name != "Pilot" || result.Items[1].Name != "Second" {
		t.Fatalf("unexpected episode order/names: %+v", result.Items)
	}
}

// TestHandleItems_SeasonParentNonEpisodeTypeIsEmpty pins that a season ParentId
// with a type filter that excludes Episode (e.g. Movie) returns an empty page and
// short-circuits before listing episodes, even though episodes exist.
func TestHandleItems_SeasonParentNonEpisodeTypeIsEmpty(t *testing.T) {
	codec := NewResourceIDCodec()
	seriesContentID := "series-1"
	seasonContentID := "season-1"
	encodedSeasonID := codec.EncodeStringID(EncodedIDSeason, seasonContentID)
	contentSvc := &countingContentService{
		seasons: []upstreamSeason{{ContentID: seasonContentID, SeasonNumber: 1, Title: "Season 1", EpisodeCount: 1}},
	}
	episodeRepo := &fakeSeasonEpisodeRepo{bySeason: map[string][]*models.Episode{
		episodeBySeasonKey(seriesContentID, 1): {
			{ContentID: "ep-1", SeriesID: seriesContentID, SeasonID: seasonContentID, SeasonNumber: 1, EpisodeNumber: 1, Title: "E1"},
		},
	}}
	h := &ItemsHandler{
		content:     contentSvc,
		userData:    &mockUserDataService{},
		codec:       codec,
		mapper:      newMapper(codec, &config.Config{}),
		images:      NewImageCache(time.Hour, time.Now),
		episodeRepo: episodeRepo,
		seasonRepo: &fakeSeasonByIDRepo{seasons: map[string]*models.Season{
			seasonContentID: {ContentID: seasonContentID, SeriesID: seriesContentID, SeasonNumber: 1},
		}},
	}

	result := performItemsRequest(t, h, "/Users/test/Items?ParentId="+encodedSeasonID+"&IncludeItemTypes=Movie")
	if result.TotalRecordCount != 0 || len(result.Items) != 0 {
		t.Fatalf("expected empty for Movie type under a season parent, got %+v", result.Items)
	}
	if contentSvc.listSeasonsCalls != 0 {
		t.Fatalf("type guard should short-circuit before episode listing; ListSeasons called %d times", contentSvc.listSeasonsCalls)
	}
}

// TestHandleItems_SeasonParentWithoutRepoIsEmptyNotViews ensures the season-parent
// path degrades to an empty page (never the library views) when no season repo is
// wired. countingContentService panics in ListUserLibraries to catch a views
// fall-through regression.
func TestHandleItems_SeasonParentWithoutRepoIsEmptyNotViews(t *testing.T) {
	codec := NewResourceIDCodec()
	encodedSeasonID := codec.EncodeStringID(EncodedIDSeason, "season-1")
	h := &ItemsHandler{
		content:  &countingContentService{},
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		images:   NewImageCache(time.Hour, time.Now),
	}

	result := performItemsRequest(t, h, "/Users/test/Items?ParentId="+encodedSeasonID)
	if result.TotalRecordCount != 0 || len(result.Items) != 0 {
		t.Fatalf("expected empty result without season repo, got %+v", result.Items)
	}
}

// TestHandleItems_SeasonParentEpisodesPaged pins that the generic season-parent
// browse path honors StartIndex/Limit (unlike /Shows/{id}/Episodes, which stays
// whole-season) and reports the full count.
func TestHandleItems_SeasonParentEpisodesPaged(t *testing.T) {
	codec := NewResourceIDCodec()
	seriesContentID := "series-1"
	seasonContentID := "season-1"
	encodedSeasonID := codec.EncodeStringID(EncodedIDSeason, seasonContentID)
	contentSvc := &countingContentService{
		seasons: []upstreamSeason{{ContentID: seasonContentID, SeasonNumber: 1, Title: "Season 1", EpisodeCount: 3}},
	}
	episodeRepo := &fakeSeasonEpisodeRepo{bySeason: map[string][]*models.Episode{
		episodeBySeasonKey(seriesContentID, 1): {
			{ContentID: "ep-1", SeriesID: seriesContentID, SeasonID: seasonContentID, SeasonNumber: 1, EpisodeNumber: 1, Title: "E1"},
			{ContentID: "ep-2", SeriesID: seriesContentID, SeasonID: seasonContentID, SeasonNumber: 1, EpisodeNumber: 2, Title: "E2"},
			{ContentID: "ep-3", SeriesID: seriesContentID, SeasonID: seasonContentID, SeasonNumber: 1, EpisodeNumber: 3, Title: "E3"},
		},
	}}
	h := &ItemsHandler{
		content:     contentSvc,
		userData:    &mockUserDataService{},
		codec:       codec,
		mapper:      newMapper(codec, &config.Config{}),
		images:      NewImageCache(time.Hour, time.Now),
		episodeRepo: episodeRepo,
		seasonRepo: &fakeSeasonByIDRepo{seasons: map[string]*models.Season{
			seasonContentID: {ContentID: seasonContentID, SeriesID: seriesContentID, SeasonNumber: 1},
		}},
	}

	result := performItemsRequest(t, h, "/Users/test/Items?ParentId="+encodedSeasonID+"&StartIndex=1&Limit=1")
	if result.TotalRecordCount != 3 || result.StartIndex != 1 {
		t.Fatalf("expected total 3 / startIndex 1, got %d / %d", result.TotalRecordCount, result.StartIndex)
	}
	if len(result.Items) != 1 || result.Items[0].Name != "E2" {
		t.Fatalf("expected page [E2], got %+v", result.Items)
	}
}

// TestParseItemsQuery_SeasonParentSetsParentSeasonID asserts the parser routes a
// season ParentId to parentSeasonID while a series (item) ParentId stays in
// parentItemID — the distinction the new routing relies on.
func TestParseItemsQuery_SeasonParentSetsParentSeasonID(t *testing.T) {
	codec := NewResourceIDCodec()

	seasonID := codec.EncodeStringID(EncodedIDSeason, "season-9")
	q := parseItemsQuery(httptest.NewRequest("GET", "/Items?ParentId="+url.QueryEscape(seasonID), nil), codec)
	if q.parentSeasonID != "season-9" {
		t.Fatalf("expected parentSeasonID season-9, got %q", q.parentSeasonID)
	}
	if q.parentItemID != "" {
		t.Fatalf("expected parentItemID empty for a season parent, got %q", q.parentItemID)
	}

	seriesID := codec.EncodeStringID(EncodedIDItem, "series-9")
	q = parseItemsQuery(httptest.NewRequest("GET", "/Items?ParentId="+url.QueryEscape(seriesID), nil), codec)
	if q.parentItemID != "series-9" || q.parentSeasonID != "" {
		t.Fatalf("expected parentItemID series-9 only, got item=%q season=%q", q.parentItemID, q.parentSeasonID)
	}
}
