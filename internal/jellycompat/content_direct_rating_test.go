package jellycompat

import "testing"

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
