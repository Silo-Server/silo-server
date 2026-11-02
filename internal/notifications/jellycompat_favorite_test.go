package notifications

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

func TestAtomicJellycompatFavoritesNotifyOnlyAfterCommit(t *testing.T) {
	edit := userstore.JellycompatProgressEdit{MediaItemID: "item-1", PositionSeconds: 123, DurationSeconds: 600, EventAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)}
	for _, tc := range []struct {
		name       string
		favoriteID string
		record     func(*watchstate.Service, bool) error
		// history rows a failed unfavorite leaves, and interest updates a commit queues
		history, pending int
	}{
		{
			name:       "item",
			favoriteID: "item-1",
			record: func(service *watchstate.Service, favorite bool) error {
				edit.IsFavorite, edit.Completed = new(favorite), favorite
				return service.RecordJellycompatProgress(t.Context(), 1, "profile-1", edit, new(favorite))
			},
			history: 1,
			pending: 1,
		},
		{
			name:       "parent",
			favoriteID: "series",
			record: func(service *watchstate.Service, favorite bool) error {
				return service.RecordJellycompatParent(t.Context(), 1, "profile-1", "series", []string{"episode-1", "episode-2"}, favorite, favorite)
			},
			history: 2,
			pending: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openUserDB(t)
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
				if err := tc.record(service, favorite); err == nil {
					t.Fatal("favorite failure ignored")
				}
				if observer.calls != before || len(updater.pending) != 0 {
					t.Fatal("failed favorite edit notified observers")
				}
				history, err := store.ListHistory(t.Context(), "profile-1", 10, 0)
				expected := 0
				if !favorite {
					expected = tc.history
				}
				if err != nil || len(history) != expected {
					t.Fatalf("rollback history=%+v error=%v", history, err)
				}
				if _, err := db.Exec("DROP TRIGGER fail_favorite"); err != nil {
					t.Fatal(err)
				}
				if err := tc.record(service, favorite); err != nil {
					t.Fatal(err)
				}
				if len(updater.pending) != tc.pending {
					t.Fatal("committed favorite mutation did not queue interest update")
				}
				if observer.calls != 1 {
					t.Fatalf("completion callbacks=%d", observer.calls)
				}
				actual, err := store.IsFavorite(t.Context(), "profile-1", tc.favoriteID)
				if err != nil || actual != favorite {
					t.Fatalf("favorite=%v error=%v", actual, err)
				}
				clear(updater.pending)
			}
		})
	}
}
