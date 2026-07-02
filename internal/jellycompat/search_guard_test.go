package jellycompat

import "testing"

func TestAuxSearchTermTooShort(t *testing.T) {
	cases := []struct {
		term string
		want bool
	}{
		{"", false},        // empty is a browse, not a search
		{"   ", false},     // whitespace trims to empty -> browse
		{"a", true},        // 1 char
		{"ab", true},       // 2 chars -> too short (below the trigram floor)
		{"abc", false},     // 3 chars -> allowed (trigram-eligible)
		{"  abc ", false},  // trims to 3 chars -> allowed
		{"abcd", false},    // 4 chars -> allowed
		{"america", false}, // long term -> allowed
		{"日本", true},       // 2 runes -> too short (rune-counted, not bytes)
		{"日本語", false},     // 3 runes -> allowed
	}
	for _, c := range cases {
		if got := auxSearchTermTooShort(c.term); got != c.want {
			t.Errorf("auxSearchTermTooShort(%q) = %v, want %v", c.term, got, c.want)
		}
	}
}

func TestClampAuxSearchLimit(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, auxSearchMaxResults},    // unset collapses to cap
		{-5, auxSearchMaxResults},   // negative collapses to cap
		{5, 5},                      // under cap preserved
		{20, 20},                    // exactly cap preserved
		{21, auxSearchMaxResults},   // over cap clamped
		{1000, auxSearchMaxResults}, // way over cap clamped
	}
	for _, c := range cases {
		if got := clampAuxSearchLimit(c.in); got != c.want {
			t.Errorf("clampAuxSearchLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
