package adminjob

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// cleanupStore records DeletePrefix calls. Only DeletePrefix is implemented;
// the executor uses nothing else.
type cleanupStore struct {
	blobstore.Store
	deleteFn func(ctx context.Context, call int, prefix string) (int, error)

	mu    sync.Mutex
	calls []string
}

func (s *cleanupStore) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	s.mu.Lock()
	call := len(s.calls)
	s.calls = append(s.calls, prefix)
	s.mu.Unlock()
	if s.deleteFn != nil {
		return s.deleteFn(ctx, call, prefix)
	}
	return 1, nil
}

func (s *cleanupStore) attempted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func cleanupPrefixes(n int) []string {
	prefixes := make([]string, n)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("local/ebooks/%d/", i)
	}
	return prefixes
}

// Once the context ends, every remaining delete would fail at once. The
// executor must stop there, not report each remaining prefix as attempted.
func TestImageCacheCleanupStopsAtFirstContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &cleanupStore{deleteFn: func(ctx context.Context, call int, _ string) (int, error) {
		if call == 3 {
			cancel()
			return 0, ctx.Err()
		}
		return 2, nil
	}}
	last := -1
	next, deleted, err := NewImageCacheCleanupExecutor(store).Execute(ctx, ImageCacheCleanupRequest{Prefixes: cleanupPrefixes(10)}, 0,
		func(next int, _ ImageCacheCleanupResult) { last = next })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context.Canceled", err)
	}
	if next != 3 || last != 3 {
		t.Fatalf("stopped at %d after progress %d, want the interrupted prefix 3", next, last)
	}
	if deleted.DeletedPrefixes != 3 || deleted.DeletedS3Objects != 6 {
		t.Fatalf("deleted %+v, want 3 prefixes and 6 objects", deleted)
	}
	if calls := store.attempted(); len(calls) != 4 {
		t.Fatalf("deletes after the context ended: %v", calls)
	}
}

// A store can report success for a prefix it stopped deleting partway when the
// context ended. That prefix must be retried, and the objects it did delete
// still counted.
func TestImageCacheCleanupRetriesAPrefixInterruptedByTheDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &cleanupStore{deleteFn: func(ctx context.Context, call int, _ string) (int, error) {
		if call == 1 {
			cancel()
			return 3, nil
		}
		return 1, nil
	}}
	next, deleted, err := NewImageCacheCleanupExecutor(store).Execute(ctx, ImageCacheCleanupRequest{Prefixes: cleanupPrefixes(5)}, 0, nil)
	if !errors.Is(err, context.Canceled) || next != 1 {
		t.Fatalf("Execute = %d, %v, want 1, context.Canceled", next, err)
	}
	if deleted.DeletedPrefixes != 1 || deleted.DeletedS3Objects != 4 {
		t.Fatalf("deleted %+v, want 1 finished prefix and 4 objects", deleted)
	}
}

func TestImageCacheCleanupResumesAtStart(t *testing.T) {
	prefixes := cleanupPrefixes(5)
	store := &cleanupStore{}
	var reported []int
	next, deleted, err := NewImageCacheCleanupExecutor(store).Execute(t.Context(), ImageCacheCleanupRequest{Prefixes: prefixes}, 2,
		func(next int, _ ImageCacheCleanupResult) { reported = append(reported, next) })
	if err != nil || next != 5 {
		t.Fatalf("Execute = %d, %v, want 5, nil", next, err)
	}
	if calls := store.attempted(); !reflect.DeepEqual(calls, prefixes[2:]) {
		t.Fatalf("attempted %v, want %v", calls, prefixes[2:])
	}
	if !reflect.DeepEqual(reported, []int{3, 4, 5}) {
		t.Fatalf("progress %v, want [3 4 5]", reported)
	}
	if deleted.DeletedPrefixes != 3 {
		t.Fatalf("deleted %d prefixes, want 3", deleted.DeletedPrefixes)
	}
}

func TestImageCacheCleanupSkipsAFailedPrefix(t *testing.T) {
	store := &cleanupStore{deleteFn: func(_ context.Context, call int, _ string) (int, error) {
		if call == 1 {
			return 0, errors.New("access denied")
		}
		return 1, nil
	}}
	next, deleted, err := NewImageCacheCleanupExecutor(store).Execute(t.Context(), ImageCacheCleanupRequest{Prefixes: cleanupPrefixes(3)}, 0, nil)
	if err != nil || next != 3 {
		t.Fatalf("Execute = %d, %v, want 3, nil", next, err)
	}
	if deleted.DeletedPrefixes != 2 || len(store.attempted()) != 3 {
		t.Fatalf("deleted %+v after %d attempts, want 2 deleted of 3", deleted, len(store.attempted()))
	}
}
