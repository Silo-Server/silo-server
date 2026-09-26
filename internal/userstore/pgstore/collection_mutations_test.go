package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCollectionCASConcurrentPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, operation := range []string{"update", "delete", "items", "collections", "group_update", "group_delete", "groups"} {
		t.Run(operation, func(t *testing.T) {
			ctx := t.Context()
			var uid int
			if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("collection-cas-%d", time.Now().UnixNano())).Scan(&uid); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, uid)
				_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_revisions WHERE user_id=$1`, uid)
				_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_order_revisions WHERE user_id=$1`, uid)
			}()
			s := newStore(pool, uid)
			c, err := s.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "first"})
			if err != nil {
				t.Fatal(err)
			}
			for i, id := range []string{"a", "b"} {
				if err := s.AddCollectionItem(ctx, c.ID, id, i); err != nil {
					t.Fatal(err)
				}
			}
			g, err := s.CreateCollectionGroup(ctx, "group", "group", "manual")
			if err != nil {
				t.Fatal(err)
			}
			revision, err := s.CollectionRevision(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "collections" || operation == "group_update" || operation == "group_delete" || operation == "groups" {
				revision, err = s.CollectionOrderRevision(ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			attempt := func() error {
				store := newStore(pool, uid)
				switch operation {
				case "update":
					return store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", Name: new("updated"), ExpectedRevision: &revision})
				case "delete":
					return store.DeleteCollectionIfRevision(ctx, c.ID, revision)
				case "items":
					return store.ReorderCollectionItemsIfRevision(ctx, c.ID, []string{"b", "a"}, revision)
				case "collections":
					return store.ReorderCollectionsIfRevision(ctx, "owner", nil, []string{c.ID}, revision)
				case "group_update":
					_, err := store.UpdateCollectionGroupIfRevision(ctx, g.ID, new("updated"), nil, nil, revision)
					return err
				case "group_delete":
					return store.DeleteCollectionGroupIfRevision(ctx, g.ID, revision)
				default:
					return store.ReorderCollectionGroupsIfRevision(ctx, []string{g.ID}, revision)
				}
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() { <-start; results <- attempt() })
			}
			close(start)
			wg.Wait()
			close(results)
			winners, stale := 0, 0
			for err := range results {
				if err == nil {
					winners++
				} else if errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
					stale++
				} else {
					t.Fatalf("unexpected concurrent failure: %v", err)
				}
			}
			if winners != 1 || stale != 1 {
				t.Fatalf("winners=%d stale=%d", winners, stale)
			}
			if operation == "update" {
				if err := s.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", Name: new("wildcard"), ExpectedRevision: new(int64(-1))}); err != nil {
					t.Fatalf("wildcard: %v", err)
				}
			}
		})
	}
}

func TestCollectionCASOrderScopePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var uid int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("collection-cas-scope-%d", time.Now().UnixNano())).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, uid)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_revisions WHERE user_id=$1`, uid)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_order_revisions WHERE user_id=$1`, uid)
	}()
	store := newStore(pool, uid)
	visible, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "visible"})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "other", Name: "hidden"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateCollectionGroup(ctx, "group", "group", "manual")
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.CollectionOrderRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Even a current account version does not authorize moving an invisible
	// collection or moving rows between the requested group and another group.
	for _, groupID := range []*string{nil, &group.ID} {
		err := store.ReorderCollectionsIfRevision(ctx, "owner", groupID, []string{hidden.ID, visible.ID}, version)
		if err == nil {
			t.Fatal("reordered outside profile/group scope")
		}
		after, err := store.CollectionOrderRevision(ctx)
		if err != nil || after != version {
			t.Fatalf("failed reorder changed aggregate: %d -> %d (%v)", version, after, err)
		}
	}
	got, err := store.GetCollection(ctx, hidden.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GroupID != nil || got.SortOrder != hidden.SortOrder {
		t.Fatalf("hidden collection mutated: %+v", got)
	}
	if err := store.ReorderCollectionsIfRevision(ctx, "owner", nil, []string{visible.ID}, version); err != nil {
		t.Fatalf("correct visible permutation rejected: %v", err)
	}
}

func TestCollectionMutationRetryClassification(t *testing.T) {
	s := &PostgresUserStore{}
	deadlock := &pgconn.PgError{Code: "40P01", Message: "deadlock"}
	calls := 0
	err := s.runCollectionMutation(t.Context(), "", -1, func() error { calls++; return deadlock })
	if !errors.Is(err, deadlock) || calls != 1 || errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
		t.Fatalf("deadlock reclassified/retried: %v calls=%d", err, calls)
	}
	serial := &pgconn.PgError{Code: "40001", Message: "serialization failure"}
	calls = 0
	err = s.runCollectionMutation(t.Context(), "", -1, func() error { calls++; return serial })
	if !errors.Is(err, serial) || calls != 3 || errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
		t.Fatalf("wildcard serialization reclassified/unbounded: %v calls=%d", err, calls)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	calls = 0
	err = s.runCollectionMutation(canceled, "", -1, func() error { calls++; return nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancellation: %v calls=%d", err, calls)
	}
}

func TestCollectionUnchangedSerializationAndNoopPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var uid int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("collection-cas-noop-%d", time.Now().UnixNano())).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, uid)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_revisions WHERE user_id=$1`, uid)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_order_revisions WHERE user_id=$1`, uid)
	}()
	s := newStore(pool, uid)
	c, err := s.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "noop"})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := s.CollectionRevision(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	serial := &pgconn.PgError{Code: "40001", Message: "unrelated serialization"}
	calls := 0
	err = s.runCollectionMutation(ctx, c.ID, revision, func() error { calls++; return serial })
	if !errors.Is(err, serial) || calls != 3 || errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
		t.Fatalf("unchanged exact witness misreported: %v calls=%d", err, calls)
	}
	if err := s.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", ExpectedRevision: &revision}); err != nil {
		t.Fatalf("guarded noop: %v", err)
	}
	after, err := s.CollectionRevision(ctx, c.ID)
	if err != nil || after <= revision {
		t.Fatalf("noop did not consume witness: %d -> %d (%v)", revision, after, err)
	}
	if err := s.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", ExpectedRevision: &revision}); !errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
		t.Fatalf("noop witness reused: %v", err)
	}
}
