package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func referenceWriterInsert(ctx context.Context, tx pgx.Tx, sectionID, collectionID string, libraryID int) error {
	_, err := tx.Exec(ctx, `INSERT INTO page_sections(id,scope,library_id,position,section_type,title,config) VALUES($1,'library',$2,0,'collection','Reference',jsonb_build_object('library_collection_id',$3::text))`, sectionID, libraryID, collectionID)
	return err
}
func namedReferencePool(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = name
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The delete's serializable snapshot starts before the referencing section
// commits. Parent lock ownership alone is insufficient: the parent write must
// make that old snapshot conflict, rather than deleting past an unseen section.
func TestLibraryCollectionReferenceSnapshotFenceDB(t *testing.T) {
	for _, operation := range []string{"exact", "wildcard", "legacy"} {
		t.Run(operation, func(t *testing.T) {
			f := newLibraryCASFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			revision := f.revision(t, false)
			before, err := f.repo.GetByID(ctx, f.collectionID)
			if err != nil {
				t.Fatal(err)
			}
			writer, err := f.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writer.Rollback(context.Background()) }()
			if err := LockLibraryCollectionReferences(ctx, writer, nil, []string{f.collectionID}); err != nil {
				t.Fatal(err)
			}
			sectionID := fmt.Sprintf("reference-snapshot-%d", f.libraryID)
			t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id=$1`, sectionID) })
			if err := referenceWriterInsert(ctx, writer, sectionID, f.collectionID, f.libraryID); err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("reference-delete-%d", f.libraryID)
			repo := NewLibraryCollectionRepository(namedReferencePool(t, ctx, name))
			done := make(chan error, 1)
			var workers sync.WaitGroup
			defer func() { cancel(); workers.Wait() }()
			workers.Go(func() {
				switch operation {
				case "legacy":
					done <- repo.Delete(ctx, f.collectionID)
				case "wildcard":
					done <- repo.DeleteIfRevision(ctx, f.collectionID, -1)
				default:
					done <- repo.DeleteIfRevision(ctx, f.collectionID, revision)
				}
			})
			waitLibraryCollectionBlocked(t, ctx, f.pool, name, "transactionid")
			if err := writer.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			want := ErrLibraryCollectionInUse
			if operation == "exact" {
				want = ErrLibraryCollectionRevisionMismatch
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("delete after concurrent reference: got%v want%v", err, want)
			}
			after, err := f.repo.GetByID(ctx, f.collectionID)
			if err != nil {
				t.Fatal(err)
			}
			if !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatal("reference fence changed display timestamp")
			}
			if got := f.revision(t, false); got <= revision {
				t.Fatal("reference fence did not advance durable witness")
			}
		})
	}
}

func TestLibraryCollectionReferenceMissingAndRollbackDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	ctx := t.Context()
	before := f.revision(t, false)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := LockLibraryCollectionReferences(ctx, tx, []string{"historical-missing-ref", f.collectionID}, nil); err != nil {
		t.Fatalf("repair missing outgoing reference: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if after := f.revision(t, false); after != before {
		t.Fatal("rolled-back reference fence changed revision")
	}
	tx, err = f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := LockLibraryCollectionReferences(ctx, tx, nil, []string{f.collectionID, "missing-required-reference"}); !errors.Is(err, ErrLibraryCollectionNotFound) {
		t.Fatalf("missing incoming reference: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if after := f.revision(t, false); after != before {
		t.Fatal("invalid incoming reference changed revision")
	}
}

func TestLibraryCollectionSectionManagedCleanupRaceDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := f.repo.Update(ctx, UpdateLibraryCollectionInput{ID: f.collectionID, ManagementMode: new("section")}); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `UPDATE library_collections SET management_mode='manual' WHERE id=$1`, f.collectionID); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("managed-cleanup-%d", f.libraryID)
	repo := NewLibraryCollectionRepository(namedReferencePool(t, ctx, name))
	type result struct {
		deleted bool
		err     error
	}
	done := make(chan result, 1)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	workers.Go(func() {
		deleted, err := repo.DeleteSectionManagedIfUnreferenced(ctx, f.collectionID)
		done <- result{deleted, err}
	})
	waitLibraryCollectionBlocked(t, ctx, f.pool, name, "transactionid")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.deleted {
		t.Fatalf("cleanup after mode change: %+v", got)
	}
	if _, err := f.repo.GetByID(ctx, f.collectionID); err != nil {
		t.Fatal("cleanup deleted newly manual collection")
	}
	if err := f.repo.Update(ctx, UpdateLibraryCollectionInput{ID: f.collectionID, ManagementMode: new("section")}); err != nil {
		t.Fatal(err)
	}

	sectionID := fmt.Sprintf("managed-reference-%d", f.libraryID)
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id=$1`, sectionID) })
	referenceTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = referenceTx.Rollback(context.Background()) }()
	if err := LockLibraryCollectionReferences(ctx, referenceTx, nil, []string{f.collectionID}); err != nil {
		t.Fatal(err)
	}
	if err := referenceWriterInsert(ctx, referenceTx, sectionID, f.collectionID, f.libraryID); err != nil {
		t.Fatal(err)
	}
	if err := referenceTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if deleted, err := repo.DeleteSectionManagedIfUnreferenced(ctx, f.collectionID); deleted || !errors.Is(err, ErrLibraryCollectionInUse) {
		t.Fatalf("referenced managed cleanup: deleted=%v err=%v", deleted, err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM page_sections WHERE id=$1`, sectionID); err != nil {
		t.Fatal(err)
	}
	if deleted, err := repo.DeleteSectionManagedIfUnreferenced(ctx, f.collectionID); err != nil || !deleted {
		t.Fatalf("unused managed cleanup: deleted=%v err=%v", deleted, err)
	}
}

// The first parent is deliberately contended. An opposing reference swap must
// wait there without retaining the second parent, even when given reversed IDs.
func TestLibraryCollectionReferenceSortedParentsDB(t *testing.T) {
	first, second := newLibraryCASFixture(t), newLibraryCASFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	writer, err := first.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if err := LockLibraryCollectionReferences(ctx, writer, []string{first.collectionID}, []string{second.collectionID}); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("opposing-reference-%d", first.libraryID)
	pool := namedReferencePool(t, ctx, name)
	done := make(chan error, 1)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	workers.Go(func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if err := LockLibraryCollectionReferences(ctx, tx, []string{second.collectionID}, []string{first.collectionID}); err != nil {
			done <- err
			return
		}
		done <- tx.Commit(ctx)
	})
	waitLibraryCollectionBlocked(t, ctx, first.pool, name, "transactionid")
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("opposing reference update: %v", err)
	}
}

func TestLibraryCollectionDeleteBeforeReferenceDB(t *testing.T) {
	for _, operation := range []string{"legacy", "guarded"} {
		t.Run(operation, func(t *testing.T) {
			f := newLibraryCASFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			revision := f.revision(t, false)
			trigger := fmt.Sprintf("test_reference_delete_%d", f.libraryID)
			deleteName, writerName := trigger+"_delete", trigger+"_writer"
			gate, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback(context.Background()) }()
			key := int64(890000000000) + int64(f.libraryID)
			if _, err := gate.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				t.Fatal(err)
			}
			sql := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF current_setting('application_name')='%s' THEN PERFORM pg_advisory_xact_lock(%d::bigint); END IF; RETURN OLD; END; $$; CREATE TRIGGER %s BEFORE DELETE ON library_collections FOR EACH ROW WHEN (OLD.id='%s') EXECUTE FUNCTION %s();`, trigger, deleteName, key, trigger, f.collectionID, trigger)
			if _, err := f.pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = f.pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER %s ON library_collections; DROP FUNCTION %s();`, trigger, trigger))
			}()
			repo := NewLibraryCollectionRepository(namedReferencePool(t, ctx, deleteName))
			writerPool := namedReferencePool(t, ctx, writerName)
			deleted, written := make(chan error, 1), make(chan error, 1)
			var workers sync.WaitGroup
			defer func() { cancel(); workers.Wait() }()
			workers.Go(func() {
				if operation == "legacy" {
					deleted <- repo.Delete(ctx, f.collectionID)
				} else {
					deleted <- repo.DeleteIfRevision(ctx, f.collectionID, revision)
				}
			})
			waitLibraryCollectionBlocked(t, ctx, f.pool, deleteName, "advisory")
			workers.Go(func() {
				tx, err := writerPool.Begin(ctx)
				if err != nil {
					written <- err
					return
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				if err := LockLibraryCollectionReferences(ctx, tx, nil, []string{f.collectionID}); err != nil {
					written <- err
					return
				}
				if err := referenceWriterInsert(ctx, tx, fmt.Sprintf("late-reference-%d", f.libraryID), f.collectionID, f.libraryID); err != nil {
					written <- err
					return
				}
				written <- tx.Commit(ctx)
			})
			waitLibraryCollectionBlocked(t, ctx, f.pool, writerName, "transactionid")
			if err := gate.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-deleted; err != nil {
				t.Fatalf("earlier collection delete: %v", err)
			}
			if err := <-written; !errors.Is(err, ErrLibraryCollectionNotFound) {
				t.Fatalf("late reference accepted deleted parent: %v", err)
			}
			var references int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM page_sections WHERE config->>'library_collection_id'=$1`, f.collectionID).Scan(&references); err != nil || references != 0 {
				t.Fatalf("dangling references=%d err=%v", references, err)
			}
		})
	}
}
