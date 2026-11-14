package showstatus

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  ", ""},
		// TMDB spellings
		{"Returning Series", "returning"},
		{"Ended", "ended"},
		{"Canceled", "cancelled"}, //nolint:misspell // the catalog's show_status spelling
		{"In Production", "in_production"},
		{"Pilot", "in_production"},
		{"Planned", "upcoming"},
		// TVDB spellings
		{"Continuing", "returning"},
		{"Upcoming", "upcoming"},
		// Already-canonical values are stable
		{"returning", "returning"},
		{"ended", "ended"},
		{"cancelled", "cancelled"}, //nolint:misspell // the catalog's show_status spelling
		{"in_production", "in_production"},
		{"upcoming", "upcoming"},
		// Unknown values pass through lowercased instead of being dropped
		{"On Hiatus", "on hiatus"},
	}
	for _, tc := range cases {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
