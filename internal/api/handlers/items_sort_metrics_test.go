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
}

func TestListSortMetricsExcludesHiddenEbookReaderProgress(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	updatedAt := time.Date(2026, 6, 3, 9, 15, 0, 0, time.UTC)
	handler := &ItemsHandler{
		ebookProgressStore: &fakeEbookReaderProgressLister{
			progress: map[string]EbookReaderProgress{
				"ebook-hidden": {
					UserID:    42,
					ProfileID: "profile-1",
					ContentID: "ebook-hidden",
					Progress:  0.95,
					UpdatedAt: updatedAt,
				},
				"ebook-reread": {
					UserID:    42,
					ProfileID: "profile-1",
					ContentID: "ebook-reread",
					Progress:  0.95,
					UpdatedAt: updatedAt.Add(2 * time.Hour),
				},
			},
			hiddenBefore: map[string]time.Time{
				"ebook-hidden": updatedAt.Add(time.Hour),
				"ebook-reread": updatedAt.Add(time.Hour),
			},
		},
	}
	items := []*models.MediaItem{
		{ContentID: "ebook-hidden", Type: "ebook", Title: "Hidden"},
		{ContentID: "ebook-reread", Type: "ebook", Title: "Reread"},
	}

	viewedMetrics := handler.listSortMetrics(ctx, items, "date_viewed", catalog.AccessFilter{}, nil, store, 42, "profile-1")
	if got := viewedMetrics["ebook-hidden"]; got != nil {
		t.Fatalf("hidden ebook must have no viewed metrics, got %#v", got)
	}
	if got := viewedMetrics["ebook-reread"]; got == nil || got.ViewedAt != "2026-06-03T11:15:00Z" {
		t.Fatalf("progress updated after hidden_before must count again, got %#v", got)
	}

	playsMetrics := handler.listSortMetrics(ctx, items, "plays", catalog.AccessFilter{}, nil, store, 42, "profile-1")
	if got := playsMetrics["ebook-hidden"]; got != nil {
		t.Fatalf("hidden ebook must have no play count, got %#v", got)
	}
	if got := playsMetrics["ebook-reread"]; got == nil || got.PlayCount == nil || *got.PlayCount != 1 {
		t.Fatalf("progress updated after hidden_before must count again, got %#v", got)
	}
}

type fakeEbookReaderProgressLister struct {
	progress map[string]EbookReaderProgress
	// hiddenBefore models user_history_hidden_items: like the PG store, a
	// row is excluded while progress.UpdatedAt <= hidden_before and counts
	// again once the progress row is updated after the hide.
	hiddenBefore map[string]time.Time
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
		if hiddenBefore, hidden := f.hiddenBefore[contentID]; hidden && !progress.UpdatedAt.After(hiddenBefore) {
			continue
		}
		result[contentID] = progress
	}
	return result, nil
}
