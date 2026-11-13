package blobgc

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
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

// fakeStore holds object keys with modification times.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string]time.Time
	// sticky keys survive DeletePrefix, as keys a batch delete failed on do.
	sticky map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string]time.Time{}, sticky: map[string]bool{}}
}

func (s *fakeStore) Delete(_ context.Context, keys []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for _, key := range keys {
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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
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

func TestCollectRetryDelay(t *testing.T) {
	for attempts, want := range map[int]time.Duration{0: 10 * time.Minute, 1: 20 * time.Minute, 3: 80 * time.Minute, 7: 1280 * time.Minute, 8: 24 * time.Hour, 40: 24 * time.Hour} {
		if got := collectRetryDelay(attempts); got != want {
			t.Errorf("attempts %d: delay %s, want %s", attempts, got, want)
		}
	}
}
