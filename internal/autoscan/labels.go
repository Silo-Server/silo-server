package autoscan

import (
	"strings"
	"unicode/utf8"
)

// MaxSourceLabelLen bounds an operator-set source label in runes.
const MaxSourceLabelLen = 120

// NormalizeSourceLabel trims surrounding whitespace and caps the label at
// MaxSourceLabelLen runes without splitting a multi-byte rune. An all-whitespace
// label normalizes to "" (unset).
func NormalizeSourceLabel(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= MaxSourceLabelLen {
		return s
	}
	return string([]rune(s)[:MaxSourceLabelLen])
}
