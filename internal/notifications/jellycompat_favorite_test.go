package notifications

import (
	"database/sql"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

func TestAtomicJellycompatFavoriteNotifiesOnlyAfterCommit(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
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
	edit := userstore.JellycompatProgressEdit{MediaItemID: "item-1", PositionSeconds: 123, DurationSeconds: 600, EventAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)}
	for _, favorite := range []bool{true, false} {
		operation := "INSERT"
		if !favorite {
			operation = "DELETE"
		}
		if _, err := db.Exec("CREATE TRIGGER fail_favorite BEFORE " + operation + " ON favorites BEGIN SELECT RAISE(ABORT, 'forced favorite failure'); END"); err != nil {
			t.Fatal(err)
		}
		edit.IsFavorite, edit.Completed = new(favorite), favorite
		before := observer.calls
		if err := service.RecordJellycompatProgress(t.Context(), 1, "profile-1", edit, new(favorite)); err == nil {
			t.Fatal("favorite failure ignored")
		}
		if observer.calls != before || len(updater.pending) != 0 {
			t.Fatal("failed favorite edit notified observers")
		}
		history, err := store.ListHistory(t.Context(), "profile-1", 10, 0)
		expected := 0
		if !favorite {
			expected = 1
		}
		if err != nil || len(history) != expected {
			t.Fatalf("rollback history=%+v error=%v", history, err)
		}
		if _, err := db.Exec("DROP TRIGGER fail_favorite"); err != nil {
			t.Fatal(err)
		}
		if err := service.RecordJellycompatProgress(t.Context(), 1, "profile-1", edit, new(favorite)); err != nil {
			t.Fatal(err)
		}
		if len(updater.pending) != 1 {
			t.Fatal("committed favorite mutation did not queue interest update")
		}
		if observer.calls != 1 {
			t.Fatalf("completion callbacks=%d", observer.calls)
		}
		actual, err := store.IsFavorite(t.Context(), "profile-1", "item-1")
		if err != nil || actual != favorite {
			t.Fatalf("favorite=%v error=%v", actual, err)
		}
		clear(updater.pending)
	}
}

func TestAtomicJellycompatParentFavoriteNotifiesOnlyAfterCommit(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
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

	for _, favorite := range []bool{true, false} {
		operation := "INSERT"
		if !favorite {
			operation = "DELETE"
		}
		if _, err := db.Exec("CREATE TRIGGER fail_favorite BEFORE " + operation + " ON favorites BEGIN SELECT RAISE(ABORT, 'forced favorite failure'); END"); err != nil {
			t.Fatal(err)
		}
		before := observer.calls
		if err := service.RecordJellycompatParent(t.Context(), 1, "profile-1", "series", []string{"episode-1", "episode-2"}, favorite, favorite); err == nil {
			t.Fatal("favorite failure ignored")
		}
		if observer.calls != before || len(updater.pending) != 0 {
			t.Fatal("failed favorite edit notified observers")
		}
		history, err := store.ListHistory(t.Context(), "profile-1", 10, 0)
		expected := 0
		if !favorite {
			expected = 2
		}
		if err != nil || len(history) != expected {
			t.Fatalf("rollback history=%+v error=%v", history, err)
		}
		if _, err := db.Exec("DROP TRIGGER fail_favorite"); err != nil {
			t.Fatal(err)
		}
		if err := service.RecordJellycompatParent(t.Context(), 1, "profile-1", "series", []string{"episode-1", "episode-2"}, favorite, favorite); err != nil {
			t.Fatal(err)
		}
		if len(updater.pending) != 3 {
			t.Fatal("committed favorite mutation did not queue interest update")
		}
		if observer.calls != 1 {
			t.Fatalf("completion callbacks=%d", observer.calls)
		}
		actual, err := store.IsFavorite(t.Context(), "profile-1", "series")
		if err != nil || actual != favorite {
			t.Fatalf("favorite=%v error=%v", actual, err)
		}
		clear(updater.pending)
	}
}
