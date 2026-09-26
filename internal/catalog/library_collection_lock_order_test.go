package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A trigger pauses the legacy update after it owns the target tuple. The
// guarded write must wait there without holding a revision counter needed by
// the legacy writer. Releasing the barrier proves progress, exact stale refusal,
// and whole-transaction wildcard retry without replacing the expected witness.
func TestLibraryCollectionMixedWriterLockOrderDB(t *testing.T) {
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
			f := newLibraryCASFixture(t)
			id, table := f.collectionID, "library_collections"
			revision := f.revision(t, false)
			if group {
				id, table = f.groupID, "library_collection_groups"
				if err := f.repo.MoveAndReorder(ctx, MoveAndReorderInput{LibraryID: f.libraryID, TargetGroupID: &f.groupID, OrderedIDs: []string{f.collectionID}}); err != nil {
					t.Fatal(err)
				}
				revision = f.revision(t, true)
			}
			gate, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback(context.Background()) }()
			key := int64(830000000000) + int64(f.libraryID)
			if _, err := gate.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				t.Fatal(err)
			}
			trigger := fmt.Sprintf("test_collection_lock_%d", f.libraryID)
			sql := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(%d::bigint); RETURN NEW; END; $$;
 CREATE TRIGGER %s BEFORE UPDATE ON %s FOR EACH ROW WHEN (NEW.id='%s') EXECUTE FUNCTION %s();`, trigger, key, trigger, table, id, trigger)
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
			run := func(p *pgxpool.Pool, expected *int64) error {
				if group {
					repo := NewLibraryCollectionGroupRepository(p)
					if expected != nil && operation == "group-delete" {
						return repo.DeleteIfRevision(ctx, id, *expected)
					}
					_, err := repo.Update(ctx, id, UpdateLibraryCollectionGroupInput{Name: new("changed"), ExpectedRevision: expected})
					return err
				}
				return NewLibraryCollectionRepository(p).Update(ctx, UpdateLibraryCollectionInput{ID: id, Title: new("changed"), ExpectedRevision: expected})
			}
			legacyDone := make(chan error, 1)
			guardDone := make(chan error, 1)
			go func() { legacyDone <- run(legacyPool, nil) }()
			waitLibraryCollectionBlocked(t, ctx, pool, legacyName, "advisory")
			guardExpected := revision
			if operation == "collection-wildcard" {
				guardExpected = -1
			}
			go func() { guardDone <- run(guardPool, &guardExpected) }()
			waitLibraryCollectionBlocked(t, ctx, pool, guardName, "transactionid")
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
			} else if !errors.Is(guardErr, ErrLibraryCollectionRevisionMismatch) {
				t.Fatalf("guarded result: %v", guardErr)
			}
		})
	}
}
func waitLibraryCollectionBlocked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, event string) {
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

// These barriers reproduce target-row inversions independently of counter
// reservation: item reorder takes revision then parent; move takes membership
// before a definition update takes that same membership after its revision.
func TestLibraryCollectionMixedTargetLockOrderDB(t *testing.T) {
	for _, operation := range []string{"items-definition", "move-definition"} {
		t.Run(operation, func(t *testing.T) {
			f := newLibraryCASFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			dsn := os.Getenv("SILO_TEST_DATABASE_URL")
			trigger := fmt.Sprintf("test_library_target_%d", f.libraryID)
			legacyName, guardName := trigger+"_legacy", trigger+"_guard"
			named := func(name string) *pgxpool.Pool {
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
				t.Cleanup(p.Close)
				return p
			}
			legacyRepo, guardRepo := NewLibraryCollectionRepository(named(legacyName)), NewLibraryCollectionRepository(named(guardName))
			itemID := fmt.Sprintf("target-item-%d", f.libraryID)
			seedSortableItem(t, f.pool, itemID, itemID, 2020)
			if _, err := f.pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, itemID, f.libraryID); err != nil {
				t.Fatal(err)
			}
			if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, itemID, 0); err != nil {
				t.Fatal(err)
			}
			revision := f.revision(t, false)
			gate, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback(context.Background()) }()
			key := int64(850000000000) + int64(f.libraryID)
			if _, err := gate.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				t.Fatal(err)
			}
			table, timing := "library_collection_revisions", "AFTER"
			if operation == "move-definition" {
				table, timing = "library_collection_libraries", "BEFORE"
			}
			sql := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF current_setting('application_name')='%s' THEN PERFORM pg_advisory_xact_lock(%d::bigint); END IF; RETURN NEW; END; $$; CREATE TRIGGER %s %s UPDATE ON %s FOR EACH ROW WHEN (NEW.collection_id='%s') EXECUTE FUNCTION %s();`, trigger, legacyName, key, trigger, timing, table, f.collectionID, trigger)
			if _, err := f.pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = f.pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER %s ON %s; DROP FUNCTION %s();`, trigger, table, trigger))
			}()
			legacyDone, guardDone := make(chan error, 1), make(chan error, 1)
			go func() {
				if operation == "items-definition" {
					legacyDone <- legacyRepo.ReorderItems(ctx, f.collectionID, []string{itemID})
				} else {
					legacyDone <- legacyRepo.MoveAndReorder(ctx, MoveAndReorderInput{LibraryID: f.libraryID, TargetGroupID: &f.groupID, OrderedIDs: []string{f.collectionID}})
				}
			}()
			waitLibraryCollectionBlocked(t, ctx, f.pool, legacyName, "advisory")
			go func() {
				in := UpdateLibraryCollectionInput{ID: f.collectionID, Title: new("guarded"), ExpectedRevision: &revision}
				if operation == "move-definition" {
					in.SetGroupID = new(new(f.groupID))
				}
				guardDone <- guardRepo.Update(ctx, in)
			}()
			waitLibraryCollectionBlocked(t, ctx, f.pool, guardName, "transactionid")
			if err := gate.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-legacyDone; err != nil {
				t.Fatalf("legacy target mutation: %v", err)
			}
			if err := <-guardDone; !errors.Is(err, ErrLibraryCollectionRevisionMismatch) {
				t.Fatalf("guarded target mutation: %v", err)
			}
		})
	}
}
