package sections

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
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

// A discovery row loses the profile's marked titles and keeps the rest in
// order; it is not fetched again wider, which would change a daily mix.
func TestWithoutNotInterestedFiltersTheRow(t *testing.T) {
	f := &Fetcher{StoreProvider: markStoreProvider{markStore{marks: map[string]bool{"t01": true, "t03": true}}}}
	section := ResolvedSection{ID: "s", SectionType: SectionMostWatched, ItemLimit: 4}

	items, total := f.withoutNotInterested(t.Context(), section, 1, "p", titleRow(4), 50)
	if want := []string{"t00", "t02"}; !slices.Equal(rowIDs(items), want) {
		t.Fatalf("row = %v, want %v", rowIDs(items), want)
	}
	if total != 48 {
		t.Fatalf("total = %d, want 48", total)
	}
	clean := []*models.MediaItem{{ContentID: "t00"}, {ContentID: "t02"}}
	if items, total := f.withoutNotInterested(t.Context(), section, 1, "p", clean, 2); len(items) != 2 || total != 2 {
		t.Fatalf("a row without marked titles changed: %v, %d", rowIDs(items), total)
	}
}

// A failed mark lookup leaves the row as fetched.
func TestWithoutNotInterestedFailsOpen(t *testing.T) {
	f := &Fetcher{StoreProvider: markStoreProvider{markStore{err: errors.New("store down")}}}
	row := titleRow(3)
	items, total := f.withoutNotInterested(t.Context(), ResolvedSection{ItemLimit: 3}, 1, "p", row, 3)
	if len(items) != 3 || total != 3 {
		t.Fatalf("row = %v, %d; want it unchanged", rowIDs(items), total)
	}
}

// A discovery recipe suggests titles the profile did not pick, so each one
// must leave marked titles out.
func TestHidesNotInterestedCoversDiscoveryRecipes(t *testing.T) {
	for _, recipe := range recipes.List() {
		def := recipe.Definition()
		if def.Category == recipes.CategoryDiscovery && !HidesNotInterested(SectionType(def.Type)) {
			t.Errorf("discovery recipe %q keeps Not interested titles", def.Type)
		}
	}
}
