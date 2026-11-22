package adminjob

import (
	"context"
	"errors"
	"fmt"
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
