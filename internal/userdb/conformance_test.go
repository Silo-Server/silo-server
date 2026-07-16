package userdb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func newConformanceStore(t *testing.T) userstore.UserStore {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return NewSQLiteUserStore(db)
}

// TestSQLiteProgressSince runs the offline-sync progress-reconciliation
// conformance test (invariant 1) against the real SQLite backend, exercising the
// synced_seq stamping triggers and event_at LWW comparison.
func TestSQLiteProgressSince(t *testing.T) {
	storetest.RunProgressSince(t, newConformanceStore)
}

func TestSQLiteAddFavoriteAtReportsInsertion(t *testing.T) {
	ctx := context.Background()
	store := newConformanceStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	addedAt := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	inserted, err := store.AddFavoriteAt(ctx, "p1", "movie-1", addedAt)
	if err != nil {
		t.Fatalf("first AddFavoriteAt: %v", err)
	}
	if !inserted {
		t.Fatal("first AddFavoriteAt reported no insertion")
	}
	inserted, err = store.AddFavoriteAt(ctx, "p1", "movie-1", addedAt)
	if err != nil {
		t.Fatalf("duplicate AddFavoriteAt: %v", err)
	}
	if inserted {
		t.Fatal("duplicate AddFavoriteAt reported an insertion")
	}
}
