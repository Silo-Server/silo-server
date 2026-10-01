package catalog

import "testing"

// TV event grouping produces both series and episode cards, so it can only run
// for sections whose configured filter_type it is able to honor. Anything
// else has to fall through to the plain recently-added query, which applies
// the type filter itself.
func TestRecentTVFilterTypeScope(t *testing.T) {
	for _, tc := range []struct {
		filterType     string
		explicitSeries bool
		tvEligible     bool
	}{
		{filterType: "", explicitSeries: false, tvEligible: true},
		{filterType: "series", explicitSeries: true, tvEligible: true},
		{filterType: "  Series  ", explicitSeries: true, tvEligible: true},
		{filterType: "episode", explicitSeries: false, tvEligible: false},
		{filterType: "season", explicitSeries: false, tvEligible: false},
		{filterType: "movie", explicitSeries: false, tvEligible: false},
		{filterType: "audiobook", explicitSeries: false, tvEligible: false},
	} {
		t.Run(tc.filterType, func(t *testing.T) {
			explicitSeries, tvEligible := recentTVFilterTypeScope(tc.filterType)
			if explicitSeries != tc.explicitSeries || tvEligible != tc.tvEligible {
				t.Fatalf("scope(%q) = (%v, %v), want (%v, %v)",
					tc.filterType, explicitSeries, tvEligible, tc.explicitSeries, tc.tvEligible)
			}
		})
	}
}
