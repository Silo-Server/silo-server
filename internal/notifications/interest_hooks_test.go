package notifications

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
)

// openUserDB opens an in-memory SQLite user database with the user schema,
// closed when the test ends.
func openUserDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return db
}

type preferenceTransactionTestProvider struct {
	store userstore.UserStore
}

func (p preferenceTransactionTestProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (preferenceTransactionTestProvider) Close() error { return nil }

// storeWithoutBatchWriter wraps a UserStore and hides the WatchedBatchWriter
// capability, standing in for a backend that has not implemented it.
type storeWithoutBatchWriter struct {
	userstore.UserStore
}

// rollupCapableStore is a UserStore that also implements the series rollup,
// standing in for the Postgres backend.
type rollupCapableStore struct {
	userstore.UserStore
	called bool
}

func (s *rollupCapableStore) SeriesEpisodeWatchCounts(_ context.Context, _ string, seriesIDs []string) (map[string]userstore.SeriesWatchCounts, error) {
	s.called = true
	counts := make(map[string]userstore.SeriesWatchCounts, len(seriesIDs))
	for _, seriesID := range seriesIDs {
		counts[seriesID] = userstore.SeriesWatchCounts{TotalEpisodes: 3, WatchedCount: 2}
	}
	return counts, nil
}

func (s *rollupCapableStore) SeriesSeasonWatchCounts(context.Context, string, string) (map[int]userstore.SeriesWatchCounts, error) {
	return map[int]userstore.SeriesWatchCounts{}, nil
}

func (s *rollupCapableStore) SeasonEpisodeWatchCounts(context.Context, string, []string) (map[string]userstore.SeriesWatchCounts, error) {
	return map[string]userstore.SeriesWatchCounts{}, nil
}

// completionCapableStore records both completion entry points so the decorator
// test exercises the production capability assertion and argument forwarding.
type completionCapableStore struct {
	call func(context.Context, string, string, []string) (map[string]bool, error)
}

func (s completionCapableStore) SeriesCompletion(ctx context.Context, profileID string, ids []string) (map[string]bool, error) {
	return s.call(ctx, "series", profileID, ids)
}

func (s completionCapableStore) SeasonCompletion(ctx context.Context, profileID string, ids []string) (map[string]bool, error) {
	return s.call(ctx, "season", profileID, ids)
}

type atomicProgressObserver struct{ calls int }

func (o *atomicProgressObserver) HandleWatchedCompleted(context.Context, int, string, []string) {
	o.calls++
}

func TestAtomicJellycompatProgressNotifiesOnlyAfterCommit(t *testing.T) {
	db := openUserDB(t)
	if _, err := db.Exec("CREATE TRIGGER fail_atomic_progress BEFORE INSERT ON watch_progress WHEN NEW.position_seconds = 321 BEGIN SELECT RAISE(ABORT, 'forced progress failure'); END"); err != nil {
		t.Fatal(err)
	}
	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "profile-1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	updater := &InterestUpdater{pending: map[interestMutation]int{}}
	wrapped := &interestTrackingStore{UserStore: store, userID: 1, updater: updater}
	observer := &atomicProgressObserver{}
	service := watchstate.NewService(preferenceTransactionTestProvider{store: wrapped}).WithCompletionObserver(observer)
	date := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	edit := userstore.JellycompatProgressEdit{MediaItemID: "item-1", PositionSeconds: 321, DurationSeconds: 600, Completed: true, EventAt: date}
	if err := service.RecordJellycompatProgress(t.Context(), 1, "profile-1", edit, new(true)); err == nil {
		t.Fatal("progress failure ignored")
	}
	if observer.calls != 0 || len(updater.pending) != 0 {
		t.Fatal("failed edit notified observers")
	}
	edit.PositionSeconds = 123
	if err := service.RecordJellycompatProgress(t.Context(), 1, "profile-1", edit, new(true)); err != nil {
		t.Fatal(err)
	}
	if observer.calls != 1 || len(updater.pending) != 1 {
		t.Fatal("committed edit did not notify observers")
	}
	clear(updater.pending)
	edit.PositionSeconds = 321
	edit.Completed = false
	if err := service.RecordJellycompatProgress(t.Context(), 1, "profile-1", edit, new(false)); err == nil {
		t.Fatal("unplayed failure ignored")
	}
	if observer.calls != 1 || len(updater.pending) != 0 {
		t.Fatal("failed unplayed edit notified observers")
	}
	progress, err := store.GetProgress(t.Context(), "profile-1", "item-1")
	if err != nil || progress == nil || !progress.Completed || progress.PositionSeconds != 123 {
		t.Fatalf("failed edit changed progress: %+v %v", progress, err)
	}
	history, err := store.ListHistory(t.Context(), "profile-1", 10, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("failed edit changed history: %+v %v", history, err)
	}
}

type catalogProgressRelationCapableStore struct {
	userstore.UserStore
}

func (catalogProgressRelationCapableStore) CatalogProgressRelation(_ *pgxpool.Pool, userID int, profileID string, firstArg int) (string, []any, bool) {
	return fmt.Sprintf("progress_$%d", firstArg), []any{userID, profileID}, true
}
