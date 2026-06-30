package catalog

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func tsPtr(t time.Time) *time.Time { return &t }

func contentIDs(items []*models.MediaItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ContentID
	}
	return ids
}

func TestMergeRecentlyAddedItems_OrdersByAddedAtDescThenContentID(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// Two already-sorted per-library slices (added_at DESC, content_id ASC).
	libA := []*models.MediaItem{
		{ContentID: "a3", AddedAt: tsPtr(base.Add(3 * time.Hour))},
		{ContentID: "a1", AddedAt: tsPtr(base.Add(1 * time.Hour))},
	}
	libB := []*models.MediaItem{
		{ContentID: "b4", AddedAt: tsPtr(base.Add(4 * time.Hour))},
		{ContentID: "b2", AddedAt: tsPtr(base.Add(2 * time.Hour))},
	}

	got := contentIDs(mergeRecentlyAddedItems([][]*models.MediaItem{libA, libB}))
	want := []string{"b4", "a3", "b2", "a1"}
	if len(got) != len(want) {
		t.Fatalf("merged length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged order = %v, want %v", got, want)
		}
	}
}

func TestMergeRecentlyAddedItems_TieBreaksByContentIDAsc(t *testing.T) {
	at := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	libA := []*models.MediaItem{{ContentID: "zeta", AddedAt: tsPtr(at)}}
	libB := []*models.MediaItem{{ContentID: "alpha", AddedAt: tsPtr(at)}}

	got := contentIDs(mergeRecentlyAddedItems([][]*models.MediaItem{libA, libB}))
	want := []string{"alpha", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tie-break order = %v, want %v", got, want)
		}
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

func TestMergeRecentlyAddedItems_NilAddedAtSortsLast(t *testing.T) {
	at := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	libA := []*models.MediaItem{{ContentID: "nil1", AddedAt: nil}}
	libB := []*models.MediaItem{{ContentID: "dated", AddedAt: tsPtr(at)}}

	got := contentIDs(mergeRecentlyAddedItems([][]*models.MediaItem{libA, libB}))
	want := []string{"dated", "nil1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nil-handling order = %v, want %v", got, want)
		}
	}
}
