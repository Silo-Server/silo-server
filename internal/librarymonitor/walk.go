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
	// it and everything below it. A skipped directory stays recorded as a
	// boundary, so adding or removing its .nomedia or .ignore is seen, but
	// the walk does not descend into it.
	listed func(dir string, skipped bool)
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
// carries (see withNetworkLinks), if any.
//
// Unreadable directories stay recorded but are not descended into; a
// canceled ctx aborts the walk.
func walkTree(ctx context.Context, dir string, v walkVisitor) error {
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		physical = dir
	}
	w := &treeWalk{
		visited:      make(map[string]struct{}),
		unsupported:  unsupportedMountPoints(),
		networkLinks: networkLinksFrom(ctx),
		v:            v,
	}
	if w.onUnsupportedMount(filepath.Clean(physical)) {
		// A runtime walk of a folder that resolves onto a network
		// filesystem (roots on one are rejected before any walk).
		w.networkLinks.add(dir)
		return nil
	}
	info, err := os.Lstat(dir)
	link := err == nil && info.Mode()&os.ModeSymlink != 0
	return w.dir(ctx, dir, filepath.Clean(physical), link)
}

type treeWalk struct {
	visited      map[string]struct{}
	unsupported  map[string]bool
	networkLinks *networkLinks
	v            walkVisitor
}

// onUnsupportedMount reports whether physical is on a filesystem the walk
// skips.
func (w *treeWalk) onUnsupportedMount(physical string) bool {
	return onUnsupportedMount(w.unsupported, physical)
}

// networkLinks collects the symlinks a walk did not follow because their
// targets are on network filesystems.
type networkLinks struct {
	mu    sync.Mutex
	paths map[string]struct{}
}

type networkLinksKey struct{}

// withNetworkLinks returns a context whose walks report skipped network
// symlinks to the returned collector.
func withNetworkLinks(ctx context.Context) (context.Context, *networkLinks) {
	links := &networkLinks{paths: make(map[string]struct{})}
	return context.WithValue(ctx, networkLinksKey{}, links), links
}

func networkLinksFrom(ctx context.Context) *networkLinks {
	links, _ := ctx.Value(networkLinksKey{}).(*networkLinks)
	return links
}

func (l *networkLinks) add(path string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths[path] = struct{}{}
}

// list returns the collected paths in order.
func (l *networkLinks) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.paths))
	for p := range l.paths {
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
		w.v.listed(logical, skipped)
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
				w.networkLinks.add(childLogical)
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
				w.networkLinks.add(childLogical)
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
