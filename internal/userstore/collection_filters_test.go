package userstore

import "testing"

func TestNormalizeCollectionFilters(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "blank defaults to all", raw: "", want: CollectionWatchFilterAll, ok: true},
		{name: "normalizes case", raw: " Watched ", want: CollectionWatchFilterWatched, ok: true},
		{name: "rejects unknown", raw: "started", ok: false},
	} {
		t.Run("watch "+tc.name, func(t *testing.T) {
			got, ok := NormalizeCollectionWatchFilter(tc.raw)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("NormalizeCollectionWatchFilter(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}

	for _, tc := range []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "blank defaults to all", raw: "", want: CollectionMediaFilterAll, ok: true},
		{name: "normalizes case", raw: " Series ", want: CollectionMediaFilterSeries, ok: true},
		{name: "rejects unknown", raw: "episode", ok: false},
	} {
		t.Run("media "+tc.name, func(t *testing.T) {
			got, ok := NormalizeCollectionMediaFilter(tc.raw)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("NormalizeCollectionMediaFilter(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}
