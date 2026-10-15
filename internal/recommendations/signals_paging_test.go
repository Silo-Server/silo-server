package recommendations

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Anchors are live catalog items: a deleted movie and an episode of a
// deleted series (which no longer resolves to it) are passed over, also when
// a full page of dead completions comes first.
func TestRecentCompletedItemIDsSkipsDeletedItems(t *testing.T) {
	at := func(minutesAgo int) string {
		return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Add(-time.Duration(minutesAgo) * time.Minute).Format(time.RFC3339)
	}
	completed := func(id string, minutesAgo int) userstore.WatchProgress {
		return userstore.WatchProgress{ProfileID: "p1", MediaItemID: id, Completed: true, UpdatedAt: at(minutesAgo)}
	}
	missing := map[string]struct{}{"deleted-movie": {}, "orphan-episode": {}, "deleted-ebook": {}}

	store := &fakeSignalStore{progress: []userstore.WatchProgress{
		completed("deleted-movie", 1),
		completed("orphan-episode", 2),
		completed("live-b", 3),
		completed("live-episode", 4),
		completed("live-a", 5),
		completed("live-older", 6),
	}}
	repo := &fakeSignalRepo{
		canonical:     map[string]string{"live-episode": "live-series"},
		missing:       missing,
		ebookProgress: []WatchProgressRow{{MediaItemID: "deleted-ebook", Completed: true, UpdatedAt: time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC)}},
	}
	got, err := NewSignalReader(repo, fakeSignalProvider{store: store}).RecentCompletedItemIDs(t.Context(), 7, "p1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"live-b", "live-series", "live-a"}; !slices.Equal(got, want) {
		t.Fatalf("anchors = %v, want %v", got, want)
	}

	// More than a page of newer dead completions: paging goes on until it
	// has three live anchors.
	paged := &fakeSignalStore{}
	for i := range signalPageSize + 5 {
		id := fmt.Sprintf("dead-%04d", i)
		missing[id] = struct{}{}
		paged.progress = append(paged.progress, completed(id, 1))
	}
	paged.progress = append(paged.progress, completed("live-b", 3), completed("live-a", 5), completed("live-older", 6))
	got, err = NewSignalReader(&fakeSignalRepo{missing: missing}, fakeSignalProvider{store: paged}).RecentCompletedItemIDs(t.Context(), 7, "p1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"live-b", "live-a", "live-older"}; !slices.Equal(got, want) {
		t.Fatalf("anchors after a page of dead completions = %v, want %v", got, want)
	}
}

// keysetOnlyStore fails the test if a progress walk falls back to offset
// paging.
type keysetOnlyStore struct {
	*fakeSignalStore
	t     *testing.T
	pages int
}

func (s *keysetOnlyStore) ListProgress(context.Context, string, string, int, int) ([]userstore.WatchProgress, error) {
	s.t.Fatal("progress read with offset paging")
	return nil, nil
}

func (s *keysetOnlyStore) ListProgressPage(ctx context.Context, profileID, status string, after *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, error) {
	s.pages++
	return s.fakeSignalStore.ListProgressPage(ctx, profileID, status, after, limit)
}

// The watched set walks progress by keyset across pages, counting every row
// at least half watched, including rows tied on updated_at at a page edge.
func TestWatchedItemIDSetPagesByKeyset(t *testing.T) {
	const rows = 2*signalPageSize + 7
	inner := &fakeSignalStore{}
	want := map[string]struct{}{}
	for i := range rows {
		id := fmt.Sprintf("item-%05d", i)
		// Ten rows share each timestamp, so page edges fall inside ties.
		wp := userstore.WatchProgress{
			ProfileID:       "p1",
			MediaItemID:     id,
			DurationSeconds: 100,
			UpdatedAt:       time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i/10) * time.Second).Format(time.RFC3339),
		}
		switch i % 3 {
		case 0:
			wp.Completed = true
			want[id] = struct{}{}
		case 1:
			wp.PositionSeconds = 60
			want[id] = struct{}{}
		default:
			wp.PositionSeconds = 10
		}
		inner.progress = append(inner.progress, wp)
	}
	store := &keysetOnlyStore{fakeSignalStore: inner, t: t}

	got, err := NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: store}).WatchedItemIDSet(t.Context(), 7, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("watched set has %d items, want %d", len(got), len(want))
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("watched set misses %s", id)
		}
	}
	if store.pages != 3 {
		t.Fatalf("pages read = %d, want 3", store.pages)
	}
}
