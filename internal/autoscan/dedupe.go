package autoscan

import "path/filepath"

// parentDirGroup is one parent directory and the distinct reported paths in it.
type parentDirGroup struct {
	Dir   string
	Paths []string
}

// groupByParentDir groups imported file paths by their parent directory, in
// first-seen order, dropping empties and repeated paths. A season's episodes
// in one folder collapse to one group.
func groupByParentDir(paths []string) []parentDirGroup {
	index := make(map[string]int)
	seenPaths := make(map[string]struct{})
	var out []parentDirGroup
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, dup := seenPaths[p]; dup {
			continue
		}
		seenPaths[p] = struct{}{}
		dir := filepath.Dir(p)
		i, ok := index[dir]
		if !ok {
			i = len(out)
			index[dir] = i
			out = append(out, parentDirGroup{Dir: dir})
		}
		out[i].Paths = append(out[i].Paths, p)
	}
	return out
}
