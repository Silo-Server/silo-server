package autoscan

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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
