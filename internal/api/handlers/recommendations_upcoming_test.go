package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
)

func TestMergePremieresIntoDiscoverRow_BlendsIntoForYouRow(t *testing.T) {
	t.Parallel()

	baseRows := []recommendations.ForYouRow{{
		Type:  "cluster",
		Label: "For You",
		Items: []recommendations.ScoredItem{
			{MediaItemID: "series-premiere"},
			{MediaItemID: "series-airing"},
			{MediaItemID: "movie-premiere"},
		},
	}}

	events := []catalog.CalendarEvent{
		movieCalendarEvent("movie-premiere", "2026-04-08", nil),
		episodeCalendarEvent("episode-premiere", "series-premiere", 1, 1, "2026-04-10", nil),
		episodeCalendarEvent("episode-followup", "series-premiere", 1, 2, "2026-04-11", nil),
		episodeCalendarEvent("episode-earliest", "series-airing", 2, 2, "2026-04-09", nil),
	}

	discoverRows := discoverRowModelsFromRecommendations(baseRows)
	rankMap, firstSeenItems := buildDiscoverRankingData(baseRows)
	merged := mergePremieresIntoDiscoverRow(
		discoverRows[0],
		rankMap,
		firstSeenItems,
		categorizeUpcomingCandidates(events),
		map[string]struct{}{},
		map[string]int{},
		map[string]bool{},
		map[string]bool{},
		time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC),
	)

	if merged.Label != discoverForYouLabel {
		t.Fatalf("merged.Label = %q, want %q", merged.Label, discoverForYouLabel)
	}
	if len(merged.Items) != 3 {
		t.Fatalf("merged items = %d, want 3", len(merged.Items))
	}

	if _, ok := merged.UpcomingEvents["series-premiere"]; !ok {
		t.Fatalf("series premiere should be enriched with upcoming metadata")
	}
	if _, ok := merged.UpcomingEvents["movie-premiere"]; !ok {
		t.Fatalf("movie premiere should be enriched with upcoming metadata")
	}
	if _, ok := merged.UpcomingEvents["series-airing"]; ok {
		t.Fatalf("regular episode airings should not be blended into the For You row")
	}
}

func TestMergePremieresIntoDiscoverRow_PromotesAndRanksCandidates(t *testing.T) {
	t.Parallel()

	baseRows := []recommendations.ForYouRow{
		{
			Type:  "cluster",
			Label: "For You",
			Items: []recommendations.ScoredItem{
				{MediaItemID: "movie-a"},
				{MediaItemID: "movie-b"},
			},
		},
		{
			Type:  "genre_sampler",
			Label: "Top Drama",
			Items: []recommendations.ScoredItem{
				{MediaItemID: "movie-c"},
			},
		},
	}

	discoverRows := discoverRowModelsFromRecommendations(baseRows)
	rankMap, firstSeenItems := buildDiscoverRankingData(baseRows)
	merged := mergePremieresIntoDiscoverRow(
		discoverRows[0],
		rankMap,
		firstSeenItems,
		categorizeUpcomingCandidates([]catalog.CalendarEvent{
			movieCalendarEvent("movie-a", "2026-04-12", nil),
			movieCalendarEvent("movie-b", "2026-04-07", testStringPtr("10:00")),
			movieCalendarEvent("movie-c", "2026-04-13", nil),
			movieCalendarEvent("movie-d", "2026-04-08", nil),
			movieCalendarEvent("movie-e", "2026-04-09", nil),
		}),
		map[string]struct{}{"movie-f": {}},
		map[string]int{"movie-e": 2},
		map[string]bool{"movie-b": true, "movie-d": true},
		map[string]bool{"movie-c": true},
		time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC),
	)

	if len(merged.Items) != 4 {
		t.Fatalf("merged items = %d, want 4", len(merged.Items))
	}

	gotIDs := []string{
		merged.Items[0].MediaItemID,
		merged.Items[1].MediaItemID,
		merged.Items[2].MediaItemID,
		merged.Items[3].MediaItemID,
	}
	wantIDs := []string{"movie-b", "movie-d", "movie-c", "movie-a"}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("row item %d = %q, want %q", i, gotIDs[i], wantIDs[i])
		}
	}

	if _, ok := merged.UpcomingEvents["movie-c"]; !ok {
		t.Fatalf("promoted discover item should carry upcoming metadata")
	}
	if _, ok := merged.UpcomingEvents["movie-d"]; !ok {
		t.Fatalf("saved-only premiere should be blended into the main For You row")
	}
	for _, item := range merged.Items {
		if item.MediaItemID == "movie-e" {
			t.Fatalf("low-rated premiere %q should not be present", item.MediaItemID)
		}
	}
}

func TestBlendUpcomingIntoDiscoverRows_LeavesRowsUnchangedWithoutForYou(t *testing.T) {
	t.Parallel()

	handler := NewRecommendationsHandler(nil, nil, nil, nil, nil, false)
	handler.CalendarRepo = stubCalendarRepository{}
	handler.nowFn = func() time.Time {
		return time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC)
	}

	rows := []discoverRowModel{{
		Type:  "genre_sampler",
		Label: "Top Drama",
		Items: []recommendations.ScoredItem{{MediaItemID: "movie-1"}},
	}}

	blended, err := handler.blendUpcomingIntoDiscoverRows(
		context.Background(),
		7,
		"profile-1",
		catalog.AccessFilter{},
		rows,
		nil,
	)
	if err != nil {
		t.Fatalf("blendUpcomingIntoDiscoverRows error = %v", err)
	}
	if len(blended) != 1 || blended[0].Label != "Top Drama" {
		t.Fatalf("blended rows = %+v, want unchanged rows", blended)
	}
}

