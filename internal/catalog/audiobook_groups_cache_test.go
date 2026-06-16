package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

func newTestGroups(n int) []AudiobookGroup {
	groups := make([]AudiobookGroup, n)
	for i := range groups {
		groups[i] = AudiobookGroup{Name: "author-" + string(rune('a'+i)), ItemCount: i + 1}
	}
	return groups
}

// The grouped list is the expensive aggregation; paging through it (the client
// fetches every page on each load) must compute it once, not once per page.
func TestAudiobookGroupsCache_ComputesFullListOncePerKey(t *testing.T) {
	full := newTestGroups(5)
	var fetches int
	c := &AudiobookGroupsCache{
		cache: cache.NewTTLCache[*groupsCacheEntry](),
		ttl:   time.Minute,
		fetch: func(context.Context, AudiobookGroupsQuery, AccessFilter) ([]AudiobookGroup, int, error) {
			fetches++
			return full, len(full), nil
		},
	}

	q := AudiobookGroupsQuery{LibraryID: 7, GroupBy: AudiobookGroupByAuthor, Sort: "name"}
	filter := AccessFilter{UserID: 1, ProfileID: "p1"}

	page1, total, err := c.Page(context.Background(), AudiobookGroupsQuery{LibraryID: q.LibraryID, GroupBy: q.GroupBy, Sort: q.Sort, Limit: 2, Offset: 0}, filter)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	page2, _, err := c.Page(context.Background(), AudiobookGroupsQuery{LibraryID: q.LibraryID, GroupBy: q.GroupBy, Sort: q.Sort, Limit: 2, Offset: 2}, filter)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	page3, _, err := c.Page(context.Background(), AudiobookGroupsQuery{LibraryID: q.LibraryID, GroupBy: q.GroupBy, Sort: q.Sort, Limit: 2, Offset: 4}, filter)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}

	if fetches != 1 {
		t.Fatalf("full list computed %d times across 3 pages of one key; want 1", fetches)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(page1) != 2 || len(page2) != 2 || len(page3) != 1 {
		t.Fatalf("page sizes = %d,%d,%d; want 2,2,1", len(page1), len(page2), len(page3))
	}
	if page1[0].Name != "author-a" || page2[0].Name != "author-c" || page3[0].Name != "author-e" {
		t.Fatalf("slice boundaries wrong: %q %q %q", page1[0].Name, page2[0].Name, page3[0].Name)
	}
}

// A different viewer must not be served another profile's cached counts.
func TestAudiobookGroupsCache_KeyedByViewer(t *testing.T) {
	var fetches int
	c := &AudiobookGroupsCache{
		cache: cache.NewTTLCache[*groupsCacheEntry](),
		ttl:   time.Minute,
		fetch: func(context.Context, AudiobookGroupsQuery, AccessFilter) ([]AudiobookGroup, int, error) {
			fetches++
			return newTestGroups(3), 3, nil
		},
	}
	q := AudiobookGroupsQuery{LibraryID: 7, GroupBy: AudiobookGroupByAuthor, Sort: "name", Limit: 10}
	if _, _, err := c.Page(context.Background(), q, AccessFilter{UserID: 1, ProfileID: "p1"}); err != nil {
		t.Fatalf("viewer1: %v", err)
	}
	if _, _, err := c.Page(context.Background(), q, AccessFilter{UserID: 2, ProfileID: "p2"}); err != nil {
		t.Fatalf("viewer2: %v", err)
	}
	if fetches != 2 {
		t.Fatalf("distinct viewers shared a cache entry: fetches=%d, want 2", fetches)
	}
}
