package librarymonitor

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// changeKind is what a reported path means for scanning.
type changeKind int

const (
	// changeFile: a file is present and complete; resolved with Resolve.
	changeFile changeKind = iota + 1
	// changeDir: a directory appeared (created or moved in); one subtree
	// change covers everything in it. Resolved with Resolve.
	changeDir
	// changeVanishedFile: a file was deleted or moved out; resolved with
	// ResolveVanishedPath.
	changeVanishedFile
	// changeVanishedDir: a directory was deleted or moved out; resolved with
	// ResolveMissingSubtree.
	changeVanishedDir
)

func (k changeKind) String() string {
	switch k {
	case changeFile:
		return "file"
	case changeDir:
		return "dir"
	case changeVanishedFile:
		return "vanished_file"
	case changeVanishedDir:
		return "vanished_dir"
	default:
		return "unknown"
	}
}

// change is a path that is ready to resolve into a scan target.
type change struct {
	path string
	kind changeKind
	// attempts counts failed resolutions; one retry is allowed.
	attempts int
}

// fileState is what the tracker needs from lstat.
type fileState struct {
	mode  fs.FileMode
	nlink uint64
	size  int64
	mtime time.Time
}

func lstatFile(path string) (fileState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileState{}, err
	}
	return fileState{mode: info.Mode(), nlink: linkCount(info), size: info.Size(), mtime: info.ModTime()}, nil
}

// pendingPath is one tracked path waiting to be reported.
type pendingPath struct {
	kind     changeKind
	complete bool
	// fresh: the path was created (IN_CREATE) while pending, so it did not
	// exist before and nothing has scanned it. If it vanishes before it is
	// reported, there is nothing to scan or clean up.
	fresh bool
	// last is the latest event on the path, or for a new directory on
	// anything below it; the quiet window runs from here.
	last    time.Time
	created time.Time
	// found: a file a walk found in a new directory (Event.Found). Its
	// close-write may have happened before the directory was watched, so it
	// is polled for stability from the start, once per quiet window.
	found bool
	// Stability polling for a created file that never saw a close-write.
	nextPoll time.Time
	polled   bool
	size     int64
	mtime    time.Time
}

// tracker turns events into changes. A path is reported once it is complete
// and has been quiet for the quiet window; another event on it restarts the
// window. Complete means close-write, moved in, a hardlink (link count > 1),
// or a symlink. A created file with link count 1 is a copy in progress: it
// waits for its close-write, and after createdFallback without one it is
// stat'ed every stablePoll and reported once two checks agree on size and
// mtime.
//
// A new directory is reported as one subtree change once nothing below it is
// still incomplete; it absorbs the changes below it.
//
// A path created and then deleted or renamed away before it was reported
// (a downloader's or rsync's temp file, a folder renamed right after it was
// made) is dropped rather than reported as vanished. Only a create proves
// the path is new: a move in may have replaced an existing entry.
//
// The tracker is not safe for concurrent use; the monitor's event loop owns
// it.
type tracker struct {
	quiet           time.Duration
	createdFallback time.Duration
	stablePoll      time.Duration
	stat            func(string) (fileState, error)
	pending         map[string]*pendingPath
}

func newTracker(quiet, createdFallback, stablePoll time.Duration) *tracker {
	return &tracker{
		quiet:           quiet,
		createdFallback: createdFallback,
		stablePoll:      stablePoll,
		stat:            lstatFile,
		pending:         make(map[string]*pendingPath),
	}
}

// observe records one path event. Overflow, root, and limit events are the
// monitor's; they are ignored here.
func (t *tracker) observe(ev Event, now time.Time) {
	path := filepath.Join(ev.Dir, ev.Name)
	switch ev.Kind {
	case EventCreate:
		if ev.IsDir {
			t.created(path, changeDir, true, now)
			return
		}
		st, err := t.stat(path)
		if err != nil {
			// Already gone; its delete event follows. Record the create so
			// the delete is recognized as a temp file's.
			t.created(path, changeFile, false, now)
			return
		}
		switch {
		case st.mode.IsDir():
			t.created(path, changeDir, true, now)
		case st.mode&fs.ModeSymlink != 0:
			t.created(path, changeFile, true, now)
		case st.mode.IsRegular():
			// A hardlink arrives whole; link count 1 is a copy that has
			// only started.
			t.created(path, changeFile, st.nlink > 1, now)
			if p := t.pending[path]; ev.Found && !p.complete {
				p.found = true
			}
		}
	case EventCloseWrite:
		t.present(path, changeFile, true, now)
	case EventMovedTo:
		t.present(path, presentKind(ev.IsDir), true, now)
	case EventDelete, EventMovedFrom:
		t.vanished(path, ev.IsDir, now)
	case EventRename:
		oldPath := filepath.Join(ev.OldDir, ev.OldName)
		if ev.IsDir {
			t.rekey(oldPath, path)
		}
		t.vanished(oldPath, ev.IsDir, now)
		t.present(path, presentKind(ev.IsDir), true, now)
	}
}

func presentKind(isDir bool) changeKind {
	if isDir {
		return changeDir
	}
	return changeFile
}

