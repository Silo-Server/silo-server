package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestListSortMetricsIncludesUserStateSorts(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	items := []*models.MediaItem{
		{ContentID: "movie-progress", Type: "movie", Title: "Progress"},
		{ContentID: "movie-viewed", Type: "movie", Title: "Viewed"},
		{ContentID: "movie-plays", Type: "movie", Title: "Plays"},
	}
	handler := &ItemsHandler{}

	if err := store.SetProgress(ctx, "profile-1", "movie-progress", 900, 3600, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("SetProgress(progress): %v", err)
	}
	completedAt := time.Date(2026, 5, 29, 18, 30, 0, 0, time.UTC)
	if err := store.SetProgressAt(ctx, "profile-1", "movie-viewed", 3600, 3600, true, completedAt); err != nil {
		t.Fatalf("SetProgressAt(viewed): %v", err)
	}
	for _, watchedAt := range []string{"2026-05-27T10:00:00Z", "2026-05-28T10:00:00Z"} {
		if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
			ProfileID:       "profile-1",
			MediaItemID:     "movie-plays",
			WatchedAt:       watchedAt,
			DurationSeconds: 3600,
			Completed:       true,
			Source:          userstore.WatchHistorySourcePlayback,
		}); err != nil {
			t.Fatalf("AddHistory(%s): %v", watchedAt, err)
		}
	}

	progressMetrics := handler.listSortMetrics(ctx, items, "progress", catalog.AccessFilter{}, nil, store, 0, "profile-1")
	progress := progressMetrics["movie-progress"]
	if progress == nil || progress.ProgressRatio == nil || *progress.ProgressRatio != 0.25 {
		t.Fatalf("progress metrics = %#v", progress)
	}

	viewedMetrics := handler.listSortMetrics(ctx, items, "date_viewed", catalog.AccessFilter{}, nil, store, 0, "profile-1")
	if got := viewedMetrics["movie-viewed"]; got == nil || got.ViewedAt != "2026-05-29T18:30:00Z" {
		t.Fatalf("viewed metrics = %#v", got)
	}

	playsMetrics := handler.listSortMetrics(ctx, items, "plays", catalog.AccessFilter{}, nil, store, 0, "profile-1")
	plays := playsMetrics["movie-plays"]
	if plays == nil || plays.PlayCount == nil || *plays.PlayCount != 2 {
		t.Fatalf("plays metrics = %#v", plays)
	}
}

func TestListSortMetricsIncludesEbookReaderProgress(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	updatedAt := time.Date(2026, 6, 3, 9, 15, 0, 0, time.UTC)
	handler := &ItemsHandler{
		ebookProgressStore: &fakeEbookReaderProgressLister{
			progress: map[string]EbookReaderProgress{
				"ebook-progress": {
					UserID:    42,
					ProfileID: "profile-1",
					ContentID: "ebook-progress",
					Progress:  0.37,
					UpdatedAt: updatedAt,
				},
				"ebook-complete": {
					UserID:    42,
					ProfileID: "profile-1",
					ContentID: "ebook-complete",
					Progress:  0.95,
					UpdatedAt: updatedAt.Add(time.Hour),
				},
			},
		},
	}
	items := []*models.MediaItem{
		{ContentID: "ebook-progress", Type: "ebook", Title: "Progress"},
		{ContentID: "ebook-complete", Type: "ebook", Title: "Complete"},
		{ContentID: "ebook-unseen", Type: "ebook", Title: "Unread"},
	}

	progressMetrics := handler.listSortMetrics(ctx, items, "progress", catalog.AccessFilter{}, nil, store, 42, "profile-1")
	progress := progressMetrics["ebook-progress"]
	if progress == nil || progress.ProgressRatio == nil || *progress.ProgressRatio != 0.37 {
		t.Fatalf("progress metrics = %#v", progress)
	}

	viewedMetrics := handler.listSortMetrics(ctx, items, "date_viewed", catalog.AccessFilter{}, nil, store, 42, "profile-1")
	if got := viewedMetrics["ebook-complete"]; got == nil || got.ViewedAt != "2026-06-03T10:15:00Z" {
		t.Fatalf("viewed metrics = %#v", got)
	}

	playsMetrics := handler.listSortMetrics(ctx, items, "plays", catalog.AccessFilter{}, nil, store, 42, "profile-1")
	plays := playsMetrics["ebook-complete"]
	if plays == nil || plays.PlayCount == nil || *plays.PlayCount != 1 {
		t.Fatalf("plays metrics = %#v", plays)
	}
	if viewedMetrics["ebook-unseen"] != nil || playsMetrics["ebook-unseen"] != nil {
		t.Fatal("missing reader progress produced viewed or play-count metrics")
	}
}

// TestListSortMetricsDatesSeriesByEpisodeViews checks that a series card in a
// date viewed sort shows its store rollup's latest episode play, since plays
// are recorded against episodes (#4638).
func TestListSortMetricsDatesSeriesByEpisodeViews(t *testing.T) {
	ctx := context.Background()
	store := seriesViewedAtStore{UserStore: newProfileTestStore(t), viewedAt: map[string]string{
		"series-watched": "2026-05-28T10:00:00Z",
	}}
	items := []*models.MediaItem{
		{ContentID: "series-watched", Type: "series", Title: "Watched"},
		{ContentID: "series-unseen", Type: "series", Title: "Unseen"},
	}

	metrics := (&ItemsHandler{}).listSortMetrics(ctx, items, "date_viewed", catalog.AccessFilter{}, nil, store, 0, "profile-1")
	if got := metrics["series-watched"]; got == nil || got.ViewedAt != "2026-05-28T10:00:00Z" {
		t.Fatalf("series-watched viewed metrics = %#v", got)
	}
	if got := metrics["series-unseen"]; got != nil {
		t.Fatalf("unwatched series was dated: %#v", got)
	}
}

// seriesViewedAtStore adds a fixed series rollup to a user store; only
// SeriesLatestViewedAt answers.
type seriesViewedAtStore struct {
	userstore.UserStore
	userstore.SeriesEpisodeRollupStore
	viewedAt map[string]string
}

func (s seriesViewedAtStore) SeriesLatestViewedAt(_ context.Context, _ string, seriesIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(seriesIDs))
	for _, id := range seriesIDs {
		if at, ok := s.viewedAt[id]; ok {
			result[id] = at
		}
	}
	return result, nil
}

type fakeEbookReaderProgressLister struct {
	progress map[string]EbookReaderProgress
}

func (f *fakeEbookReaderProgressLister) ListByContentIDs(
	_ context.Context,
	_ int,
	_ string,
	contentIDs []string,
) (map[string]EbookReaderProgress, error) {
	result := make(map[string]EbookReaderProgress, len(contentIDs))
	for _, contentID := range contentIDs {
		progress, ok := f.progress[contentID]
		if !ok {
			continue
		}
		result[contentID] = progress
	}
	return result, nil
}
