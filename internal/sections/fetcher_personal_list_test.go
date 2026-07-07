package sections

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Watchlist and favorites sections are profile-scoped: with no user store (or
// no authenticated profile) they must degrade to an empty rail instead of the
// "unsupported section type" error they returned before being wired up.
func TestFetchSectionPersonalListWithoutStoreReturnsEmpty(t *testing.T) {
	t.Parallel()

	f := &Fetcher{}
	for _, sectionType := range []SectionType{SectionWatchlist, SectionFavorites} {
		s := ResolvedSection{ID: "s1", SectionType: sectionType, ItemLimit: 20}
		items, total, err := f.fetchSection(context.Background(), s, nil, nil, 0, "", catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("fetchSection(%s) error = %v, want nil", sectionType, err)
		}
		if len(items) != 0 || total != 0 {
			t.Fatalf("fetchSection(%s) = %d items, total %d; want empty", sectionType, len(items), total)
		}
	}
}

func TestSortPersonalListItems(t *testing.T) {
	t.Parallel()

	f64 := func(v float64) *float64 { return &v }
	str := func(v string) *string { return &v }
	build := func() []*models.MediaItem {
		return []*models.MediaItem{
			{ContentID: "b", Title: "Beta", ReleaseDate: str("2020-01-01"), RatingIMDB: f64(6.1)},
			{ContentID: "a", Title: "alpha", ReleaseDate: str("2024-05-05"), RatingIMDB: nil},
			{ContentID: "c", Title: "Gamma", FirstAirDate: str("2022-03-03"), RatingIMDB: f64(8.4)},
			{ContentID: "d", Title: "Delta", RatingIMDB: f64(7.0)},
		}
	}

	cases := []struct {
		field, order string
		want         []string
	}{
		{"title", "asc", []string{"a", "b", "d", "c"}},
		{"title", "desc", []string{"c", "d", "b", "a"}},
		// FirstAirDate is used when ReleaseDate is missing; unknown dates last.
		{"release_date", "desc", []string{"a", "c", "b", "d"}},
		{"release_date", "asc", []string{"b", "c", "a", "d"}},
		// Missing ratings sort last regardless of direction.
		{"rating_imdb", "desc", []string{"c", "d", "b", "a"}},
		{"rating_imdb", "asc", []string{"b", "d", "c", "a"}},
	}
	for _, tc := range cases {
		qs, ok := catalog.NormalizePersonalListSort(tc.field, tc.order)
		if !ok {
			t.Fatalf("NormalizePersonalListSort(%s, %s) not ok", tc.field, tc.order)
		}
		items := build()
		sortPersonalListItems(items, qs)
		got := make([]string, len(items))
		for i, item := range items {
			got[i] = item.ContentID
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("sort %s %s = %v, want %v", tc.field, tc.order, got, tc.want)
			}
		}
	}
}

func TestNormalizePersonalListSortDefaultsAndRejects(t *testing.T) {
	t.Parallel()

	if qs, ok := catalog.NormalizePersonalListSort("title", ""); !ok || qs.Order != "asc" {
		t.Fatalf("title default = %+v, %v; want asc", qs, ok)
	}
	if qs, ok := catalog.NormalizePersonalListSort("release_date", ""); !ok || qs.Order != "desc" {
		t.Fatalf("release_date default = %+v, %v; want desc", qs, ok)
	}
	if _, ok := catalog.NormalizePersonalListSort("", "asc"); ok {
		t.Fatal("empty field must keep list order")
	}
	if _, ok := catalog.NormalizePersonalListSort("progress", ""); ok {
		t.Fatal("personalized/unsupported fields must be rejected")
	}
}

// added_at sorts by when the item was added to the list, from the entry
// timestamps rather than item metadata; missing timestamps sort last and any
// other sort keeps the stored list order.
func TestOrderPersonalListIDsByAddedAt(t *testing.T) {
	t.Parallel()

	build := func() []catalog.PersonalListEntry {
		return []catalog.PersonalListEntry{
			{ID: "synced", AddedAt: "2024-02-02T00:00:00Z"},
			{ID: "old", AddedAt: "2023-01-01T00:00:00Z"},
			{ID: "new", AddedAt: "2025-06-06T00:00:00Z"},
			{ID: "unknown", AddedAt: ""},
		}
	}

	qs, ok := catalog.NormalizePersonalListSort("added_at", "")
	if !ok || qs.Order != "desc" {
		t.Fatalf("added_at default = %+v, %v; want desc", qs, ok)
	}

	got := catalog.OrderPersonalListIDs(build(), qs)
	want := []string{"new", "synced", "old", "unknown"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("added_at desc = %v, want %v", got, want)
		}
	}

	got = catalog.OrderPersonalListIDs(build(), catalog.QuerySort{Field: "added_at", Order: "asc"})
	want = []string{"old", "synced", "new", "unknown"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("added_at asc = %v, want %v", got, want)
		}
	}

	got = catalog.OrderPersonalListIDs(build(), catalog.QuerySort{})
	want = []string{"synced", "old", "new", "unknown"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("no sort = %v, want stored order %v", got, want)
		}
	}
}
