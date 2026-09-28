package librarymonitor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"
)

var errBackendClosed = errors.New("librarymonitor: backend closed")

// errRootReleased is AddRoot's result when the backend released the root
// while its walk ran (the root was lost, or recording a directory created
// under it failed). Reporting success would claim coverage the backend no
// longer holds.
var errRootReleased = errors.New("the folder was released while Silo recorded it; Silo retries")

// BackendInotify is the backend name reported in the status.
const BackendInotify = "inotify"

// EventKind is the common form of a kernel filesystem event. The backend
// translates its native events into these kinds; everything after it (quiet
// window, classification, scan resolution) works on them.
type EventKind int

const (
	// EventCreate: Name was created in Dir. For a directory (IsDir), the
	// backend has already recorded the new subtree before emitting it.
	EventCreate EventKind = iota + 1
	// EventCloseWrite: a writer closed Name in Dir after writing.
	EventCloseWrite
	// EventMovedFrom: Name left Dir and did not reappear inside the monitored
	// tree (moved out). For a directory, the backend has already dropped
	// everything it recorded under it.
	EventMovedFrom
	// EventMovedTo: Name arrived in Dir from outside the monitored tree
	// (moved in). For a directory, the backend has already recorded the new
	// subtree.
	EventMovedTo
	// EventRename: a move within the monitored tree, from OldDir/OldName to
	// Dir/Name. For a directory, the backend has already rewritten the paths
	// it records for the moved subtree.
	EventRename
	// EventDelete: Name was removed from Dir. For a directory, the backend
	// has already dropped everything it recorded under it.
	EventDelete
	// EventOverflow: the kernel dropped events. Dir and Name are empty.
	EventOverflow
	// EventRootLost: Root was deleted, moved, or unmounted. The backend has
	// already released everything it recorded for Root.
	EventRootLost
	// EventLimitReached: recording a new directory under Root failed because
	// a kernel limit was reached. The backend has already released everything
	// it recorded for Root. Err is a *WatchLimitError.
	EventLimitReached
	// EventRewalk: Root must be walked again because kernel state below it
	// went away without per-directory events: a filesystem mounted inside it
	// was unmounted. Nothing is queued; changes made in the gap are the
	// nightly scan's job.
	EventRewalk
	// EventRescanRoot: Root's contents need a whole-library scan: ignore
	// rules on the library folder itself changed, and only a library scan
	// applies them to entries directly in it.
	EventRescanRoot
)

func (k EventKind) String() string {
	switch k {
	case EventCreate:
		return "create"
	case EventCloseWrite:
		return "close_write"
	case EventMovedFrom:
		return "moved_from"
	case EventMovedTo:
		return "moved_to"
	case EventRename:
		return "rename"
	case EventDelete:
		return "delete"
	case EventOverflow:
		return "overflow"
	case EventRootLost:
		return "root_lost"
	case EventLimitReached:
		return "limit_reached"
	case EventRewalk:
		return "rewalk"
	case EventRescanRoot:
		return "rescan_root"
	default:
		return fmt.Sprintf("EventKind(%d)", int(k))
	}
}

// Event is one filesystem change in the common form. Paths are logical: they
// are built from the configured library root string, even when that root (or
// a directory below it) is a symlink, because the scan resolver matches
// configured paths.
type Event struct {
	Kind  EventKind
	Dir   string
	Name  string
	IsDir bool

	// OldDir and OldName are set for EventRename.
	OldDir  string
	OldName string

	// Found is set on an EventCreate for a file the backend found while
	// walking a directory that was just created, rather than one the kernel
	// reported: the file may have been written before the directory's watch
	// existed, so its close-write may never arrive.
	Found bool

	// Root is set for EventRootLost, EventLimitReached, and EventRewalk.
	Root string
	// Err is set for EventLimitReached.
	Err error
}

// Backend records the directories of library roots with one kernel
// notification mechanism and turns its events into the common Event form.
//
// A backend is shared by every root that uses it and must be safe for
// concurrent use: AddRoot runs in a per-root goroutine while the backend's own
// reader delivers events.
type Backend interface {
	// Name is the backend's status name, BackendInotify.
	Name() string
	// AddRoot records root and every directory below it that the scanner
	// would enter (see walkTree), and keeps that set current as directories
	// are created, moved, and deleted. It blocks until the walk completes.
	// Calling it again for a recorded root walks again: directories created
	// since are recorded and directories that no longer exist are dropped
	// (used after an overflow). If the backend releases root while the walk
	// runs, AddRoot returns an error (a *WatchLimitError, or errRootReleased)
	// instead of success. On error the backend may still hold part of the
	// walk; callers release it with RemoveRoot.
	AddRoot(ctx context.Context, root string) error
	// RemoveRoot releases everything recorded for root. Directories another
	// root still covers stay recorded. Unknown roots are a no-op.
	RemoveRoot(root string)
	// Directories reports how many directories are recorded for root. It is
	// live during AddRoot, which the walk ordering uses to detect progress.
	Directories(root string) int
	// Events delivers events until Close, then is closed.
	Events() <-chan Event
	// Close releases the kernel resources and closes Events.
	Close() error
}

