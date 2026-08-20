package userdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func newConformanceStore(t *testing.T) userstore.UserStore {
	t.Helper()
	db, err := NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
	if err != nil {
		t.Fatalf("open user database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewSQLiteUserStore(db.DB)
}

// TestSQLiteProgressSince runs the offline-sync progress-reconciliation
// conformance test (invariant 1) against the real SQLite backend, exercising the
// synced_seq stamping triggers and event_at LWW comparison.
func TestSQLiteProgressSince(t *testing.T) {
	storetest.RunProgressSince(t, newConformanceStore)
}

// TestSQLiteMarkWatchedBatch runs the batch mark-watched conformance test
// (series/season mark-watched) against the real SQLite backend. The Postgres
// backend runs the same suite in internal/userstore/pgstore, which is what
// keeps the two transactional implementations from drifting.
func TestSQLiteMarkWatchedBatch(t *testing.T) {
	storetest.RunMarkWatchedBatch(t, newConformanceStore)
}

// TestSQLiteSettingValues runs the canonical settings-contract storage
// conformance tests against the per-user SQLite backend. The Postgres backend
// runs the same suite in internal/userstore/pgstore, which is what keeps the two
// from drifting on scope identity, partial uniqueness and delete behavior.
func TestSQLiteSettingValues(t *testing.T) {
	storetest.RunSettingValues(t, newConformanceStore)
}

// TestSQLiteJellycompatDisplayPrefs runs the Jellyfin DisplayPreferences
// storage conformance tests against the per-user SQLite backend; the Postgres
// backend runs the same suite in internal/userstore/pgstore.
func TestSQLiteJellycompatDisplayPrefs(t *testing.T) {
	storetest.RunJellycompatDisplayPrefs(t, newConformanceStore)
}

func TestSQLiteCollectionSortPreferences(t *testing.T) {
	storetest.RunCollectionSortPreferences(t, newConformanceStore)
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
