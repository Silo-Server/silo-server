package usercollections

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// failingOwners fails every sync at the owner-access step, after running
// during, which stands in for whatever happens while the sync runs.
type failingOwners struct{ during func() }

func (o failingOwners) OwnerFilter(context.Context, int, string) (catalog.AccessFilter, error) {
	if o.during != nil {
		o.during()
	}
	return catalog.AccessFilter{}, errors.New("owner access unavailable")
}

// TestSchedulerFailedSyncRetryAndEditsDB pins a scheduled sync's claim on a
// due collection: one node runs it, a failed sync retries after the minimum
// interval, and a schedule edited while the sync ran, on any node, keeps
// what the edit wrote.
func TestSchedulerFailedSyncRetryAndEditsDB(t *testing.T) {
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
	var account int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("scheduler-failure-%d", time.Now().UnixNano())).Scan(&account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id=$1`, account)
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM user_collection_revisions WHERE user_id=$1`, account)
	})
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "owner", Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	// node builds one cluster node's scheduler whose syncs fail after
	// running during.
	node := func(during func()) *Scheduler {
		return NewScheduler(pool, NewService(provider, nil, nil, failingOwners{during: during}, nil, logger), logger)
	}
	daily, weekly := AllowedSyncSchedules["daily"], AllowedSyncSchedules["weekly"]
	due := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	interval := time.Duration(MinSyncIntervalHours) * time.Hour

	createDue := func(t *testing.T, schedule string) dueCollection {
		t.Helper()
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
			CreatorProfileID: "owner", Name: "Synced", CollectionType: "mdblist", QueryDefinition: "{}",
			SourceConfig: `{"mode":"mdblist_json","url":"https://mdblist.com/lists/user/list"}`,
			SyncSchedule: &schedule, NextSyncAt: &due,
		})
		if err != nil {
			t.Fatal(err)
		}
		return dueCollection{UserID: account, CollectionID: c.ID}
	}
	run := func(t *testing.T, s *Scheduler, dc dueCollection) SchedulerResult {
		t.Helper()
		var (
			mu     sync.Mutex
			result SchedulerResult
		)
		s.syncOne(ctx, dc, &mu, &result)
		return result
	}
	stored := func(t *testing.T, dc dueCollection) *userstore.Collection {
		t.Helper()
		got, err := store.GetCollection(ctx, dc.CollectionID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	dbNow := func(t *testing.T) time.Time {
		t.Helper()
		var now time.Time
		if err := pool.QueryRow(ctx, `SELECT NOW()`).Scan(&now); err != nil {
			t.Fatal(err)
		}
		return now
	}

	t.Run("a failed sync retries after the minimum interval on the database clock", func(t *testing.T) {
		dc := createDue(t, daily)
		before := dbNow(t)
		if got := run(t, node(nil), dc); got.Failed != 1 {
			t.Fatalf("result = %+v, want one failed sync", got)
		}
		after := dbNow(t)
		got := stored(t, dc)
		if got.NextSyncAt == nil || got.NextSyncAt.Before(before.Add(interval)) || got.NextSyncAt.After(after.Add(interval)) {
			t.Fatalf("next_sync_at = %v, want between %v and %v", got.NextSyncAt, before.Add(interval), after.Add(interval))
		}
	})
	t.Run("a collection another node already took is skipped", func(t *testing.T) {
		// Both nodes listed the collection as due. The first node's sync
		// fails; the second must not run it again, or its later success
		// would find the first node's retry time and keep it.
		dc := createDue(t, weekly)
		nodeA, nodeB := node(nil), node(nil)
		if got := run(t, nodeA, dc); got.Failed != 1 {
			t.Fatalf("node A result = %+v, want one failed sync", got)
		}
		if got := run(t, nodeB, dc); got.Skipped != 1 || got.Failed != 0 || got.Synced != 0 {
			t.Fatalf("node B result = %+v, want the collection skipped", got)
		}
	})
	t.Run("a schedule turned off during the sync stays off", func(t *testing.T) {
		var dc dueCollection
		s := node(func() {
			if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: dc.CollectionID, RequestProfileID: "owner", ClearSyncSchedule: true, ClearNextSyncAt: true}); err != nil {
				t.Error(err)
			}
		})
		dc = createDue(t, daily)
		run(t, s, dc)
		if got := stored(t, dc); got.SyncSchedule != nil || got.NextSyncAt != nil {
			t.Fatalf("schedule %v, next_sync_at %v; want both null", got.SyncSchedule, got.NextSyncAt)
		}
	})
	t.Run("a schedule changed during the sync keeps its own next run", func(t *testing.T) {
		weeklyNext := time.Now().Add(6 * 24 * time.Hour).UTC().Truncate(time.Microsecond)
		var dc dueCollection
		s := node(func() {
			if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: dc.CollectionID, RequestProfileID: "owner", SyncSchedule: &weekly, NextSyncAt: &weeklyNext}); err != nil {
				t.Error(err)
			}
		})
		dc = createDue(t, daily)
		run(t, s, dc)
		if got := stored(t, dc); got.NextSyncAt == nil || !got.NextSyncAt.Equal(weeklyNext) {
			t.Fatalf("next_sync_at = %v, want %v", got.NextSyncAt, weeklyNext)
		}
	})
}
