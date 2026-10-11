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

// TestPostgresCollectionSyncStateKeepsAScheduleEditedDuringTheSync pins the
// compare-and-set on sync completion: a sync writes next_sync_at only while
// the stored schedule and next_sync_at are the ones it started with, so an
// edit made while it ran, on any node, keeps the next_sync_at the edit
// computed, even when the edit saved the same cadence again. The rest of the
// sync state is written either way.
func TestPostgresCollectionSyncStateKeepsAScheduleEditedDuringTheSync(t *testing.T) {
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
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, fmt.Sprintf("collection-sync-state-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM user_collection_revisions WHERE user_id=$1`, userID)
	})
	store := newStore(pool, userID)
	const profile = "sync-state-profile"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Sync"}); err != nil {
		t.Fatal(err)
	}
	daily, weekly := "30 4 * * *", "30 4 * * 0"
	base := time.Date(2026, 10, 4, 4, 30, 0, 0, time.UTC)
	dailyNext, weeklyNext, syncNext := base.Add(24*time.Hour), base.Add(7*24*time.Hour), base.Add(25*time.Hour)
	editNext := base.Add(23 * time.Hour)

	create := func(t *testing.T) *userstore.Collection {
		t.Helper()
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
			CreatorProfileID: profile, Name: "Synced", CollectionType: "mdblist", QueryDefinition: "{}",
			SourceConfig: `{"mode":"mdblist","url":"https://mdblist.com/lists/user/list"}`,
			SyncSchedule: &daily, NextSyncAt: &dailyNext,
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// finish completes a sync that started on start's schedule and next run.
	finish := func(t *testing.T, start *userstore.Collection) *userstore.Collection {
		t.Helper()
		if err := store.UpdateCollectionSyncState(ctx, userstore.UpdateCollectionSyncStateInput{
			ID: start.ID, Status: "success", Message: "Matched 3 of 3 entries", ItemCount: 3,
			LastSyncAt: base, NextSyncAt: &syncNext,
			ScheduleAtStart: start.SyncSchedule, NextSyncAtAtStart: start.NextSyncAt,
		}); err != nil {
			t.Fatal(err)
		}
		c, err := store.GetCollection(ctx, start.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.LastSyncAt == nil || !c.LastSyncAt.Equal(base) || c.LastSyncStatus != "success" || c.LastSyncMessage != "Matched 3 of 3 entries" || c.ItemCount != 3 {
			t.Fatalf("sync state not written: last %v %q %q, count %d", c.LastSyncAt, c.LastSyncStatus, c.LastSyncMessage, c.ItemCount)
		}
		return c
	}

	t.Run("an unchanged schedule takes the sync's next run", func(t *testing.T) {
		c := create(t)
		got := finish(t, c)
		if got.NextSyncAt == nil || !got.NextSyncAt.Equal(syncNext) {
			t.Fatalf("next_sync_at = %v, want %v", got.NextSyncAt, syncNext)
		}
	})
	t.Run("a schedule turned off during the sync stays off", func(t *testing.T) {
		c := create(t)
		if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: profile, ClearSyncSchedule: true, ClearNextSyncAt: true}); err != nil {
			t.Fatal(err)
		}
		got := finish(t, c)
		if got.SyncSchedule != nil || got.NextSyncAt != nil {
			t.Fatalf("schedule %v, next_sync_at %v; want both null", got.SyncSchedule, got.NextSyncAt)
		}
	})
	t.Run("a schedule changed during the sync keeps its own next run", func(t *testing.T) {
		c := create(t)
		if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: profile, SyncSchedule: &weekly, NextSyncAt: &weeklyNext}); err != nil {
			t.Fatal(err)
		}
		got := finish(t, c)
		if got.SyncSchedule == nil || *got.SyncSchedule != weekly || got.NextSyncAt == nil || !got.NextSyncAt.Equal(weeklyNext) {
			t.Fatalf("schedule %v, next_sync_at %v; want %q, %v", got.SyncSchedule, got.NextSyncAt, weekly, weeklyNext)
		}
	})
	t.Run("a schedule turned on during the sync keeps its own next run", func(t *testing.T) {
		c := create(t)
		if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: profile, ClearSyncSchedule: true, ClearNextSyncAt: true}); err != nil {
			t.Fatal(err)
		}
		off, err := store.GetCollection(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: profile, SyncSchedule: &weekly, NextSyncAt: &weeklyNext}); err != nil {
			t.Fatal(err)
		}
		got := finish(t, off)
		if got.NextSyncAt == nil || !got.NextSyncAt.Equal(weeklyNext) {
			t.Fatalf("next_sync_at = %v, want %v", got.NextSyncAt, weeklyNext)
		}
	})
	t.Run("a schedule saved again during the sync keeps the edit's next run", func(t *testing.T) {
		c := create(t)
		if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: profile, SyncSchedule: &daily, NextSyncAt: &editNext}); err != nil {
			t.Fatal(err)
		}
		got := finish(t, c)
		if got.NextSyncAt == nil || !got.NextSyncAt.Equal(editNext) {
			t.Fatalf("next_sync_at = %v, want %v", got.NextSyncAt, editNext)
		}
	})
	t.Run("a schedule changed and changed back during the sync keeps the edit's next run", func(t *testing.T) {
		c := create(t)
		if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: profile, SyncSchedule: &weekly, NextSyncAt: &weeklyNext}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: profile, SyncSchedule: &daily, NextSyncAt: &editNext}); err != nil {
			t.Fatal(err)
		}
		got := finish(t, c)
		if got.SyncSchedule == nil || *got.SyncSchedule != daily || got.NextSyncAt == nil || !got.NextSyncAt.Equal(editNext) {
			t.Fatalf("schedule %v, next_sync_at %v; want %q, %v", got.SyncSchedule, got.NextSyncAt, daily, editNext)
		}
	})
}
