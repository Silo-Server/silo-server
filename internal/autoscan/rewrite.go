package autoscan

import "strings"

// normalizeSeparators converts Windows backslash separators to forward slashes
// so paths from a Windows-hosted arr resolve on the Linux host. (filepath.ToSlash
// is a no-op on Linux, so the replacement is explicit.)
func normalizeSeparators(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

// applyRewrites returns path with the MOST-SPECIFIC matching prefix rewrite
// applied, or path unchanged when none match.
//
// "Most-specific" means the longest matching From wins, not the first one in the
// slice. A first-match strategy lets a broad rewrite (From="/data") shadow a
// nested, more-specific one (From="/data/media") when the broad rule happens to
// be listed first; the arr plugin review flagged exactly this. Selecting the
// longest matching prefix makes the result independent of rule ordering.
func applyRewrites(path string, rewrites []PathRewrite) string {
	bestIdx := -1
	bestLen := -1
	var bestTrimmed string
	for i, rw := range rewrites {
		// Normalize the stored From the SAME way coveredBy/normalizePath does
		// (backslash→slash, collapse '//', strip trailing '/') so a Windows-style
		// or dup-slash rewrite that suggest-time reports as "covered" actually
		// matches at poll time. Without this, a From like `D:\data\tv` would be
		// "covered" in the suggester yet never match the normalized incoming path.
		trimmed := normalizePath(rw.From)
		if trimmed == "" {
			continue
		}
		// Match only at a path-segment boundary: exact, or prefix followed by '/'.
		// This prevents From="/data/media" from rewriting "/data/media2/x".
		if path == trimmed || strings.HasPrefix(path, trimmed+"/") {
			if len(trimmed) > bestLen {
				bestLen = len(trimmed)
				bestIdx = i
				bestTrimmed = trimmed
			}
		}
	}
	if bestIdx < 0 {
		return path
	}
	return strings.TrimSpace(rewrites[bestIdx].To) + strings.TrimPrefix(path, bestTrimmed)
}
