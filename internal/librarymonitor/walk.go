package librarymonitor

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// walkVisitor receives the directories of a walk. Both backends record
// directories through it, so they skip exactly the same folders.
type walkVisitor struct {
	// enter records dir before it is listed, so a file created while the
	// listing runs still produces an event. link reports that the walk
	// reached dir through a symlink to a directory. It returns false to
	// leave dir unlisted: the backend already records it for this root under
	// another path (a symlink alias), or it could not be recorded. A non-nil
	// error aborts the walk.
	enter func(dir string, link bool) (bool, error)
	// listed reports, once dir was listed, whether its ignore files exclude
	// it and everything below it, and returns whether the walk treats it as
	// skipped: the backend may know a newer answer (a marker changed after
	// the listing). A skipped directory stays recorded as a boundary, so
	// adding or removing its .nomedia or .ignore is seen, but the walk does
	// not descend into it.
	listed func(dir string, skipped bool) bool
	// file, when set, receives every file (or symlink to a file) in a
	// listed directory that is not skipped.
	file func(path string)
}

// walkTree records dir and every directory below it that the scanner would
// enter: it lists each directory in name order, skips directories by the
// fixed ignore list, the scanner's ignored names, and their .nomedia or
// pattern-less .ignore files (a directory skipped that way is still recorded
// itself, see walkVisitor.listed), and follows symlinked directories as the
// scanner does. Paths stay logical (built from dir), while loops through
// symlinks are cut by the physical directory, like the scanner's walk. A
// network filesystem mounted below dir is not entered: it is unsupported for
// the same reasons as a network root, and a hung mount would stall the walk.
//
// The same holds for a symlink whose target is on such a filesystem: it is
// not followed, and its logical path is reported to the collector ctx
// carries (see withSkippedPaths), if any.
//
// Unreadable directories stay recorded but are not descended into; a
// canceled ctx aborts the walk.
func walkTree(ctx context.Context, dir string, v walkVisitor) error {
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		physical = dir
	}
	w := &treeWalk{
		visited:     make(map[string]struct{}),
		unsupported: unsupportedMountPoints(),
		skipped:     skippedPathsFrom(ctx),
		v:           v,
	}
	if w.onUnsupportedMount(filepath.Clean(physical)) {
		// A runtime walk of a folder that resolves onto a network
		// filesystem (roots on one are rejected before any walk).
		w.skipped.addNetwork(dir)
		return nil
	}
	info, err := os.Lstat(dir)
	link := err == nil && info.Mode()&os.ModeSymlink != 0
	return w.dir(ctx, dir, filepath.Clean(physical), link)
}

type treeWalk struct {
	visited     map[string]struct{}
	unsupported map[string]bool
	skipped     *skippedPaths
	v           walkVisitor
}

// onUnsupportedMount reports whether physical is on a filesystem the walk
// skips.
func (w *treeWalk) onUnsupportedMount(physical string) bool {
	return onUnsupportedMount(w.unsupported, physical)
}

// skippedPaths collects what one walk left unmonitored below a root and the
// status names: symlinks onto network filesystems, and directories the
// backend could not watch because Silo may not read them.
type skippedPaths struct {
	mu         sync.Mutex
	network    map[string]struct{}
	unreadable map[string]struct{}
}

type skippedPathsKey struct{}

// withSkippedPaths returns a context whose walks report skipped paths to the
// returned collector.
func withSkippedPaths(ctx context.Context) (context.Context, *skippedPaths) {
	sp := &skippedPaths{network: make(map[string]struct{}), unreadable: make(map[string]struct{})}
	return context.WithValue(ctx, skippedPathsKey{}, sp), sp
}

func skippedPathsFrom(ctx context.Context) *skippedPaths {
	sp, _ := ctx.Value(skippedPathsKey{}).(*skippedPaths)
	return sp
}

func (sp *skippedPaths) addNetwork(path string) {
	if sp != nil {
		sp.add(sp.network, path)
	}
}

func (sp *skippedPaths) addUnreadable(path string) {
	if sp != nil {
		sp.add(sp.unreadable, path)
	}
}

func (sp *skippedPaths) add(set map[string]struct{}, path string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	set[path] = struct{}{}
}

// lists returns the collected paths of each kind, in order.
func (sp *skippedPaths) lists() (network, unreadable []string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sortedKeys(sp.network), sortedKeys(sp.unreadable)
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (w *treeWalk) dir(ctx context.Context, logical, physical string, link bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, seen := w.visited[physical]; seen {
		return nil
	}
	w.visited[physical] = struct{}{}

	ok, err := w.v.enter(logical, link)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	entries, err := os.ReadDir(logical)
	if err != nil {
		// Unreadable: nothing below it can be recorded. Not a walk failure.
		return nil //nolint:nilerr // an unreadable directory must not abort the walk.
	}
	skipped := dirSkipped(logical, entries)
	if w.v.listed != nil {
		skipped = w.v.listed(logical, skipped)
	}
	if skipped {
		return nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if ignoredDir(name) {
			continue
		}
		childLogical := filepath.Join(logical, name)
		childPhysical := filepath.Join(physical, name)
		childLink := false
		switch {
		case entry.IsDir():
		case entry.Type()&os.ModeSymlink != 0:
			childLink = true
			// Decide from the link text first, so a link into a hung
			// network mount is never resolved or stat'ed.
			if linkOntoUnsupportedMount(w.unsupported, childLogical, physical) {
				w.skipped.addNetwork(childLogical)
				continue
			}
			resolved, err := filepath.EvalSymlinks(childLogical)
			if err != nil {
				continue
			}
			info, err := os.Stat(resolved)
			if err != nil {
				continue
			}
			if !info.IsDir() {
				w.foundFile(childLogical)
				continue
			}
			childPhysical = filepath.Clean(resolved)
			if w.onUnsupportedMount(childPhysical) {
				// Reached through another link.
				w.skipped.addNetwork(childLogical)
				continue
			}
		default:
			if entry.Type().IsRegular() {
				w.foundFile(childLogical)
			}
			continue
		}
		if w.onUnsupportedMount(childPhysical) {
			continue
		}
		if err := w.dir(ctx, childLogical, childPhysical, childLink); err != nil {
			return err
		}
	}
	return nil
}

func (w *treeWalk) foundFile(path string) {
	if w.v.file != nil {
		w.v.file(path)
	}
}

// symlinkToDir reports whether path is a symlink to a directory, which the
// walk follows and records like a directory. Its events carry no directory
// flag, so the backends check.
//
// A link onto a filesystem the walk skips counts as not a directory: the link
// text is checked before the target is touched, so a new link into a hung
// network mount cannot block the backend's read loop, and the change is
// reported like a file for a scoped scan instead of being walked.
func symlinkToDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	mounts := unsupportedMountPoints()
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		parent = filepath.Dir(path)
	}
	if linkOntoUnsupportedMount(mounts, path, parent) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || onUnsupportedMount(mounts, resolved) {
		return false
	}
	target, err := os.Stat(resolved)
	return err == nil && target.IsDir()
}