// renameSide is one half of a move within the monitored tree: the logical
// parent directories, the entry name, whether it is (or was) a recorded
// directory, and whether its name is ignored.
type renameSide struct {
	dirs    []string
	name    string
	isDir   bool
	ignored bool
}

// emitRename reports a move within the monitored tree. A pair with one
// parent on each side and the same kind becomes one EventRename; otherwise
// the halves are reported as a move out and a move in. An ignored side is
// not reported.
func emitRename(emit func(Event), from, to renameSide) {
	if !from.ignored && !to.ignored && from.isDir == to.isDir && len(from.dirs) == 1 && len(to.dirs) == 1 {
		emit(Event{
			Kind: EventRename, Dir: to.dirs[0], Name: to.name, IsDir: to.isDir,
			OldDir: from.dirs[0], OldName: from.name,
		})
		return
	}
	if !from.ignored {
		for _, dir := range from.dirs {
			emit(Event{Kind: EventMovedFrom, Dir: dir, Name: from.name, IsDir: from.isDir})
		}
	}
	if !to.ignored {
		for _, dir := range to.dirs {
			emit(Event{Kind: EventMovedTo, Dir: dir, Name: to.name, IsDir: to.isDir})
		}
	}
}

// childDirs indexes recorded directory paths by their parent path, so a
// backend can drop a recorded subtree without scanning every path.
type childDirs map[string]map[string]struct{}

func (c childDirs) add(path string) {
	parent := filepath.Dir(path)
	if parent == path {
		return // the filesystem root is nobody's child, and not its own
	}
	if c[parent] == nil {
		c[parent] = make(map[string]struct{})
	}
	c[parent][path] = struct{}{}
}

func (c childDirs) remove(path string) {
	parent := filepath.Dir(path)
	if set := c[parent]; set != nil {
		delete(set, path)
		if len(set) == 0 {
			delete(c, parent)
		}
	}
}

// of returns the indexed paths directly below path.
func (c childDirs) of(path string) []string {
	out := make([]string, 0, len(c[path]))
	for child := range c[path] {
		out = append(out, child)
	}
	return out
}

// subtree returns path and every indexed path below it.
func (c childDirs) subtree(path string) []string {
	stack := []string{path}
	var all []string
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		all = append(all, p)
		for child := range c[p] {
			stack = append(stack, child)
		}
	}
	return all
}

// maxPendingMoves bounds how many moved-from halves a backend keeps waiting
// for their partner; the oldest beyond it count as moves out.
const maxPendingMoves = 1024

// expireMoves reports, oldest first, the pending moves whose partner did not
// arrive within wait as moves out, and returns the rest. The kernel queues
// both halves of a rename together, so a partner that is not there by then
// never comes. Moves beyond maxPendingMoves are reported as well. moves must
// be in arrival order.
func expireMoves[M any](moves []M, now time.Time, wait time.Duration, at func(M) time.Time, movedOut func([]M)) []M {
	n := 0
	for n < len(moves) && (len(moves)-n > maxPendingMoves || now.Sub(at(moves[n])) >= wait) {
		n++
	}
	if n == 0 {
		return moves
	}
	movedOut(moves[:n])
	return moves[n:]
}

// reportFolder reports a directory whose contents need a scan, because its
// ignore markers no longer exclude it or its ignore rules changed: one
// directory change for it, or for a library folder itself a whole-library
// scan (EventRescanRoot). A path change directly in a library folder resolves
// to a single-file scan, which does not apply the folder's own ignore rules.
func reportFolder(emit func(Event), dir string, isRoot bool) {
	if isRoot {
		emit(Event{Kind: EventRescanRoot, Root: dir})
		return
	}
	emit(Event{Kind: EventMovedTo, Dir: filepath.Dir(dir), Name: filepath.Base(dir), IsDir: true})
}

// eventName returns an entry name from a kernel event without the NUL
// padding the kernel appends.
func eventName(raw []byte) string {
	if i := bytes.IndexByte(raw, 0); i >= 0 {
		raw = raw[:i]
	}
	return string(raw)
}

// WatchLimitError reports that the kernel ran out of inotify watches while
// recording a root. The backend has released the root's watches, so the
// status never claims coverage with holes.
type WatchLimitError struct {
	// Limit is fs.inotify.max_user_watches when the limit was hit, or 0 when
	// it could not be read.
	Limit int
	// Directories is how many directories the root needs.
	Directories int
}

func (e *WatchLimitError) Error() string {
	return fmt.Sprintf("inotify watch limit reached (max_user_watches=%d, directories=%d)", e.Limit, e.Directories)
}

// BackendOptions carries what the backend needs from the monitor.
type BackendOptions struct {
	Logger *slog.Logger
}