func TestRecommendationsHandleDiscover_FallsBackToLegacyRowsWhenUpcomingLoadFails(t *testing.T) {
	t.Parallel()

	handler := NewRecommendationsHandler(nil, stubRecommendationsReader{
		discoverRows: []recommendations.ForYouRow{{
			Type:  "cluster",
			Label: "For You",
			Items: []recommendations.ScoredItem{{MediaItemID: "movie-1"}},
		}},
	}, nil, nil, nil, false)
	handler.Fetcher = stubDiscoverFetcher{
		items: []*models.MediaItem{{
			ContentID: "movie-1",
			Type:      "movie",
			Title:     "Movie One",
			Genres:    []string{"Drama"},
			Status:    "matched",
		}},
	}
	handler.CalendarRepo = stubCalendarRepository{err: errors.New("calendar unavailable")}

	req := httptest.NewRequest(http.MethodGet, "/recommendations/discover", nil)
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.HandleDiscover(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp discoverResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(resp.Rows))
	}
	if resp.Rows[0].Label != "For You" {
		t.Fatalf("row label = %q, want For You", resp.Rows[0].Label)
	}
	if len(resp.Rows[0].Items) != 1 || resp.Rows[0].Items[0].ContentID != "movie-1" {
		t.Fatalf("legacy row items = %+v, want movie-1", resp.Rows[0].Items)
	}
	if resp.Rows[0].Items[0].UpcomingEvent != nil {
		t.Fatalf("legacy row should not include upcoming event metadata")
	}
}

type stubRecommendationsReader struct {
	discoverRows []recommendations.ForYouRow
}

func (s stubRecommendationsReader) GetForYouMain(
	context.Context,
	int,
	string,
	int,
	catalog.AccessFilter,
) (*recommendations.ForYouRow, error) {
	return nil, nil
}

func (s stubRecommendationsReader) GetForYouRows(
	context.Context,
	int,
	string,
	int,
	catalog.AccessFilter,
) ([]recommendations.ForYouRow, error) {
	return nil, nil
}

func (s stubRecommendationsReader) GetSimilarUsersLiked(
	context.Context,
	int,
	string,
	int,
	catalog.AccessFilter,
) ([]recommendations.ScoredItem, error) {
	return nil, nil
}

func (s stubRecommendationsReader) GetDiscoverRows(
	context.Context,
	int,
	string,
	int,
	catalog.AccessFilter,
) ([]recommendations.ForYouRow, error) {
	return s.discoverRows, nil
}

func (s stubRecommendationsReader) GetSection(
	context.Context,
	int,
	string,
	string,
	string,
	int,
	catalog.AccessFilter,
) (*recommendations.ForYouRow, error) {
	return nil, nil
}

func (s stubRecommendationsReader) GetWatchTonight(
	context.Context,
	int,
	string,
	int,
	catalog.AccessFilter,
) (recommendations.WatchTonightResult, error) {
	return recommendations.WatchTonightResult{}, nil
}

type stubDiscoverFetcher struct {
	items []*models.MediaItem
}

func (s stubDiscoverFetcher) FetchItemsByContentIDs(
	context.Context,
	[]string,
	catalog.AccessFilter,
) ([]*models.MediaItem, error) {
	return s.items, nil
}

func (s stubDiscoverFetcher) FetchEpisodesByContentIDs(
	context.Context,
	[]string,
	catalog.AccessFilter,
) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	return nil, map[string]sections.SectionItemMeta{}, nil
}

func (s stubDiscoverFetcher) ListOverlaySummaries(
	context.Context,
	[]string,
	catalog.AccessFilter,
) (map[string]*models.OverlaySummary, error) {
	return map[string]*models.OverlaySummary{}, nil
}

type stubCalendarRepository struct {
	err error
}

func (s stubCalendarRepository) ListEvents(
	context.Context,
	catalog.CalendarFilter,
) ([]catalog.CalendarEvent, error) {
	return nil, s.err
}

func movieCalendarEvent(contentID string, airDate string, airTime *string) catalog.CalendarEvent {
	return catalog.CalendarEvent{
		ContentID: contentID,
		Type:      "movie",
		Title:     contentID,
		AirDate:   mustCalendarDate(airDate),
		AirTime:   airTime,
	}
}

func episodeCalendarEvent(
	contentID string,
	seriesID string,
	seasonNumber int,
	episodeNumber int,
	airDate string,
	airTime *string,
) catalog.CalendarEvent {
	return catalog.CalendarEvent{
		ContentID:     contentID,
		Type:          "episode",
		Title:         seriesID,
		SeriesID:      &seriesID,
		SeasonNumber:  &seasonNumber,
		EpisodeNumber: &episodeNumber,
		AirDate:       mustCalendarDate(airDate),
		AirTime:       airTime,
		IsPremiere:    episodeNumber == 1,
	}
}

func mustCalendarDate(value string) time.Time {
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		panic(err)
	}
	return t
}

func testStringPtr(value string) *string {
	return &value
}
