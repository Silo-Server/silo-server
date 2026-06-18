package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestResolveItemUserStatesIncludesCompletedEbookReaderProgress(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	items := []*models.MediaItem{
		{ContentID: "ebook-complete", Type: "ebook", Title: "Complete Ebook"},
		{ContentID: "ebook-progress", Type: "ebook", Title: "Partial Ebook"},
	}

	states, err := resolveItemUserStatesWithOptions(ctx, store, "profile-1", nil, items, itemUserStateOptions{
		UserID: 42,
		EbookProgressStore: &fakeEbookReaderProgressLister{
			progress: map[string]EbookReaderProgress{
				"ebook-complete": {
					UserID:    42,
					ProfileID: "profile-1",
					ContentID: "ebook-complete",
					Progress:  0.95,
					UpdatedAt: time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC),
				},
				"ebook-progress": {
					UserID:    42,
					ProfileID: "profile-1",
					ContentID: "ebook-progress",
					Progress:  0.45,
					UpdatedAt: time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("resolveItemUserStatesWithOptions: %v", err)
	}

	if states["ebook-complete"] == nil || !states["ebook-complete"].Played {
		t.Fatalf("completed ebook state = %+v, want played", states["ebook-complete"])
	}
	if states["ebook-progress"] == nil || states["ebook-progress"].Played {
		t.Fatalf("partial ebook state = %+v, want not played", states["ebook-progress"])
	}
}

func TestResolveItemUserStatesExcludesHiddenEbookReaderProgress(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	updatedAt := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	items := []*models.MediaItem{
		{ContentID: "ebook-hidden", Type: "ebook", Title: "Hidden Ebook"},
		{ContentID: "ebook-reread", Type: "ebook", Title: "Reread Ebook"},
	}

	states, err := resolveItemUserStatesWithOptions(ctx, store, "profile-1", nil, items, itemUserStateOptions{
		UserID: 42,
		EbookProgressStore: &fakeEbookReaderProgressLister{
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
	})
	if err != nil {
		t.Fatalf("resolveItemUserStatesWithOptions: %v", err)
	}

	if states["ebook-hidden"] == nil || states["ebook-hidden"].Played {
		t.Fatalf("hidden ebook state = %+v, want not played", states["ebook-hidden"])
	}
	if states["ebook-reread"] == nil || !states["ebook-reread"].Played {
		t.Fatalf("ebook updated after hidden_before = %+v, want played again", states["ebook-reread"])
	}
}

func TestResolveItemUserStatesIncludesCompletedHistory(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	addCompletedHistoryForUserDataTest(t, store, "movie-history-only")
	items := []*models.MediaItem{
		{ContentID: "movie-history-only", Type: "movie", Title: "Imported Movie"},
	}

	states, err := resolveItemUserStates(ctx, store, "profile-1", nil, items)
	if err != nil {
		t.Fatalf("resolveItemUserStates: %v", err)
	}
	if states["movie-history-only"] == nil || !states["movie-history-only"].Played {
		t.Fatalf("history-only movie state = %+v, want played", states["movie-history-only"])
	}
}

func TestAllEpisodesCompletedIncludesCompletedHistory(t *testing.T) {
	episodes := []*models.Episode{{ContentID: "episode-progress"}, {ContentID: "episode-history"}}
	progress := map[string]userstore.WatchProgress{
		"episode-progress": {MediaItemID: "episode-progress", Completed: true},
		"episode-history":  {MediaItemID: "episode-history", Completed: true},
	}

	if !allEpisodesCompleted(episodes, progress) {
		t.Fatal("allEpisodesCompleted = false, want completed from progress plus history")
	}
}
