package recommendations

import "testing"

func TestFilterAndRankGenreMatchesPrioritizesHigherOverlap(t *testing.T) {
	items := []ScoredItem{
		{MediaItemID: "single-high", Score: 0.95},
		{MediaItemID: "double-mid", Score: 0.80},
		{MediaItemID: "double-low", Score: 0.60},
		{MediaItemID: "single-low", Score: 0.50},
		{MediaItemID: "no-match", Score: 0.99},
	}

	genreMap := map[string][]string{
		"single-high": {"Action"},
		"double-mid":  {"Action", "Thriller"},
		"double-low":  {"Thriller", "Action", "Mystery"},
		"single-low":  {"Thriller"},
		"no-match":    {"Comedy"},
	}

	ranked := FilterAndRankGenreMatches(items, []string{"Action", "Thriller"}, genreMap)

	if len(ranked) != 4 {
		t.Fatalf("expected 4 ranked items, got %d", len(ranked))
	}

	wantOrder := []string{"double-mid", "double-low", "single-high", "single-low"}
	for i, want := range wantOrder {
		if ranked[i].MediaItemID != want {
			t.Fatalf("item %d: expected %q, got %q", i, want, ranked[i].MediaItemID)
		}
	}
}
