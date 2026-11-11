package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// A Jellyfin client's MaxOfficialRating can only narrow the profile's ceiling,
// never widen it, and is compared by age: a client cap of 13 under a PG-13
// profile (whose tier admits TV-14, age 14) must win.
func TestClampMaxContentRating(t *testing.T) {
	cases := []struct {
		existing, requested, want string
	}{
		{"", "", ""},
		{"", "PG", "PG"},
		{"PG-13", "", "PG-13"},
		{"PG-13", "PG", "PG"},
		{"PG", "PG-13", "PG"},
		{"PG-13", "13", "13"},
		{"PG-13", "FSK 16", "PG-13"},
		{"15", "TV-14", "TV-14"},
		// A client value with no usable age is ignored, not applied.
		{"PG-13", "banana", "PG-13"},
		{"PG-13", "NR", "PG-13"},
		{"PG-13", "  ", "PG-13"},
		// ...including against an unrestricted profile: a blank cap from a
		// client must never become a ceiling that hides the whole library.
		{"", "banana", ""},
		{"", "  ", ""},
		// A whitespace ceiling stored on the PROFILE is a different matter: it
		// is a set control that resolves to nothing, so it keeps blocking and
		// a client cap cannot lift it.
		{"  ", "PG-13", "  "},
	}
	for _, tc := range cases {
		if got := clampMaxContentRating(tc.existing, tc.requested); got != tc.want {
			t.Errorf("clampMaxContentRating(%q, %q) = %q, want %q", tc.existing, tc.requested, got, tc.want)
		}
	}
}

// A client that narrows the ceiling asked for less than the profile may see,
// so the profile's allowed titles stop passing it; a client that narrows
// nothing keeps them.
func TestClampMaturityLimitsDropsAllowancesWhenNarrowed(t *testing.T) {
	limits := access.MaturityLimits{MaxContentRating: "PG-13", AllowedTitleIDs: []string{"movie-r"}}
	for _, tc := range []struct {
		requested string
		keep      bool
	}{{"", true}, {"R", true}, {"banana", true}, {"G", false}} {
		got := clampMaturityLimits(limits, tc.requested)
		if kept := len(got.AllowedTitleIDs) > 0; kept != tc.keep {
			t.Errorf("MaxOfficialRating %q: allowances kept = %v, want %v", tc.requested, kept, tc.keep)
		}
	}
}
