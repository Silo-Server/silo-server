package sections

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// markStore answers ListNotInterestedByMediaItems from marks, or fails.
type markStore struct {
	userstore.UserStore
	marks map[string]bool
	err   error
}

func (s markStore) ListNotInterestedByMediaItems(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]bool{}
	for _, id := range ids {
		if s.marks[id] {
			out[id] = true
		}
	}
	return out, nil
}

type markStoreProvider struct{ store markStore }

func (markStoreProvider) Close() error { return nil }

func (p markStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func titleRow(n int) []*models.MediaItem {
	items := make([]*models.MediaItem, n)
	for i := range items {
		items[i] = &models.MediaItem{ContentID: fmt.Sprintf("t%02d", i)}
	}
	return items
}

func rowIDs(items []*models.MediaItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ContentID
	}
	return ids
}

// A discovery row loses the profile's marked titles and refills from a wider
// fetch to its limit; a row with none of them is not fetched again.
func TestWithoutNotInterestedRefillsTheRow(t *testing.T) {
	f := &Fetcher{StoreProvider: markStoreProvider{markStore{marks: map[string]bool{"t01": true, "t03": true}}}}
	section := ResolvedSection{ID: "s", SectionType: SectionMostWatched, ItemLimit: 4}
	var refetched []int
	refetch := func(s ResolvedSection) ([]*models.MediaItem, int, error) {
		refetched = append(refetched, s.ItemLimit)
		return titleRow(s.ItemLimit), 50, nil
	}

	items, total, err := f.withoutNotInterested(t.Context(), section, 1, "p", titleRow(4), 50, refetch)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"t00", "t02", "t04", "t05"}; !slices.Equal(rowIDs(items), want) {
		t.Fatalf("row = %v, want %v", rowIDs(items), want)
	}
	if total != 48 || !slices.Equal(refetched, []int{4 + notInterestedRefill(4)}) {
		t.Fatalf("total = %d, refetched = %v", total, refetched)
	}

	refetched = nil
	clean := []*models.MediaItem{{ContentID: "t00"}, {ContentID: "t02"}}
	items, _, _ = f.withoutNotInterested(t.Context(), section, 1, "p", clean, 2, refetch)
	if len(items) != 2 || refetched != nil {
		t.Fatalf("a row without marked titles changed (%v) or was fetched again (%v)", rowIDs(items), refetched)
	}

	// Without refetch the row is only filtered.
	items, _, _ = f.withoutNotInterested(t.Context(), section, 1, "p", titleRow(4), 4, nil)
	if want := []string{"t00", "t02"}; !slices.Equal(rowIDs(items), want) {
		t.Fatalf("filtered row = %v, want %v", rowIDs(items), want)
	}
}

// A failed mark lookup leaves the row as fetched.
func TestWithoutNotInterestedFailsOpen(t *testing.T) {
	f := &Fetcher{StoreProvider: markStoreProvider{markStore{err: errors.New("store down")}}}
	row := titleRow(3)
	items, total, err := f.withoutNotInterested(t.Context(), ResolvedSection{ItemLimit: 3}, 1, "p", row, 3, nil)
	if err != nil || len(items) != 3 || total != 3 {
		t.Fatalf("row = %v, %d, %v; want it unchanged", rowIDs(items), total, err)
	}
}
