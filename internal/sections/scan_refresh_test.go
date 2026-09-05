package sections

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestScanRefreshServesCachedRowWhileRebuilding(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()
	now := time.Now()
	resolved := ResolvedSection{SectionType: SectionRecentlyAdded, ItemLimit: 20}
	scope := catalog.AccessFilter{AllowedLibraryIDs: []int{7}, MaxContentRating: "PG"}
	key := resolvedListCacheKey(resolved, nil, []int{7}, scope)
	resolvedListSet(key, mediaItems("cached"), 1, now)
	InvalidateResolvedListCache()
	key = resolvedListCacheKey(resolved, nil, []int{7}, scope)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	loader := func(context.Context) ([]*models.MediaItem, int, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return mediaItems("fresh"), 1, nil
	}
	type result struct {
		items []*models.MediaItem
		err   error
	}
	returned := make(chan result, 1)
	go func() {
		items, _, err := getOrRefresh(t.Context(), key, time.Now(), loader)
		returned <- result{items, err}
	}()
	// Always release and join the refresh, including when the old blocking
	// behavior makes the foreground request miss its deadline.
	defer func() {
		close(release)
		if !waitFor(2*time.Second, func() bool {
			entry, ok := resolvedListGet(key)
			resolvedListRefreshMu.Lock()
			idle := len(resolvedListRefreshing) == 0
			resolvedListRefreshMu.Unlock()
			return idle && ok && len(entry.items) == 1 && entry.items[0].ContentID == "fresh"
		}) {
			t.Error("completed refresh did not replace the cached row")
		}
	}()
	<-started
	select {
	case got := <-returned:
		if got.err != nil || len(got.items) != 1 || got.items[0].ContentID != "cached" {
			t.Fatalf("got %v, %v; want cached row during refresh", itemIDs(got.items), got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("page request is blocked on the post-scan row rebuild")
	}
	// Concurrent page requests keep using the row and join one refresh.
	for range 10 {
		items, _, err := getOrRefresh(t.Context(), key, time.Now(), loader)
		if err != nil || len(items) != 1 || items[0].ContentID != "cached" {
			t.Fatalf("refresh waiter got %v, %v", itemIDs(items), err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("started %d refreshes; want 1", calls.Load())
	}
	// A different access scope must still perform its own read.
	otherKey := resolvedListCacheKey(resolved, nil, []int{7}, catalog.AccessFilter{AllowedLibraryIDs: []int{7}, MaxContentRating: "G"})
	items, _, err := getOrRefresh(t.Context(), otherKey, time.Now(), staticLoader(mediaItems("restricted"), nil))
	if err != nil || len(items) != 1 || items[0].ContentID != "restricted" {
		t.Fatalf("new access scope received migrated membership: %v, %v", itemIDs(items), err)
	}
}
