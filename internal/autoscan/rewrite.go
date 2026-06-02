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
		// Match only at a path-segment boundary: exact, or prefix followed by '/'.
		// This prevents From="/data/media" from rewriting "/data/media2/x".
		trimmed := strings.TrimSuffix(from, "/")
		if path == trimmed || strings.HasPrefix(path, trimmed+"/") {
			return strings.TrimSpace(rw.To) + strings.TrimPrefix(path, trimmed)
		}
	}
	return path
}
