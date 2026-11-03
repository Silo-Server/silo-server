package catalog

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestEscapePrefixForLike checks that the helper lowercases input and escapes
// the LIKE wildcards (% _) and the escape character itself (\).
func TestEscapePrefixForLike(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Star", "star"},
		{"  Star  ", "star"},
		{"50%", `50\%`},
		{"a_b", `a\_b`},
		{`back\slash`, `back\\slash`},
		{`%_\`, `\%\_\\`},
	}
	for _, tc := range cases {
		got := escapePrefixForLike(tc.in)
		if got != tc.want {
			t.Errorf("escapePrefixForLike(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFilterCatalogNamePrefix_MatchesSortKeyOnly keeps the in-memory prefix
// filter (relevance search, exact-order collections, offset pages) in step with
// sortTitlePrefixCondition, so offset and cursor pages agree on the letter.
func TestFilterCatalogNamePrefix_MatchesSortKeyOnly(t *testing.T) {
	hobbit := &models.MediaItem{ContentID: "hobbit", Title: "The Hobbit", SortTitle: "Hobbit, The"}
	hobgoblins := &models.MediaItem{ContentID: "hobgoblins", Title: "Hobgoblins"}
	titanic := &models.MediaItem{ContentID: "titanic", Title: "Titanic", SortTitle: "  "}
	// SQL BTRIM strips spaces only, so a tab-led sort title keeps its tab.
	tabbed := &models.MediaItem{ContentID: "tabbed", Title: "Heat", SortTitle: "\tHeat"}
	items := []*models.MediaItem{hobbit, nil, hobgoblins, titanic, tabbed}

	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{prefix: "", want: []string{"hobbit", "", "hobgoblins", "titanic", "tabbed"}},
		{prefix: "H", want: []string{"hobbit", "hobgoblins"}},
		{prefix: " t ", want: []string{"titanic"}},
		{prefix: "the", want: []string{}},
	} {
		var got []string
		for _, item := range filterCatalogNamePrefix(items, tc.prefix) {
			if item == nil {
				got = append(got, "")
				continue
			}
			got = append(got, item.ContentID)
		}
		if got == nil {
			got = []string{}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("filterCatalogNamePrefix(%q) = %v, want %v", tc.prefix, got, tc.want)
		}
	}
}
