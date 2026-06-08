package autoscan

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeSourceLabel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trims surrounding whitespace", "  4K Movies  ", "4K Movies"},
		{"empty stays empty", "", ""},
		{"all whitespace becomes empty", "   ", ""},
		{"under cap unchanged", strings.Repeat("x", 120), strings.Repeat("x", 120)},
		{"over cap truncated to 120 runes", strings.Repeat("x", 200), strings.Repeat("x", 120)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeSourceLabel(c.in); got != c.want {
				t.Fatalf("NormalizeSourceLabel(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeSourceLabelRuneSafe(t *testing.T) {
	// 200 multi-byte runes must cap to 120 runes and stay valid UTF-8.
	got := NormalizeSourceLabel(strings.Repeat("é", 200))
	if utf8.RuneCountInString(got) != 120 {
		t.Fatalf("rune count = %d, want 120", utf8.RuneCountInString(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("result is not valid UTF-8")
	}
}
