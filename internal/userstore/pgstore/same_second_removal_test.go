package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Imported progress and history carry the event's own time, and a history
// removal hides everything at or before its watermark, to the microsecond. A
// write later in the same second as the removal must stay visible, while one
// at or before the removal stays hidden.
func TestImportedWritesAfterSameSecondRemovalDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`,
		fmt.Sprintf("same-second-removal-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
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
}
