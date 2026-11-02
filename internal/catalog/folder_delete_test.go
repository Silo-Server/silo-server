package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// withFastDeadlockRetry shrinks retry timing/attempts for tests and restores
// the originals on cleanup. Tests using it must not call t.Parallel().
func withFastDeadlockRetry(t *testing.T, maxAttempts int) {
	t.Helper()
	oldMax, oldBackoff := deadlockMaxAttempts, deadlockBaseBackoff
	deadlockMaxAttempts = maxAttempts
	deadlockBaseBackoff = time.Millisecond
	t.Cleanup(func() {
		deadlockMaxAttempts = oldMax
		deadlockBaseBackoff = oldBackoff
	})
}

func TestRetryOnDeadlockRetriesThenSucceeds(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	calls := 0
	err := retryOnDeadlock(context.Background(), func() error {
		calls++
		if calls < 3 {
			return &pgconn.PgError{Code: "40P01"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryOnDeadlockReturnsNonRetryableImmediately(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	sentinel := errors.New("boom")
	calls := 0
	err := retryOnDeadlock(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestRetryOnDeadlockGivesUpAfterMaxAttempts(t *testing.T) {
	withFastDeadlockRetry(t, 4)
	calls := 0
	err := retryOnDeadlock(context.Background(), func() error {
		calls++
		return &pgconn.PgError{Code: "40P01"}
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("expected 40P01 pg error, got %v", err)
	}
	if calls != 4 {
		t.Fatalf("expected 4 calls, got %d", calls)
	}
}

func TestRetryOnDeadlockStopsOnCanceledContext(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	// op always runs once at the top of the loop before the canceled ctx is
	// observed in the select, so exactly one call is expected.
	err := retryOnDeadlock(ctx, func() error {
		calls++
		return &pgconn.PgError{Code: "40P01"}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before cancel, got %d", calls)
	}
}

func TestDeleteInBatchesLoopsUntilUnderBatchSize(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	counts := []int64{5, 5, 2}
	idx := 0
	total, err := deleteInBatches(context.Background(), 5, func(context.Context) (int64, error) {
		n := counts[idx]
		idx++
		return n, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 12 {
		t.Fatalf("expected total 12, got %d", total)
	}
	if idx != 3 {
		t.Fatalf("expected 3 batches, got %d", idx)
	}
}

func TestDeleteInBatchesStopsImmediatelyWhenFirstBatchUnderSize(t *testing.T) {
	withFastDeadlockRetry(t, 5)
	calls := 0
	total, err := deleteInBatches(context.Background(), 5, func(context.Context) (int64, error) {
		calls++
		return 0, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || calls != 1 {
		t.Fatalf("expected total 0 and 1 call, got total=%d calls=%d", total, calls)
	}
}

func TestLateOrphanCleanupUsesProvisionalSafetyPredicate(t *testing.T) {
	conditions := normalizePredicateSQL(orphanedProvisionalMediaItemConditions)
	for name, query := range map[string]string{
		"collect": collectOrphanedProvisionalIDsByContentIDSQL,
		"delete":  deleteOrphanedProvisionalIDsByContentIDSQL,
	} {
		if !strings.Contains(normalizePredicateSQL(query), conditions) {
			t.Fatalf("%s late orphan query does not use provisional safety predicate", name)
		}
	}
	if !strings.Contains(normalizePredicateSQL(deleteOrphanedProvisionalIDsByContentIDSQL), "RETURNING mi.content_id") {
		t.Fatalf("late orphan delete query must return IDs for catalog search cleanup")
	}
}

// Deleting one library of a multi-library admin collection must keep the
// collection, whichever library the admin picked first. Only a collection with
// no other library goes with the library.
func TestFolderDeleteKeepsMultiLibraryCollectionsDB(t *testing.T) {
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
	folders := NewFolderRepository(pool)
	collections := NewLibraryCollectionRepository(pool)

	// Libraries are indexes into three seeded libraries; the collection covers
	// scope, in the order the admin picked them.
	for _, tc := range []struct {
		name        string
		scope       []int
		deleted     int
		slugTakenIn []int
		wantLibrary int
		wantSuffix  string
	}{
		{name: "delete the first library", scope: []int{0, 1}, deleted: 0, wantLibrary: 1},
		{name: "delete the second library", scope: []int{0, 1}, deleted: 1, wantLibrary: 0},
		{name: "slug taken in the only other library", scope: []int{0, 1}, deleted: 0, slugTakenIn: []int{1}, wantLibrary: 1, wantSuffix: "-2"},
		{name: "slug free in a later library", scope: []int{0, 1, 2}, deleted: 0, slugTakenIn: []int{1}, wantLibrary: 2},
		{name: "slug taken in every other library", scope: []int{0, 1, 2}, deleted: 0, slugTakenIn: []int{1, 2}, wantLibrary: 1, wantSuffix: "-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := fmt.Sprintf("folder-delete-collection-%d", time.Now().UnixNano())
			libraries, items := seedLibrariesWithOneItemEach(t, pool, prefix)
			scope := make([]int, len(tc.scope))
			for i, l := range tc.scope {
				scope[i] = libraries[l]
			}

			multi, err := collections.Create(ctx, CreateLibraryCollectionInput{LibraryIDs: scope, Slug: prefix, Title: "Across libraries", CollectionType: "manual"})
			if err != nil {
				t.Fatal(err)
			}
			single, err := collections.Create(ctx, CreateLibraryCollectionInput{LibraryIDs: []int{libraries[tc.deleted]}, Slug: prefix + "-single", Title: "Deleted library only", CollectionType: "manual"})
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range tc.slugTakenIn {
				if _, err := collections.Create(ctx, CreateLibraryCollectionInput{LibraryIDs: []int{libraries[l]}, Slug: prefix, Title: "Same slug", CollectionType: "manual"}); err != nil {
					t.Fatal(err)
				}
			}
			var wantMembers []string
			for i, l := range tc.scope {
				if err := collections.AddItem(ctx, multi.ID, items[l], i); err != nil {
					t.Fatal(err)
				}
				if l != tc.deleted {
					wantMembers = append(wantMembers, items[l])
				}
			}
			if _, err := pool.Exec(ctx, `INSERT INTO library_collection_poster_variants(collection_id,variant_key,poster_path) VALUES($1,'v','p')`, multi.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO library_collection_sync_runs(id,collection_id,status) VALUES($1,$2,'success')`, prefix+"-run", multi.ID); err != nil {
				t.Fatal(err)
			}

			if err := folders.Delete(ctx, libraries[tc.deleted]); err != nil {
				t.Fatal(err)
			}

			got, err := collections.GetByID(ctx, multi.ID)
			if err != nil {
				t.Fatalf("multi-library collection after deleting its library %d: %v", tc.deleted, err)
			}
			if got.LibraryID != libraries[tc.wantLibrary] || got.Slug != prefix+tc.wantSuffix {
				t.Fatalf("library_id=%d slug=%q, want %d %q", got.LibraryID, got.Slug, libraries[tc.wantLibrary], prefix+tc.wantSuffix)
			}
			var members []string
			if err := pool.QueryRow(ctx, `SELECT COALESCE(array_agg(media_item_id ORDER BY media_item_id), '{}') FROM library_collection_items WHERE collection_id=$1`, multi.ID).Scan(&members); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(members, wantMembers) || len(got.LibraryIDs) != len(tc.scope)-1 {
				t.Fatalf("members=%v library_ids=%v, want members %v", members, got.LibraryIDs, wantMembers)
			}
			var variants, runs int
			if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM library_collection_poster_variants WHERE collection_id=$1), (SELECT COUNT(*) FROM library_collection_sync_runs WHERE collection_id=$1)`, multi.ID).Scan(&variants, &runs); err != nil {
				t.Fatal(err)
			}
			if variants != 1 || runs != 1 {
				t.Fatalf("poster variants=%d sync runs=%d, want 1 and 1", variants, runs)
			}
			// An editor save resubmits the scope in ID order.
			renamed := "Renamed"
			if err := collections.Update(ctx, UpdateLibraryCollectionInput{ID: multi.ID, Title: &renamed, LibraryIDs: &got.LibraryIDs}); err != nil {
				t.Fatalf("saving the collection after the delete: %v", err)
			}
			if _, err := collections.GetByID(ctx, single.ID); !errors.Is(err, ErrLibraryCollectionNotFound) {
				t.Fatalf("single-library collection after deleting its library: %v", err)
			}
		})
	}
}

// seedLibrariesWithOneItemEach creates three movie libraries named after prefix,
// each holding one item, and removes them and any collection using the prefix
// as its slug when the test ends.
func seedLibrariesWithOneItemEach(t *testing.T, pool *pgxpool.Pool, prefix string) ([]int, []string) {
	t.Helper()
	ctx := t.Context()
	libraries := make([]int, 3)
	items := make([]string, 3)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collections WHERE slug LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, libraries)
	})
	for i := range libraries {
		items[i] = fmt.Sprintf("%s-item-%d", prefix, i)
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("%s-%d", prefix, i)).Scan(&libraries[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,status,genres) VALUES($1,'movie',$1,'released','{}')`, items[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, items[i], libraries[i]); err != nil {
			t.Fatal(err)
		}
	}
	return libraries, items
}
