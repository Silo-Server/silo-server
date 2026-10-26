package catalog

import (
	"testing"
)

// TestBrowseScopeMayContainManga pins the gating that lets browse skip the two
// manga count subqueries when the scope cannot return manga rows. An empty type
// filter (all types) or one that includes "manga" keeps them; any other
// explicit type filter rules manga out.
func TestBrowseScopeMayContainManga(t *testing.T) {
	cases := []struct {
		typeFilter string
		want       bool
	}{
		{"", true},
		{"manga", true},
		{"movie,manga", true},
		{" manga ", true},
		{"movie", false},
		{"movie,series,episode", false},
		{"ebook", false},
	}
	for _, c := range cases {
		if got := browseScopeMayContainManga(BrowseFilters{Type: c.typeFilter}); got != c.want {
			t.Fatalf("browseScopeMayContainManga(%q) = %v, want %v", c.typeFilter, got, c.want)
		}
	}
}
