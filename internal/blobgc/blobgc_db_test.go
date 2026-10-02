package blobgc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
)

// These tests share blob_gc_queue with the rest of the test database, so
// they use file ids far above any fixture's and remove their rows after.
const testIDBase = 1_900_000_000

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	clear := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM public.blob_gc_queue WHERE prefix LIKE 'chapter-images/19%'`)
	}
	clear()
	t.Cleanup(clear)
	return pool
}

func testPrefix(n int) string {
	return fmt.Sprintf("chapter-images/%d/", testIDBase+n)
}

// testNamespace owns chapter-images/<id>/ prefixes and reports those in live
// as referenced.
func testNamespace(live map[string]bool) Namespace {
	var mu sync.Mutex
	return Namespace{
		Root: "chapter-images/",
		Group: func(key string) (string, bool) {
			rest, ok := strings.CutPrefix(key, "chapter-images/")
			if !ok {
				return "", false
			}
			id, _, ok := strings.Cut(rest, "/")
			if !ok || id == "" {
				return "", false
			}
			return "chapter-images/" + id + "/", true
		},
		Live: func(_ context.Context, _ Querier, prefixes []string) (map[string]bool, error) {
			mu.Lock()
			defer mu.Unlock()
			result := map[string]bool{}
			for _, prefix := range prefixes {
				if live[prefix] {
					result[prefix] = true
				}
			}
			return result, nil
		},
	}
}

// fakeStore holds object keys with modification times and counts deletes.
type fakeStore struct {
	mu        sync.Mutex
	objects   map[string]time.Time
	deletes   map[string]int
	deleteErr map[string]error
	// sticky keys survive DeletePrefix, as keys a batch delete failed on do.
	sticky map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string]time.Time{}, deletes: map[string]int{}, deleteErr: map[string]error{}, sticky: map[string]bool{}}
}

func (s *fakeStore) Delete(_ context.Context, keys []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for _, key := range keys {
		s.deletes[key]++
		if err := s.deleteErr[key]; err != nil {
			return removed, err
		}
		if !s.sticky[key] {
			delete(s.objects, key)
			removed++
		}
	}
	return removed, nil
}

func (s *fakeStore) Stat(_ context.Context, key string) (blobstore.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	modified, ok := s.objects[key]
	if !ok {
		return blobstore.ObjectInfo{}, blobstore.ErrNotFound
	}
	return blobstore.ObjectInfo{Key: key, ModTime: modified}, nil
}

func (s *fakeStore) DeletePrefix(_ context.Context, prefix string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes[prefix]++
	if err := s.deleteErr[prefix]; err != nil {
		return 0, err
	}
	removed := 0
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) && !s.sticky[key] {
			delete(s.objects, key)
			removed++
		}
	}
	return removed, nil
}

func (s *fakeStore) List(_ context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) && key > cursor {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	next := ""
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
		next = keys[len(keys)-1]
	}
	infos := make([]blobstore.ObjectInfo, len(keys))
	for i, key := range keys {
		infos[i] = blobstore.ObjectInfo{Key: key, ModTime: s.objects[key]}
	}
	return infos, next, nil
}

func queue(t *testing.T, pool *pgxpool.Pool, prefix string, due time.Duration) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO public.blob_gc_queue (prefix, not_before) VALUES ($1, now() + make_interval(secs => $2))
		ON CONFLICT (prefix) DO UPDATE SET not_before = EXCLUDED.not_before, attempts = 0, last_error = ''`,
		prefix, due.Seconds()); err != nil {
		t.Fatal(err)
	}
}

type queueRow struct {
	attempts  int
	lastError string
	due       bool
}

func queueRows(t *testing.T, pool *pgxpool.Pool) map[string]queueRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT prefix, attempts, last_error, not_before <= now() FROM public.blob_gc_queue
		WHERE prefix LIKE 'chapter-images/19%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]queueRow{}
	for rows.Next() {
		var prefix string
		var row queueRow
		if err := rows.Scan(&prefix, &row.attempts, &row.lastError, &row.due); err != nil {
			t.Fatal(err)
		}
		result[prefix] = row
	}
	return result
}

