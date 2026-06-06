package autoscan

import "path/filepath"

// uniqueParentDirs maps imported file paths to their distinct parent directories,
// dropping empties. A season's episodes in one folder collapse to one entry.
func uniqueParentDirs(paths []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		dir := filepath.Dir(p)
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}
