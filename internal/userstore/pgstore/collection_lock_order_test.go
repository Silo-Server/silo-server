package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A database trigger pauses the legacy update after it owns the target tuple.
// The guarded update then waits behind it. Before all writers locked account
// first, releasing the trigger formed account->tuple->account and PostgreSQL
// aborted one request with 40P01. A valid mixed sequence completes the legacy
// write and refuses the guarded write as stale, without a deadlock retry.
func TestCollectionMixedWriterLockOrderPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	for _, operation := range []string{"collection-update", "collection-wildcard", "group-update", "group-delete"} {
		group := strings.HasPrefix(operation, "group-")
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			var uid int
			if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("collection-lock-%d", time.Now().UnixNano())).Scan(&uid); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, uid)
				_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_revisions WHERE user_id=$1`, uid)
				_, _ = pool.Exec(context.Background(), `DELETE FROM user_collection_order_revisions WHERE user_id=$1`, uid)
			}()
			store := newStore(pool, uid)
			c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "first"})
			if err != nil {
				t.Fatal(err)
			}
			id, table := c.ID, "user_personal_collections"
			revision, err := store.CollectionRevision(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if group {
				g, err := store.CreateCollectionGroup(ctx, "group", "group", "manual")
				if err != nil {
					t.Fatal(err)
				}
				id = g.ID
				if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", GroupID: new(new(g.ID))}); err != nil {
					t.Fatal(err)
				}
				table = "user_collection_groups"
				revision, err = store.CollectionOrderRevision(ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			gate, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback(context.Background()) }()
			key := int64(830000000000) + int64(uid)
			if _, err := gate.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				t.Fatal(err)
			}
			trigger := fmt.Sprintf("test_collection_lock_%d", uid)
			sql := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(%d::bigint); RETURN NEW; END; $$;
 CREATE TRIGGER %s BEFORE UPDATE ON %s FOR EACH ROW WHEN (NEW.user_id=%d) EXECUTE FUNCTION %s();`, trigger, key, trigger, table, uid, trigger)
			if _, err := pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = pool.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON %s; DROP FUNCTION %s();", trigger, table, trigger))
			}()
			newNamed := func(name string) *pgxpool.Pool {
				cfg, err := pgxpool.ParseConfig(dsn)
				if err != nil {
					t.Fatal(err)
				}
				cfg.ConnConfig.RuntimeParams["application_name"] = name
				cfg.MaxConns = 1
				p, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			legacyName, guardName := trigger+"_legacy", trigger+"_guarded"
			legacyPool, guardPool := newNamed(legacyName), newNamed(guardName)
			defer legacyPool.Close()
			defer guardPool.Close()
			run := func(s *PostgresUserStore, expected *int64) error {
				if group {
					if expected != nil && operation == "group-delete" {
						return s.DeleteCollectionGroupIfRevision(ctx, id, *expected)
					}
					if expected == nil {
						_, e := s.UpdateCollectionGroup(ctx, id, new("legacy"), nil, nil)
						return e
					}
					_, e := s.UpdateCollectionGroupIfRevision(ctx, id, new("guarded"), nil, nil, *expected)
					return e
				}
				return s.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: id, RequestProfileID: "owner", Name: new("changed"), ExpectedRevision: expected})
			}
			legacyDone := make(chan error, 1)
			guardDone := make(chan error, 1)
			go func() { legacyDone <- run(newStore(legacyPool, uid), nil) }()
			waitCollectionBlocked(t, ctx, pool, legacyName, "advisory")
			guardExpected := revision
			if operation == "collection-wildcard" {
				guardExpected = -1
			}
			go func() { guardDone <- run(newStore(guardPool, uid), &guardExpected) }()
			waitCollectionBlocked(t, ctx, pool, guardName, "transactionid")
			if err := gate.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-legacyDone; err != nil {
				t.Fatalf("legacy write failed: %v", err)
			}
			guardErr := <-guardDone
			if operation == "collection-wildcard" {
				if guardErr != nil {
					t.Fatalf("wildcard overwrite: %v", guardErr)
				}
			} else if !errors.Is(guardErr, userstore.ErrCollectionRevisionMismatch) {
				t.Fatalf("guarded result: %v", guardErr)
			}
		})
	}
}
func waitCollectionBlocked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, event string) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event=$2)`, name, event).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("writer %s did not wait on %s: %v", name, event, ctx.Err())
		case <-ticker.C:
		}
	}
}
