package userstore_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestGetProgressWithCompletedHistoryCarriesHistoryTimestamp(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Profile"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if err := store.AddHistory(context.Background(), userstore.WatchHistoryEntry{
		ProfileID:       "profile-1",
		MediaItemID:     "movie-history-only",
		WatchedAt:       "2026-05-04T12:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourceTrakt,
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}

	progress, err := userstore.GetProgressWithCompletedHistory(context.Background(), store, "profile-1", "movie-history-only")
	if err != nil {
		t.Fatalf("GetProgressWithCompletedHistory: %v", err)
	}
	if progress == nil || !progress.Completed {
		t.Fatalf("progress = %+v, want synthetic completed progress", progress)
	}
	if progress.UpdatedAt != "2026-05-04T12:00:00Z" {
		t.Fatalf("UpdatedAt = %q, want history watched_at", progress.UpdatedAt)
	}
}
