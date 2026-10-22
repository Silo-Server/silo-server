package catalog

import (
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRecentlyAddedLibraryBrowseFiltersPreserveDisabledLibraries(t *testing.T) {
	base := BrowseFilters{
		LibraryIDs:         []int{1, 2},
		DisabledLibraryIDs: []int{9},
		Offset:             25,
		Limit:              50,
	}

	got := recentlyAddedLibraryBrowseFilters(base, 7, 20)
	if got.LibraryID != 7 {
		t.Fatalf("LibraryID = %d, want 7", got.LibraryID)
	}
	if got.LibraryIDs != nil {
		t.Fatalf("LibraryIDs = %v, want nil for the single-library fast path", got.LibraryIDs)
	}
	if !slices.Equal(got.DisabledLibraryIDs, []int{9}) {
		t.Fatalf("DisabledLibraryIDs = %v, want [9]", got.DisabledLibraryIDs)
	}
	if got.Offset != 0 || got.Limit != 20 {
		t.Fatalf("Offset/Limit = %d/%d, want 0/20", got.Offset, got.Limit)
	}
}

func tsPtr(t time.Time) *time.Time { return &t }

func contentIDs(items []*models.MediaItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ContentID
	}
	return ids
}

func TestMergeRecentlyAddedItems_Order(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tie := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		libs [][]*models.MediaItem
		want []string
	}{
		{
			// Two already-sorted per-library slices (added_at DESC, content_id ASC).
			name: "added_at desc across libraries",
			libs: [][]*models.MediaItem{
				{
					{ContentID: "a3", AddedAt: tsPtr(base.Add(3 * time.Hour))},
					{ContentID: "a1", AddedAt: tsPtr(base.Add(1 * time.Hour))},
				},
				{
					{ContentID: "b4", AddedAt: tsPtr(base.Add(4 * time.Hour))},
					{ContentID: "b2", AddedAt: tsPtr(base.Add(2 * time.Hour))},
				},
			},
			want: []string{"b4", "a3", "b2", "a1"},
		},
		{
			name: "tie breaks by content_id asc",
			libs: [][]*models.MediaItem{{{ContentID: "zeta", AddedAt: tsPtr(tie)}}, {{ContentID: "alpha", AddedAt: tsPtr(tie)}}},
			want: []string{"alpha", "zeta"},
		},
		{
			name: "nil added_at sorts last",
			libs: [][]*models.MediaItem{{{ContentID: "nil1", AddedAt: nil}}, {{ContentID: "dated", AddedAt: tsPtr(base)}}},
			want: []string{"dated", "nil1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := contentIDs(mergeRecentlyAddedItems(tc.libs)); !slices.Equal(got, tc.want) {
				t.Fatalf("merged order = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMergeRecentlyAddedItems_DedupsKeepingEarliestAddedAt(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	other := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// "dup" appears in both libraries with different first_seen_at. The merge
	// must collapse it to one entry carrying the EARLIEST timestamp (MIN
	// semantics), which also moves it after "other" in DESC order.
	libA := []*models.MediaItem{
		{ContentID: "dup", AddedAt: tsPtr(late)},
	}
	libB := []*models.MediaItem{
		{ContentID: "other", AddedAt: tsPtr(other)},
		{ContentID: "dup", AddedAt: tsPtr(early)},
	}

	merged := mergeRecentlyAddedItems([][]*models.MediaItem{libA, libB})
	if got := len(merged); got != 2 {
		t.Fatalf("merged length = %d, want 2 (dup must collapse): %v", got, contentIDs(merged))
	}
	// other (March) should now precede dup (January).
	want := []string{"other", "dup"}
	for i := range want {
		if merged[i].ContentID != want[i] {
			t.Fatalf("dedup order = %v, want %v", contentIDs(merged), want)
		}
	}
	var dup *models.MediaItem
	for _, it := range merged {
		if it.ContentID == "dup" {
			dup = it
		}
	}
	if dup.AddedAt == nil || !dup.AddedAt.Equal(early) {
		t.Fatalf("dup AddedAt = %v, want earliest %v", dup.AddedAt, early)
	}
}
