package autoscan

import "strings"

// applyRewrites returns path with the first matching prefix rewrite applied,
// or path unchanged when none match.
func applyRewrites(path string, rewrites []PathRewrite) string {
	for _, rw := range rewrites {
		from := strings.TrimSpace(rw.From)
		if from == "" {
			continue
		}
		if strings.HasPrefix(path, from) {
			return strings.TrimSpace(rw.To) + strings.TrimPrefix(path, from)
		}
	}
	return path
}
