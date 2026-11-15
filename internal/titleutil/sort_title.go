package titleutil

import (
	"strings"
	"unicode"
)

// DeriveDefaultSortTitle returns a Plex-style sort title when title begins with
// a leading English article (The, A, An). The article moves to a comma suffix
// so "The Office" becomes "Office, The". Returns "" when no derivation applies.
func DeriveDefaultSortTitle(title string) string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return ""
	}

	spaceIdx := strings.IndexFunc(trimmed, unicode.IsSpace)
	var firstWord, rest string
	if spaceIdx < 0 {
		firstWord = trimmed
		rest = ""
	} else {
		firstWord = trimmed[:spaceIdx]
		rest = strings.TrimSpace(trimmed[spaceIdx:])
	}

	switch strings.ToLower(firstWord) {
	case "the":
		if rest == "" {
			return ""
		}
		return rest + ", The"
	case "a":
		if rest == "" {
			return ""
		}
		return rest + ", A"
	case "an":
		if rest == "" {
			return ""
		}
		return rest + ", An"
	default:
		return ""
	}
}

// IsDefaultSortTitle reports whether sortTitle has the form
// DeriveDefaultSortTitle writes: text followed by ", The", ", A" or ", An".
// Such a sort title was derived from some title; any other value was set by
// someone.
func IsDefaultSortTitle(sortTitle string) bool {
	for _, suffix := range []string{", The", ", A", ", An"} {
		if rest, ok := strings.CutSuffix(sortTitle, suffix); ok && strings.TrimSpace(rest) != "" {
			return true
		}
	}
	return false
}
