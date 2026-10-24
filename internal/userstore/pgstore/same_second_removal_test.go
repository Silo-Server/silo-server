package pgstore

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// Imported progress and history carry the event's own time, and a history
// removal hides everything at or before its watermark, to the microsecond. A
// write later in the same second as the removal must stay visible, while one
// at or before the removal stays hidden.
func TestImportedWritesAfterSameSecondRemovalDB(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	userID := seedTestUser(t, pool, "same-second-removal")
	store := newStore(pool, userID)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Viewer"}); err != nil {
		t.Fatal(err)
	}

	second := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	removedAt := second.Add(500 * time.Millisecond)
	stale := []time.Time{second.Add(300 * time.Millisecond), removedAt}
	later := second.Add(900 * time.Millisecond)

	for _, tc := range []struct {
		name  string
		write func(mediaItemID string, at time.Time) error
	}{
		{"SetProgressAt", func(mediaItemID string, at time.Time) error {
			return store.SetProgressAt(ctx, "p1", mediaItemID, 0, 600, true, at)
		}},
		{"SetProgressIfNewer", func(mediaItemID string, at time.Time) error {
			_, err := store.SetProgressIfNewer(ctx, "p1", mediaItemID, 0, 600, true, at)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mediaItemID := "same-second-" + tc.name
			if err := tc.write(mediaItemID, second.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := store.RemoveHistoryItems(ctx, "p1", []string{mediaItemID}, removedAt); err != nil {
				t.Fatal(err)
			}
			for _, at := range stale {
				if err := tc.write(mediaItemID, at); err != nil {
					t.Fatal(err)
				}
				if progress, err := store.GetProgress(ctx, "p1", mediaItemID); err != nil || progress != nil {
					t.Fatalf("write at %s, at or before the %s removal: progress %+v, err %v; want hidden", at.Format(time.RFC3339Nano), removedAt.Format(time.RFC3339Nano), progress, err)
				}
			}
			if err := tc.write(mediaItemID, later); err != nil {
				t.Fatal(err)
			}
			if progress, err := store.GetProgress(ctx, "p1", mediaItemID); err != nil || progress == nil || !progress.Completed {
				t.Fatalf("write at %s, after the %s removal: progress %+v, err %v; want completed", later.Format(time.RFC3339Nano), removedAt.Format(time.RFC3339Nano), progress, err)
			}
		})
	}

	t.Run("AddHistoryIfMissing", func(t *testing.T) {
		const mediaItemID = "same-second-history"
		imported := func(at string) userstore.WatchHistoryEntry {
			return userstore.WatchHistoryEntry{ProfileID: "p1", MediaItemID: mediaItemID, WatchedAt: at, Completed: true, Source: userstore.WatchHistorySourceImport}
		}
		if created, err := store.AddHistoryIfMissing(ctx, imported(second.Add(-time.Hour).Format(time.RFC3339))); err != nil || !created {
			t.Fatalf("seed: created %v, err %v", created, err)
		}
		if err := store.RemoveHistoryItems(ctx, "p1", []string{mediaItemID}, removedAt); err != nil {
			t.Fatal(err)
		}
		for _, at := range stale {
			if created, err := store.AddHistoryIfMissing(ctx, imported(at.Format(time.RFC3339Nano))); err != nil || created {
				t.Fatalf("import at %s, at or before the %s removal: created %v, err %v; want hidden", at.Format(time.RFC3339Nano), removedAt.Format(time.RFC3339Nano), created, err)
			}
		}
		if created, err := store.AddHistoryIfMissing(ctx, imported(later.Format(time.RFC3339Nano))); err != nil || !created {
			t.Fatalf("import at %s, after the %s removal: created %v, err %v", later.Format(time.RFC3339Nano), removedAt.Format(time.RFC3339Nano), created, err)
		}
		// A replay, or the same play reported by another source a moment
		// later, is a duplicate rather than a second watch.
		for _, at := range []time.Time{later, later.Add(50 * time.Millisecond)} {
			if created, err := store.AddHistoryIfMissing(ctx, imported(at.Format(time.RFC3339Nano))); err != nil || created {
				t.Fatalf("import at %s, beside one at %s: created %v, err %v; want a duplicate", at.Format(time.RFC3339Nano), later.Format(time.RFC3339Nano), created, err)
			}
		}
		completed, err := store.ListCompletedHistoryItems(ctx, userstore.CompletedHistoryItemQuery{ProfileID: "p1", MediaItemIDs: []string{mediaItemID}})
		if err != nil || len(completed) != 1 {
			t.Fatalf("import at %s, after the %s removal: completed history %+v, err %v; want the item", later.Format(time.RFC3339Nano), removedAt.Format(time.RFC3339Nano), completed, err)
		}
		if rows, err := store.ListHistory(ctx, "p1", 10, 0); err != nil || len(rows) != 1 {
			t.Fatalf("history after a replayed import: %+v, err %v; want one row", rows, err)
		}

		// Imports outside the removal's second keep whole-second stamps, so a
		// replay with a sub-second time still matches a row stored before.
		next := second.Add(2 * time.Second)
		if created, err := store.AddHistoryIfMissing(ctx, imported(next.Format(time.RFC3339))); err != nil || !created {
			t.Fatalf("whole-second import: created %v, err %v", created, err)
		}
		if created, err := store.AddHistoryIfMissing(ctx, imported(next.Add(400*time.Millisecond).Format(time.RFC3339Nano))); err != nil || created {
			t.Fatalf("sub-second replay of a whole-second import: created %v, err %v; want a duplicate", created, err)
		}
	})

	// A mark unwatched keeps the play it cleared. A new play later in that
	// second is a different watch, not a duplicate of the kept one.
	t.Run("AddHistoryIfMissingAfterUnmark", func(t *testing.T) {
		const mediaItemID = "same-second-unmark"
		imported := func(at time.Time) userstore.WatchHistoryEntry {
			return userstore.WatchHistoryEntry{ProfileID: "p1", MediaItemID: mediaItemID, WatchedAt: at.Format(time.RFC3339Nano), Completed: true, Source: userstore.WatchHistorySourceImport}
		}
		if created, err := store.AddHistoryIfMissing(ctx, imported(second)); err != nil || !created {
			t.Fatalf("seed: created %v, err %v", created, err)
		}
		svc := watchstate.NewService(storetest.SingleStoreProvider{Store: store})
		if err := svc.RecordImportedMarkUnplayed(ctx, userID, "p1", mediaItemID, removedAt); err != nil {
			t.Fatal(err)
		}
		kept, err := store.ListCompletedHistory(ctx, userstore.CompletedHistoryQuery{ProfileID: "p1", MediaItemIDs: []string{mediaItemID}})
		if err != nil || len(kept) != 0 {
			t.Fatalf("completed history after the unmark: %+v, err %v; want none counting as watched", kept, err)
		}
		listed := 0
		rows, err := store.ListHistory(ctx, "p1", 50, 0)
		for _, row := range rows {
			if row.MediaItemID == mediaItemID {
				listed++
			}
		}
		if err != nil || listed != 1 {
			t.Fatalf("history after the unmark: %+v, err %v; want the kept play listed", rows, err)
		}
		if created, err := store.AddHistoryIfMissing(ctx, imported(later)); err != nil || !created {
			t.Fatalf("import at %s, after the %s unmark: created %v, err %v; want a new play", later.Format(time.RFC3339Nano), removedAt.Format(time.RFC3339Nano), created, err)
		}
		if created, err := store.AddHistoryIfMissing(ctx, imported(later.Add(50*time.Millisecond))); err != nil || created {
			t.Fatalf("replay of the new play: created %v, err %v; want a duplicate", created, err)
		}
	})
}
