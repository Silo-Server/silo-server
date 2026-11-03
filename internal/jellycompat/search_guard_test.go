package jellycompat

import "testing"

func TestAuxSearchTermTooShort(t *testing.T) {
	cases := []struct {
		term string
		want bool
	}{
		{"", false},       // empty is a browse, not a search
		{"   ", false},    // whitespace trims to empty -> browse
		{"ab", true},      // 2 chars -> too short (below the trigram floor)
		{"abc", false},    // 3 chars -> allowed (trigram-eligible)
		{"  abc ", false}, // trims to 3 chars -> allowed
		{"日本", true},      // 2 runes -> too short (rune-counted, not bytes)
		{"日本語", false},    // 3 runes -> allowed
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
		{0, auxSearchMaxResults},  // unset collapses to cap
		{20, 20},                  // exactly cap preserved
		{21, auxSearchMaxResults}, // over cap clamped
	}
	for _, c := range cases {
		if got := clampAuxSearchLimit(c.in); got != c.want {
			t.Errorf("clampAuxSearchLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
