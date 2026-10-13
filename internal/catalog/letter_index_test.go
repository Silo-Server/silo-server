package catalog

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// translate() pairs the two fold strings character by character; a length
// mismatch would delete the unpaired initials instead of folding them.
func TestLetterFoldTablePairsEveryInitial(t *testing.T) {
	from, to := []rune(letterFoldFrom), []rune(letterFoldTo)
	if len(from) != len(to) {
		t.Fatalf("fold table pairs %d initials with %d letters", len(from), len(to))
	}
	for i, r := range from {
		if to[i] < 'a' || to[i] > 'z' {
			t.Errorf("%q folds to %q, not a base letter", r, to[i])
		}
		if strings.ContainsRune(string(from[:i]), r) {
			t.Errorf("%q is listed twice", r)
		}
	}
	if !utf8.ValidString(letterFoldFrom) {
		t.Fatal("fold table is not valid UTF-8")
	}
}