func TestCollectorDeletesDeadPrefixesDB(t *testing.T) {
	pool := testPool(t)
	store := newFakeStore()
	old := time.Now().Add(-48 * time.Hour)
	for n := range 6 {
		store.objects[testPrefix(n)+"0/w300.webp"] = old
		store.objects[testPrefix(n)+"1/w300.webp"] = old
	}
	queue(t, pool, testPrefix(0), -time.Minute) // dead: deleted
	queue(t, pool, testPrefix(1), -time.Minute) // referenced again: kept
	queue(t, pool, testPrefix(2), time.Hour)    // not due yet
	queue(t, pool, testPrefix(3), -time.Minute) // storage fails
	queue(t, pool, testPrefix(4), -time.Minute) // a key survives the delete
	queue(t, pool, "chapter-images/1999999999/", -time.Minute)
	store.deleteErr[testPrefix(3)] = errors.New("s3 unavailable")
	store.sticky[testPrefix(4)+"1/w300.webp"] = true
	// No namespace here owns 1999999999: stand in for a newer build's queue.
	ns := testNamespace(map[string]bool{testPrefix(1): true})
	owned := ns
	owned.Group = func(key string) (string, bool) {
		if strings.HasPrefix(key, "chapter-images/1999999999/") {
			return "", false
		}
		return ns.Group(key)
	}

	stats, err := NewCollector(pool, store, owned).Collect(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if want := (CollectStats{Deleted: 1, Objects: 2, Kept: 1, Retried: 3}); stats != want {
		t.Fatalf("stats %+v, want %+v", stats, want)
	}
	rows := queueRows(t, pool)
	if _, ok := rows[testPrefix(0)]; ok {
		t.Error("deleted prefix is still queued")
	}
	if _, ok := rows[testPrefix(1)]; ok || store.deletes[testPrefix(1)] != 0 {
		t.Error("a referenced prefix must be dequeued without deleting anything")
	}
	if row := rows[testPrefix(2)]; row.attempts != 0 || store.deletes[testPrefix(2)] != 0 {
		t.Errorf("a prefix not yet due was touched: %+v", row)
	}
	for _, prefix := range []string{testPrefix(3), testPrefix(4), "chapter-images/1999999999/"} {
		if row, ok := rows[prefix]; !ok || row.attempts != 1 || row.due || row.lastError == "" {
			t.Errorf("%s: want one failed attempt rescheduled with its error, got %+v (queued %t)", prefix, row, ok)
		}
	}
	if _, ok := store.objects[testPrefix(0)+"0/w300.webp"]; ok {
		t.Error("dead prefix's objects remain")
	}
}

func TestCollectorsShareTheQueueDB(t *testing.T) {
	pool := testPool(t)
	store := newFakeStore()
	var prefixes []string
	for n := range 40 {
		prefixes = append(prefixes, testPrefix(100+n))
		store.objects[testPrefix(100+n)+"0/w300.webp"] = time.Now()
		queue(t, pool, testPrefix(100+n), -time.Minute)
	}
	ns := testNamespace(nil)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := NewCollector(pool, store, ns).Collect(t.Context(), 100); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, prefix := range prefixes {
		if n := store.deletes[prefix]; n != 1 {
			t.Errorf("%s deleted %d times, want once", prefix, n)
		}
	}
	if left := queueRows(t, pool); len(left) != 0 {
		t.Errorf("rows left: %v", slices.Collect(maps.Keys(left)))
	}
}

func TestCollectorTruncatesMultibyteErrorsDB(t *testing.T) {
	pool := testPool(t)
	store := newFakeStore()
	prefix := testPrefix(150)
	queue(t, pool, prefix, -time.Minute)
	store.deleteErr[prefix] = errors.New(strings.Repeat("x", 499) + "雪")
	stats, err := NewCollector(pool, store, testNamespace(nil)).Collect(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	row := queueRows(t, pool)[prefix]
	if stats.Retried != 1 || row.attempts != 1 || row.due || row.lastError != strings.Repeat("x", 499) {
		t.Fatalf("retry = %+v, row = %+v", stats, row)
	}
}

func TestCollectorRetriesSingleObjectAfterPartialDeleteDB(t *testing.T) {
	pool := testPool(t)
	store := newFakeStore()
	key := testPrefix(175) + "0/w300.webp"
	store.objects[key] = time.Now()
	store.sticky[key] = true
	queue(t, pool, key, -time.Minute)
	ns := testNamespace(nil)
	ns.Group = func(key string) (string, bool) { return key, true }
	collector := NewCollector(pool, store, ns)
	stats, err := collector.Collect(t.Context(), 1)
	if err != nil || stats.Retried != 1 || stats.Deleted != 0 {
		t.Fatalf("partial deletion: stats=%+v err=%v", stats, err)
	}
	if row, ok := queueRows(t, pool)[key]; !ok || row.attempts != 1 || row.due || row.lastError == "" {
		t.Fatalf("partial deletion lost its retry: row=%+v queued=%t", row, ok)
	}
	store.sticky[key] = false
	queue(t, pool, key, -time.Minute)
	stats, err = collector.Collect(t.Context(), 1)
	if err != nil || stats.Deleted != 1 || stats.Objects != 1 {
		t.Fatalf("deletion retry: stats=%+v err=%v", stats, err)
	}
	if _, ok := queueRows(t, pool)[key]; ok {
		t.Fatal("deleted object is still queued")
	}
}

func TestSweeperQueuesOldDeadPrefixesDB(t *testing.T) {
	pool := testPool(t)
	store := newFakeStore()
	old, recent := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	live := map[string]bool{}
	for n := range 30 {
		prefix := testPrefix(200 + n)
		store.objects[prefix+"0/w300.webp"] = old
		if n < 20 {
			live[prefix] = true
		}
	}
	// A dead prefix with one recent object is left alone.
	store.objects[testPrefix(225)+"1/w300.webp"] = recent
	// Already queued: not counted again.
	queue(t, pool, testPrefix(226), time.Hour)
	store.objects["chapter-images/not-an-id.webp"] = old

	sweeper := NewSweeper(pool, store, testNamespace(live))
	stats, err := sweeper.Sweep(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	got := stats.Namespaces[0]
	want := NamespaceSweep{Root: "chapter-images/", Objects: 32, Unrecognized: 1, Prefixes: 30, Live: 20, TooNew: 1, Scheduled: 1, Queued: 8}
	if got != want {
		t.Fatalf("sweep %+v, want %+v", got, want)
	}
	rows := queueRows(t, pool)
	for n := 20; n < 30; n++ {
		_, queued := rows[testPrefix(200+n)]
		if want := n != 25; queued != want {
			t.Errorf("%s queued %t, want %t", testPrefix(200+n), queued, want)
		}
	}
	if len(store.deletes) != 0 {
		t.Error("the sweep must not delete anything itself")
	}
}

func TestSweeperStopsWhenMostPrefixesLookDeadDB(t *testing.T) {
	pool := testPool(t)
	store := newFakeStore()
	for n := range 30 {
		store.objects[testPrefix(300+n)+"0/w300.webp"] = time.Now().Add(-48 * time.Hour)
	}
	stats, err := NewSweeper(pool, store, testNamespace(map[string]bool{testPrefix(300): true})).Sweep(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if ns := stats.Namespaces[0]; !ns.StoppedOnAnomaly || ns.Queued != 0 {
		t.Fatalf("sweep %+v, want a stop with nothing queued", ns)
	}
	if rows := queueRows(t, pool); len(rows) != 0 {
		t.Fatalf("queued %d prefixes on an anomaly", len(rows))
	}
}

func TestSweeperSkipsWhileAnotherServerSweepsDB(t *testing.T) {
	pool := testPool(t)
	lock, ok, err := pglock.TryAcquire(t.Context(), pool, sweepAdvisoryLock)
	if err != nil || !ok {
		t.Fatalf("take the lock: %v %t", err, ok)
	}
	defer func() { _ = lock.Release(context.Background()) }()
	stats, err := NewSweeper(pool, newFakeStore(), testNamespace(nil)).Sweep(t.Context(), 100)
	if err != nil || !stats.Skipped {
		t.Fatalf("stats %+v err %v, want a skip", stats, err)
	}
}

func TestCollectRetryDelay(t *testing.T) {
	for attempts, want := range map[int]time.Duration{0: 10 * time.Minute, 1: 20 * time.Minute, 3: 80 * time.Minute, 7: 1280 * time.Minute, 8: 24 * time.Hour, 40: 24 * time.Hour} {
		if got := collectRetryDelay(attempts); got != want {
			t.Errorf("attempts %d: delay %s, want %s", attempts, got, want)
		}
	}
}
