package pgstore

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestPostgresFailedSyncKeepsANewerResult pins the guard a failed sync writes
// with: it records only while last_sync_at is still the one the sync started
// from, so a sync that started later and finished first keeps its result.
func TestPostgresFailedSyncKeepsANewerResult(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	userID := seedTestUser(t, pool, "failed-sync-guard")
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_revisions WHERE user_id=$1`, userID)
	}()
	s := newStore(pool, userID)
	c, err := s.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "synced", CollectionType: "mdblist"})
	if err != nil {
		t.Fatal(err)
	}
	failedFrom := func(lastSyncAt *time.Time) userstore.UpdateCollectionSyncStateInput {
		return userstore.UpdateCollectionSyncStateInput{
			ID: c.ID, Status: "failed", Message: "list unavailable", ItemCount: 0,
			LastSyncAt: time.Now().UTC(), RequireLastSyncAt: true, LastSyncAtAtStart: lastSyncAt,
		}
	}
	read := func() *userstore.Collection {
		t.Helper()
		got, err := s.GetCollection(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// A sync that started before any sync finished, then the newer success.
	if err := s.UpdateCollectionSyncState(ctx, userstore.UpdateCollectionSyncStateInput{
		ID: c.ID, Status: "success", Message: "Matched 3 of 3 entries", ItemCount: 3, LastSyncAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	success := read()
	if err := s.UpdateCollectionSyncState(ctx, failedFrom(nil)); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.LastSyncStatus != "success" || got.ItemCount != 3 {
		t.Fatalf("stale failure overwrote the newer success: %s with %d items", got.LastSyncStatus, got.ItemCount)
	}

	// A failure that started from the stored sync is recorded.
	if err := s.UpdateCollectionSyncState(ctx, failedFrom(success.LastSyncAt)); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.LastSyncStatus != "failed" || got.LastSyncMessage != "list unavailable" {
		t.Fatalf("current failure not recorded: %s %q", got.LastSyncStatus, got.LastSyncMessage)
	}
}
