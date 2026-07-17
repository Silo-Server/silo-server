package historyimport

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestAddFavoriteUsesAtomicInsertResult(t *testing.T) {
	store := &favoriteInsertTestStore{}
	service := &Service{stores: favoriteInsertTestProvider{store: store}}

	inserted, err := service.addFavorite(context.Background(), 42, "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("addFavorite: %v", err)
	}
	if inserted {
		t.Fatal("addFavorite reported an existing favorite as newly inserted")
	}
	if store.addCalls != 1 {
		t.Fatalf("AddFavoriteAt calls = %d, want 1", store.addCalls)
	}
	if store.isFavoriteCalls != 0 {
		t.Fatalf("IsFavorite calls = %d, want 0", store.isFavoriteCalls)
	}
}

func TestEmbyFavoriteOnlyRecordAddsFavoriteWithoutProgress(t *testing.T) {
	ctx := context.Background()
	pool := newPlexWatchlistImportTestPool(t)
	repo := NewRepository(pool, nil)
	service := &Service{
		repo:         repo,
		matcher:      NewMatcher(repo),
		stores:       pgstore.NewPostgresProvider(pool),
		bgContext:    ctx,
		runSemaphore: make(chan struct{}, maxConcurrentRuns),
		runCancels:   make(map[string]context.CancelFunc),
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, year, tvdb_id, status)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		"series-371980", KindSeries, "Severance", 2022, "371980", "matched",
	); err != nil {
		t.Fatalf("seed media item: %v", err)
	}
	run, err := repo.CreateRun(ctx, Run{
		ID:               "emby-favorites-run",
		UserID:           42,
		ProfileID:        "profile-1",
		SourceType:       SourceTypeEmby,
		ConnectionMode:   ConnectionModeCustom,
		Status:           RunStatusQueued,
		Warnings:         []string{},
		UnmatchedSamples: []UnmatchedSample{},
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	record := Record{
		ExternalID:   "emby-series-1",
		Kind:         KindSeries,
		Title:        "Severance",
		Year:         2022,
		TVDBID:       "371980",
		Favorite:     true,
		FavoriteOnly: true,
	}
	service.executeRun(run, staticWatchlistProvider{records: []Record{record, record}})

	completed, err := repo.GetRunForUser(ctx, 42, run.ID)
	if err != nil {
		t.Fatalf("GetRunForUser: %v", err)
	}
	if completed.Status != RunStatusCompleted {
		t.Fatalf("run status = %q, want %q; warnings=%v", completed.Status, RunStatusCompleted, completed.Warnings)
	}
	if completed.FavoritesImported != 1 || completed.ProgressUpdated != 0 || completed.HistoryCreated != 0 {
		t.Fatalf("run counters = favorites:%d progress:%d history:%d, want 1/0/0",
			completed.FavoritesImported, completed.ProgressUpdated, completed.HistoryCreated)
	}
	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM user_favorites
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		42, "profile-1", "series-371980",
	).Scan(&rows); err != nil {
		t.Fatalf("count favorite rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("favorite rows = %d, want 1", rows)
	}
}

type favoriteInsertTestProvider struct {
	store userstore.UserStore
}

func (p favoriteInsertTestProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (favoriteInsertTestProvider) Close() error { return nil }

type favoriteInsertTestStore struct {
	userstore.UserStore
	addCalls        int
	isFavoriteCalls int
}

func (s *favoriteInsertTestStore) AddFavoriteAt(context.Context, string, string, time.Time) (bool, error) {
	s.addCalls++
	return false, nil
}

func (s *favoriteInsertTestStore) IsFavorite(context.Context, string, string) (bool, error) {
	s.isFavoriteCalls++
	return false, nil
}
