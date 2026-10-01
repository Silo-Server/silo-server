package scanner

import "testing"

func TestEscapeLikePattern(t *testing.T) {
	cases := map[string]string{
		"heat":          "heat",
		"100%_done":     `100\%\_done`,
		`C:\media\Heat`: `C:\\media\\Heat`,
		"plain (1995)":  "plain (1995)",
		`\%`:            `\\\%`,
	}
	for in, want := range cases {
		if got := escapeLikePattern(in); got != want {
			t.Errorf("escapeLikePattern(%q) = %q, want %q", in, got, want)
		}
	}
}