// created records a path the kernel reported as created: new unless a
// change to the same path is already pending (a delete followed by a create
// replaced an existing entry).
func (t *tracker) created(path string, kind changeKind, complete bool, now time.Time) {
	fresh := t.pending[path] == nil
	t.present(path, kind, complete, now)
	if fresh {
		t.pending[path].fresh = true
	}
}

func (t *tracker) present(path string, kind changeKind, complete bool, now time.Time) {
	p := t.pending[path]
	if p == nil || p.kind != kind {
		fresh := p != nil && p.fresh && p.kind != changeVanishedFile && p.kind != changeVanishedDir
		t.pending[path] = &pendingPath{kind: kind, complete: complete, fresh: fresh, last: now, created: now}
	} else {
		p.last = now
		if complete {
			p.complete = true
		}
	}
	t.touchAncestors(path, now)
}

func (t *tracker) vanished(path string, isDir bool, now time.Time) {
	if p := t.pending[path]; p != nil && p.fresh {
		// Created and gone again before it was reported: nothing scanned
		// it, and nothing below a new directory existed before either.
		delete(t.pending, path)
		t.dropBelow(path)
		return
	}
	kind := changeVanishedFile
	if isDir {
		kind = changeVanishedDir
		// A vanished subtree scan covers everything that was below it.
		t.dropBelow(path)
	}
	t.pending[path] = &pendingPath{kind: kind, complete: true, last: now, created: now}
	t.touchAncestors(path, now)
}

// touchAncestors keeps a new directory quiet-window-open while anything below
// it changes, so a folder being filled is reported once, after it settles.
func (t *tracker) touchAncestors(path string, now time.Time) {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if p := t.pending[dir]; p != nil && p.kind == changeDir {
			p.last = now
		}
		if filepath.Dir(dir) == dir {
			return
		}
	}
}

// rekey moves pending paths below oldDir to newDir after a directory rename,
// so a copy in progress inside it keeps its state.
func (t *tracker) rekey(oldDir, newDir string) {
	for path, p := range t.pending {
		if rel, ok := below(path, oldDir); ok {
			delete(t.pending, path)
			t.pending[filepath.Join(newDir, rel)] = p
		}
	}
}

func (t *tracker) dropBelow(dir string) {
	for path := range t.pending {
		if _, ok := below(path, dir); ok {
			delete(t.pending, path)
		}
	}
}

// dropUnder forgets every pending path at or below root.
func (t *tracker) dropUnder(root string) {
	delete(t.pending, root)
	t.dropBelow(root)
}

// below reports whether path is strictly below dir, with the relative path.
func below(path, dir string) (string, bool) {
	prefix := dir
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	return path[len(prefix):], true
}

// ready removes and returns the changes due at now, parents before children.
func (t *tracker) ready(now time.Time) []change {
	t.pollIncomplete(now)

	var due []string
	for path, p := range t.pending {
		if p.complete && now.Sub(p.last) >= t.quiet {
			due = append(due, path)
		}
	}
	sort.Strings(due)

	var out []change
	var reportedDirs []string
	for _, path := range due {
		p := t.pending[path]
		if p == nil {
			continue // absorbed by a directory reported above
		}
		if underAny(path, reportedDirs) {
			delete(t.pending, path)
			continue
		}
		if t.newDirAbove(path) {
			continue // held until the new directory above reports it
		}
		if p.kind == changeDir && t.incompleteBelow(path) {
			continue
		}
		delete(t.pending, path)
		out = append(out, change{path: path, kind: p.kind})
		if p.kind == changeDir {
			reportedDirs = append(reportedDirs, path)
			t.dropBelow(path)
		}
	}
	return out
}

func underAny(path string, dirs []string) bool {
	for _, dir := range dirs {
		if _, ok := below(path, dir); ok {
			return true
		}
	}
	return false
}

// newDirAbove reports whether a new directory above path is still pending.
func (t *tracker) newDirAbove(path string) bool {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if p := t.pending[dir]; p != nil && p.kind == changeDir {
			return true
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}

func (t *tracker) incompleteBelow(dir string) bool {
	for path, p := range t.pending {
		if !p.complete {
			if _, ok := below(path, dir); ok {
				return true
			}
		}
	}
	return false
}

// pollIncomplete runs the stability check for created files that never saw
// a close-write.
func (t *tracker) pollIncomplete(now time.Time) {
	for path, p := range t.pending {
		wait, every := t.createdFallback, t.stablePoll
		if p.found {
			wait, every = 0, t.quiet
		}
		if p.complete || p.kind != changeFile || now.Sub(p.created) < wait || now.Before(p.nextPoll) {
			continue
		}
		st, err := t.stat(path)
		if os.IsNotExist(err) {
			delete(t.pending, path) // its delete event reports it
			continue
		}
		if err != nil {
			p.nextPoll = now.Add(t.stablePoll)
			continue
		}
		if p.polled && st.size == p.size && st.mtime.Equal(p.mtime) {
			p.complete = true
			// Unchanged across a whole poll interval is as quiet as the
			// quiet window; report now.
			p.last = now.Add(-t.quiet)
			continue
		}
		p.polled = true
		p.size = st.size
		p.mtime = st.mtime
		p.nextPoll = now.Add(every)
	}
}
