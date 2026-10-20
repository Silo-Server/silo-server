package catalog

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

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
	revision := f.revision(t, false)
	if err := f.repo.AppendItemIfAbsent(ctx, f.collectionID, tail); err != nil {
		t.Fatal(err)
	}
	assertPosition(tail, 612)
	if got := f.revision(t, false); got != revision {
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
