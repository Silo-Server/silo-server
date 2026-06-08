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

	progressMetrics := handler.listSortMetrics(ctx, items, "progress", catalog.AccessFilter{}, nil, store, "profile-1")
	progress := progressMetrics["movie-progress"]
	if progress == nil || progress.ProgressRatio == nil || *progress.ProgressRatio != 0.25 {
		t.Fatalf("progress metrics = %#v", progress)
	}

	viewedMetrics := handler.listSortMetrics(ctx, items, "date_viewed", catalog.AccessFilter{}, nil, store, "profile-1")
	if got := viewedMetrics["movie-viewed"]; got == nil || got.ViewedAt != "2026-05-29T18:30:00Z" {
		t.Fatalf("viewed metrics = %#v", got)
	}

	playsMetrics := handler.listSortMetrics(ctx, items, "plays", catalog.AccessFilter{}, nil, store, "profile-1")
	plays := playsMetrics["movie-plays"]
	if plays == nil || plays.PlayCount == nil || *plays.PlayCount != 2 {
		t.Fatalf("plays metrics = %#v", plays)
	}
}
