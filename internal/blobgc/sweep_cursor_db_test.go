package blobgc

import (
	"fmt"
	"testing"
	"time"
)

func TestSweeperResumesAfterBoundedRunDB(t *testing.T) {
	pool := testPool(t)
	store := newFakeStore()
	live := map[string]bool{}
	for n := range 1001 {
		prefix := testPrefix(600 + n)
		store.objects[prefix+"w300.webp"] = time.Now().Add(-48 * time.Hour)
		if n < 1000 {
			live[prefix] = true
		}
	}
	for run := range 2 {
		stats, err := NewSweeper(pool, store, testNamespace(live)).Sweep(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if run == 0 && (!stats.Namespaces[0].Truncated || stats.Namespaces[0].Queued != 0) {
			t.Fatalf("first page: %+v", stats)
		}
	}
	if _, ok := queueRows(t, pool)[testPrefix(1600)]; !ok {
		t.Fatal("orphan after the page limit was never queued")
	}
}

func (s *fakeStore) Identity() string { return "blobgc-test:" + fmt.Sprintf("%p", s) }

func TestSweeperDefersPrefixSpanningBoundedRunsDB(t *testing.T) {
	pool := testPool(t)
	store := newFakeStore()
	prefix := testPrefix(1700)
	for n := range 1001 {
		store.objects[fmt.Sprintf("%s%04d.webp", prefix, n)] = time.Now().Add(-48 * time.Hour)
	}
	store.objects[prefix+"1000.webp"] = time.Now()
	for run := range 2 {
		stats, err := NewSweeper(pool, store, testNamespace(nil)).Sweep(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Namespaces[0].Queued != 0 {
			t.Fatalf("queued a prefix before seeing its recent object: %+v", stats)
		}
		if run == 1 && stats.Namespaces[0].TooNew != 1 {
			t.Fatalf("lost the partial prefix across runs: %+v", stats)
		}
	}
}

func TestSweeperRestartsAfterStorageMoveDB(t *testing.T) {
	pool := testPool(t)
	first := newFakeStore()
	for n := range 1001 {
		first.objects[fmt.Sprintf("%s%04d.webp", testPrefix(1800), n)] = time.Now().Add(-48 * time.Hour)
	}
	if _, err := NewSweeper(pool, first, testNamespace(nil)).Sweep(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	moved := newFakeStore()
	moved.objects[testPrefix(1)+"w300.webp"] = time.Now().Add(-48 * time.Hour)
	stats, err := NewSweeper(pool, moved, testNamespace(nil)).Sweep(t.Context(), 1)
	if err != nil || stats.Namespaces[0].Queued != 1 || stats.Namespaces[0].Truncated {
		t.Fatalf("reused another store's cursor: %+v %v", stats, err)
	}
}
