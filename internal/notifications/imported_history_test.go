package notifications

import (
	"database/sql"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestImportedHistoryQueuesOnlyCreatedRows(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	db.SetMaxOpenConns(1)
	if err := userdb.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	updater := &InterestUpdater{pending: map[interestMutation]int{}}
	store := &interestTrackingStore{UserStore: userdb.NewSQLiteUserStore(db), userID: 1, system: &System{}, updater: updater}
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "p", Name: "Profile"}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := userstore.WatchHistoryEntry{ProfileID: "p", MediaItemID: "movie", WatchedAt: stamp.Format(time.RFC3339), Completed: true}
	created, err := store.AddHistoryIfMissing(t.Context(), entry)
	if err != nil || !created {
		t.Fatalf("first=%v %v", created, err)
	}
	if len(updater.pending) != 1 {
		t.Fatalf("pending=%d", len(updater.pending))
	}
	clear(updater.pending)
	created, err = store.AddHistoryIfMissing(t.Context(), entry)
	if err != nil || created {
		t.Fatalf("replay=%v %v", created, err)
	}
	if len(updater.pending) != 0 {
		t.Fatal("duplicate history queued interest work")
	}
	if err := store.RemoveHistoryItems(t.Context(), "p", []string{"movie"}, stamp.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	clear(updater.pending)
	created, err = store.AddHistoryIfMissing(t.Context(), entry)
	if err != nil || created {
		t.Fatalf("hidden=%v %v", created, err)
	}
	if len(updater.pending) != 0 {
		t.Fatal("suppressed history queued interest work")
	}
}
