package literaryworks

import (
	"strings"
	"unicode"
)

var leadingArticles = map[string]struct{}{
	"a":   {},
	"an":  {},
	"the": {},
}

func normalizeKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastSpace := true
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	parts := strings.Fields(b.String())
	filtered := parts[:0]
	for _, part := range parts {
		if _, ok := leadingArticles[part]; ok {
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(filtered, " ")
}

func personKey(names []string) string {
	for _, name := range names {
		if key := normalizeKey(name); key != "" {
			return key
		}
	}
	return ""
}
