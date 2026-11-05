package catalog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type libraryCASFixture struct {
	pool         *pgxpool.Pool
	repo         *LibraryCollectionRepository
	libraryID    int
	collectionID string
}

func newLibraryCASFixture(t *testing.T) libraryCASFixture {
	t.Helper()
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("library-cas-%d", time.Now().UnixNano())).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, libraryID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_order_revisions WHERE library_id=$1`, libraryID)
	})
	repo := NewLibraryCollectionRepository(pool)
	c, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: libraryID, Title: "first", Slug: fmt.Sprintf("cas-%d", libraryID)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = repo.Delete(context.Background(), c.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, c.ID)
	})
	return libraryCASFixture{pool, repo, libraryID, c.ID}
}

func (f libraryCASFixture) revision(t *testing.T) int64 {
	t.Helper()
	rev, err := f.repo.CollectionRevision(t.Context(), f.collectionID)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

func seedNativeCollectionItem(t *testing.T, f libraryCASFixture, suffix string) string {
	t.Helper()
	id := fmt.Sprintf("native-member-%d-%s", f.libraryID, suffix)
	seedSortableItem(t, f.pool, id, id, 2020)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, f.libraryID); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLibraryCollectionAtomicAppendDB(t *testing.T) {
	f := newLibraryCASFixture(t)
	other := newLibraryCASFixture(t)
	t.Logf("append cleanup libraries=%d,%d collections=%s,%s", f.libraryID, other.libraryID, f.collectionID, other.collectionID)
	ctx := t.Context()
	first := seedNativeCollectionItem(t, f, "empty")
	if err := f.repo.AppendItemIfAbsent(ctx, f.collectionID, first); err != nil {
		t.Fatal(err)
	}
	assertPosition := func(id string, want int) {
		t.Helper()
		var got int
		if err := f.pool.QueryRow(ctx, `SELECT position FROM library_collection_items WHERE collection_id=$1 AND media_item_id=$2`, f.collectionID, id).Scan(&got); err != nil || got != want {
			t.Fatalf("position %s: %d want %d (%v)", id, got, want, err)
		}
	}
	assertPosition(first, 0)
	var tail string
	for i := range 205 {
		tail = seedNativeCollectionItem(t, f, fmt.Sprintf("seed-%03d", i))
		if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, tail, i*3); err != nil {
			t.Fatal(err)
		}
	}
	assertLibraryConcurrentAppend(t, f)
	revision := f.revision(t)
	if err := f.repo.AppendItemIfAbsent(ctx, f.collectionID, tail); err != nil {
		t.Fatal(err)
	}
	assertPosition(tail, 612)
	if got := f.revision(t); got != revision {
		t.Fatal("duplicate append changed revision")
	}
	explicit := seedNativeCollectionItem(t, f, "explicit")
	if err := f.repo.AddItemIfAbsent(ctx, f.collectionID, explicit, 17); err != nil {
		t.Fatal(err)
	}
	assertPosition(explicit, 17)
	// Legacy AddItem deliberately keeps its existing reposition-on-upsert behavior.
	if err := f.repo.AddItem(ctx, f.collectionID, explicit, 71); err != nil {
		t.Fatal(err)
	}
	assertPosition(explicit, 71)
	outside := seedNativeCollectionItem(t, other, "outside")
	if err := f.repo.AppendItemIfAbsent(ctx, f.collectionID, outside); !errors.Is(err, ErrLibraryCollectionItemNotFound) {
		t.Fatalf("outside-library append: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE library_collections SET collection_type='smart' WHERE id=$1`, f.collectionID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.AppendItemIfAbsent(ctx, f.collectionID, first); !errors.Is(err, ErrLibraryCollectionNotManual) {
		t.Fatalf("smart append: %v", err)
	}
	if err := f.repo.AppendItemIfAbsent(ctx, "missing-append-collection", first); !errors.Is(err, ErrLibraryCollectionNotFound) {
		t.Fatalf("missing collection append: %v", err)
	}
}

func assertLibraryConcurrentAppend(t *testing.T, f libraryCASFixture) {
	t.Helper()
	ctx := t.Context()
	const concurrent = 16
	ids := make([]string, concurrent)
	for i := range ids {
		ids[i] = seedNativeCollectionItem(t, f, fmt.Sprintf("append-%02d", i))
	}
	start := make(chan struct{})
	results := make(chan error, concurrent)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() { <-start; results <- f.repo.AppendItemIfAbsent(ctx, f.collectionID, id) })
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count, distinct, minPosition, maxPosition int
	if err := f.pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT position),min(position),max(position) FROM library_collection_items WHERE collection_id=$1 AND media_item_id=ANY($2)`, f.collectionID, ids).Scan(&count, &distinct, &minPosition, &maxPosition); err != nil {
		t.Fatal(err)
	}
	if count != concurrent || distinct != concurrent || minPosition != 613 || maxPosition != 612+concurrent {
		t.Fatalf("concurrent gapped append: %d/%d positions %d..%d", count, distinct, minPosition, maxPosition)
	}
}
