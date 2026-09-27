//go:build linux

package librarymonitor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// inotifyMask is the per-directory watch mask. There is deliberately no
// IN_MODIFY: a copy always ends in IN_CLOSE_WRITE, so per-write events would
// only add volume. The kernel adds IN_IGNORED, IN_UNMOUNT, and IN_Q_OVERFLOW
// on its own.
const inotifyMask = unix.IN_CREATE | unix.IN_CLOSE_WRITE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO |
	unix.IN_DELETE | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_ONLYDIR | unix.IN_EXCL_UNLINK

// inotifyMoveWait bounds how long an IN_MOVED_FROM that ends a read waits for
// its IN_MOVED_TO. The kernel queues both halves of a rename together, so
// only a read buffer boundary can split them.
const inotifyMoveWait = 20 * time.Millisecond

// inotifyReadBuffer holds many events per read; one event needs at most
// SizeofInotifyEvent+NAME_MAX+1 bytes.
const inotifyReadBuffer = 64 * 1024

// errWalkLimit aborts a runtime walk that hit the watch limit.
var errWalkLimit = errors.New("librarymonitor: watch limit reached")

// inotifyBackend is the unprivileged backend: one inotify instance for the
// process and one watch per recorded directory.
//
// A watch descriptor names a directory inode, which can be reachable under
// several logical paths (overlapping roots share paths; a symlink in one
// root can alias a directory of another). Each logical path records which
// roots cover it, so removing one root never drops a watch another root
// still needs, and an event on a watch is reported once per logical path.
type inotifyBackend struct {
	fd      int
	file    *os.File
	log     *slog.Logger
	hooks   inotifyHooks
	events  chan Event
	ctx     context.Context
	cancel  context.CancelFunc
	closing chan struct{}
	done    chan struct{}
	once    sync.Once
	// fdMu keeps the descriptor open while inotify_add_watch runs. The
	// call resolves a path, which can block on a hung mount, so it runs
	// outside mu: status reads of the maps must never wait on a mount.
	fdMu sync.RWMutex

	mu       sync.Mutex
	closed   bool
	byWD     map[int]*inotifyWatch
	byPath   map[string]*inotifyDir
	children childDirs
	roots    map[string]*inotifyRoot

	// unmounted collects, during one read, the roots with a filesystem
	// unmounted below them. Only the read loop uses it.
	unmounted map[string]struct{}
}

type inotifyWatch struct {
	wd    int
	paths map[string]struct{}
}

type inotifyDir struct {
	wd int
	// roots maps each covering root to the walk generation that last
	// recorded this path for it; a re-walk drops paths it did not reach.
	roots map[*inotifyRoot]uint64
}

type inotifyRoot struct {
	path string
	dirs int
	gen  uint64
	// limitErr is set when a runtime walk hit the watch limit and released
	// the root, so an AddRoot still walking it reports the limit.
	limitErr *WatchLimitError
}

// inotifyParent is one logical path of a watched directory, with its roots.
type inotifyParent struct {
	path  string
	roots []*inotifyRoot
}

// inotifyMove is an IN_MOVED_FROM waiting for its IN_MOVED_TO.
type inotifyMove struct {
	cookie  uint32
	name    string
	isDir   bool
	parents []inotifyParent
}

func newInotifyBackend(opts BackendOptions, hooks inotifyHooks) (Backend, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("inotify_init1: %w", err)
	}
	if hooks.addWatch == nil {
		hooks.addWatch = unix.InotifyAddWatch
	}
	if hooks.maxUserWatches == nil {
		hooks.maxUserWatches = readMaxUserWatches
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &inotifyBackend{
		fd: fd,
		// A non-blocking descriptor makes the file pollable, so Close
		// unblocks Read and read deadlines work.
		file:     os.NewFile(uintptr(fd), "inotify"),
		log:      logger,
		hooks:    hooks,
		events:   make(chan Event, 1024),
		ctx:      ctx,
		cancel:   cancel,
		closing:  make(chan struct{}),
		done:     make(chan struct{}),
		byWD:     make(map[int]*inotifyWatch),
		byPath:   make(map[string]*inotifyDir),
		children: make(childDirs),
		roots:    make(map[string]*inotifyRoot),

		unmounted: make(map[string]struct{}),
	}
	go b.readLoop()
	return b, nil
}

func (b *inotifyBackend) Name() string { return BackendInotify }

func (b *inotifyBackend) Events() <-chan Event { return b.events }

func (b *inotifyBackend) Close() error {
	var err error
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		b.cancel()
		close(b.closing)
		b.fdMu.Lock()
		err = b.file.Close()
		b.fdMu.Unlock()
		<-b.done
	})
	return err
}

func (b *inotifyBackend) Directories(root string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.roots[root]; r != nil {
		return r.dirs
	}
	return 0
}

func (b *inotifyBackend) AddRoot(ctx context.Context, root string) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errBackendClosed
	}
	r := b.roots[root]
	if r == nil {
		r = &inotifyRoot{path: root}
		b.roots[root] = r
	}
	r.gen++
	gen := r.gen
	b.mu.Unlock()

	// After ENOSPC the walk keeps going without adding watches, only to
	// count the directories the root needs for the status detail.
	total := 0
	limited := false
	err := walkTree(ctx, root, walkVisitor{
		enter: func(dir string) (bool, error) {
			total++
			if limited {
				return true, nil
			}
			ok, err := b.register(dir, []*inotifyRoot{r})
			if errors.Is(err, unix.ENOSPC) {
				limited = true
				return true, nil
			}
			if err != nil {
				return false, err
			}
			if !ok {
				total--
			}
			return ok, nil
		},
		skip: func(dir string) {
			total--
			if !limited {
				b.unregister(dir, r)
			}
		},
	})
	if err != nil {
		return err
	}
	if limited {
		b.RemoveRoot(root)
		return &WatchLimitError{Limit: b.hooks.maxUserWatches(), Directories: total}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.roots[root] != r {
		// Released while the walk ran: the root was lost, or a directory
		// created under it hit the watch limit.
		if r.limitErr != nil {
			return &WatchLimitError{Limit: r.limitErr.Limit, Directories: max(r.limitErr.Directories, total)}
		}
		return errRootReleased
	}
	for path, d := range b.byPath {
		if g, ok := d.roots[r]; ok && g < gen {
			b.removeRootFromPathLocked(path, d, r)
		}
	}
	return nil
}

func (b *inotifyBackend) RemoveRoot(root string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.roots[root]
	if r == nil {
		return
	}
	delete(b.roots, root)
	for path, d := range b.byPath {
		if _, ok := d.roots[r]; ok {
			b.removeRootFromPathLocked(path, d, r)
		}
	}
}

// register adds a watch for dir and records it for roots. It reports whether
// dir was recorded for any root: false when dir vanished, is not a
// directory, cannot be watched, or is an alias of a directory already
// recorded for the same root under another path.
func (b *inotifyBackend) register(dir string, roots []*inotifyRoot) (bool, error) {
	wd, err := b.addWatch(dir)
	if err != nil {
		if errors.Is(err, unix.ENOSPC) || errors.Is(err, errBackendClosed) {
			return false, err
		}
		return false, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false, errBackendClosed
	}
	if d := b.byPath[dir]; d != nil && d.wd != wd {
		// The path now names a different directory than the one recorded.
		b.dropSubtreeLocked(dir, b.rmWatchLocked)
	}
	w := b.byWD[wd]
	if w == nil {
		w = &inotifyWatch{wd: wd, paths: make(map[string]struct{})}
		b.byWD[wd] = w
	}
	d := b.byPath[dir]
	recorded := false
	for _, r := range roots {
		if b.roots[r.path] != r {
			continue // removed while the walk ran
		}
		if d == nil && b.aliasLocked(w, r) {
			continue
		}
		if d == nil {
			d = &inotifyDir{wd: wd, roots: make(map[*inotifyRoot]uint64)}
			b.byPath[dir] = d
			w.paths[dir] = struct{}{}
			b.children.add(dir)
		}
		if _, ok := d.roots[r]; !ok {
			r.dirs++
		}
		d.roots[r] = r.gen
		recorded = true
	}
	if len(w.paths) == 0 {
		delete(b.byWD, wd)
		b.rmWatchLocked(wd)
	}
	return recorded, nil
}

func (b *inotifyBackend) addWatch(dir string) (int, error) {
	b.fdMu.RLock()
	defer b.fdMu.RUnlock()
	if b.isClosed() {
		return -1, errBackendClosed
	}
	return b.hooks.addWatch(b.fd, dir, inotifyMask)
}

func (b *inotifyBackend) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// aliasLocked reports whether watch w is already recorded for r under some
// logical path, which makes another path to it a symlink alias.
func (b *inotifyBackend) aliasLocked(w *inotifyWatch, r *inotifyRoot) bool {
	for p := range w.paths {
		if d := b.byPath[p]; d != nil {
			if _, ok := d.roots[r]; ok {
				return true
			}
		}
	}
	return false
}

func (b *inotifyBackend) unregister(dir string, r *inotifyRoot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d := b.byPath[dir]; d != nil {
		if _, ok := d.roots[r]; ok {
			b.removeRootFromPathLocked(dir, d, r)
		}
	}
}

func (b *inotifyBackend) removeRootFromPathLocked(path string, d *inotifyDir, r *inotifyRoot) {
	delete(d.roots, r)
	r.dirs--
	if len(d.roots) == 0 {
		b.dropPathLocked(path, b.rmWatchLocked)
	}
}

// dropPathLocked forgets one logical path. rm is called for a watch left
// with no path; pass a no-op when the kernel already removed it.
func (b *inotifyBackend) dropPathLocked(path string, rm func(wd int)) {
	d := b.byPath[path]
	if d == nil {
		return
	}
	for r := range d.roots {
		r.dirs--
	}
	delete(b.byPath, path)
	b.children.remove(path)
	if w := b.byWD[d.wd]; w != nil {
		delete(w.paths, path)
		if len(w.paths) == 0 {
			delete(b.byWD, d.wd)
			rm(d.wd)
		}
	}
}

// dropSubtreeLocked forgets path and every recorded path below it. It
// returns the roots whose own directory was among them: a root nested in
// another root's tree is lost through its parent's event, before (or
// instead of) an event on the root itself.
func (b *inotifyBackend) dropSubtreeLocked(path string, rm func(wd int)) []string {
	var lost []string
	for _, p := range b.children.subtree(path) {
		if d := b.byPath[p]; d != nil {
			for r := range d.roots {
				if r.path == p {
					lost = append(lost, r.path)
				}
			}
		}
		b.dropPathLocked(p, rm)
	}
	return lost
}

// loseRoots releases roots that disappeared and reports them.
func (b *inotifyBackend) loseRoots(roots []string) {
	sort.Strings(roots)
	for _, root := range roots {
		b.RemoveRoot(root)
		b.emit(Event{Kind: EventRootLost, Root: root})
	}
}

func (b *inotifyBackend) rmWatchLocked(wd int) {
	if b.closed {
		return
	}
	// EINVAL means the kernel already dropped it (deleted directory).
	_, _ = unix.InotifyRmWatch(b.fd, uint32(wd)) //nolint:gosec // watch descriptors are non-negative.
}

func (b *inotifyBackend) emit(ev Event) {
	select {
	case b.events <- ev:
	case <-b.closing:
	}
}

func (b *inotifyBackend) readLoop() {
	defer close(b.done)
	defer close(b.events)
	buf := make([]byte, inotifyReadBuffer)
	var moves []inotifyMove
	for {
		n, err := b.file.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				b.movedOut(moves)
				moves = nil
				_ = b.file.SetReadDeadline(time.Time{})
				continue
			}
			if !b.isClosed() {
				b.log.Error("librarymonitor: inotify read failed", "component", "librarymonitor", "err", err)
			}
			return
		}
		moves = b.process(buf[:n], moves)
		b.flushUnmounted()
		if len(moves) > 0 {
			if err := b.file.SetReadDeadline(time.Now().Add(inotifyMoveWait)); err != nil {
				b.movedOut(moves)
				moves = nil
			}
		} else {
			_ = b.file.SetReadDeadline(time.Time{})
		}
	}
}

func (b *inotifyBackend) process(buf []byte, moves []inotifyMove) []inotifyMove {
	for off := 0; off+unix.SizeofInotifyEvent <= len(buf); {
		wd := int(int32(binary.NativeEndian.Uint32(buf[off:]))) //nolint:gosec // the kernel writes wd as a signed 32-bit value.
		mask := binary.NativeEndian.Uint32(buf[off+4:])
		cookie := binary.NativeEndian.Uint32(buf[off+8:])
		nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
		start := off + unix.SizeofInotifyEvent
		end := start + nameLen
		if end > len(buf) {
			break
		}
		off = end
		moves = b.handle(wd, mask, cookie, eventName(buf[start:end]), moves)
	}
	return moves
}

func (b *inotifyBackend) handle(wd int, mask, cookie uint32, name string, moves []inotifyMove) []inotifyMove {
	if mask&unix.IN_Q_OVERFLOW != 0 {
		b.movedOut(moves)
		b.emit(Event{Kind: EventOverflow})
		return nil
	}
	if name == "" {
		b.handleSelf(wd, mask)
		return moves
	}
	parents := b.parentsOf(wd)
	if len(parents) == 0 {
		return moves
	}
	isDir := mask&unix.IN_ISDIR != 0
	switch {
	case mask&unix.IN_MOVED_FROM != 0:
		return append(moves, inotifyMove{cookie: cookie, name: name, isDir: isDir, parents: parents})
	case mask&unix.IN_MOVED_TO != 0:
		for i, mv := range moves {
			if mv.cookie == cookie {
				moves = append(moves[:i:i], moves[i+1:]...)
				b.renamed(mv, name, isDir, parents)
				return moves
			}
		}
		b.arrived(EventMovedTo, parents, name, isDir)
	case mask&unix.IN_CREATE != 0:
		b.arrived(EventCreate, parents, name, isDir)
	case mask&unix.IN_CLOSE_WRITE != 0:
		if !ignoredName(name) {
			for _, p := range parents {
				b.emit(Event{Kind: EventCloseWrite, Dir: p.path, Name: name})
			}
		}
	case mask&unix.IN_DELETE != 0:
		b.left(EventDelete, parents, name, isDir)
	}
	return moves
}

// parentsOf snapshots the logical paths of watch wd, with their roots.
func (b *inotifyBackend) parentsOf(wd int) []inotifyParent {
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.byWD[wd]
	if w == nil {
		return nil
	}
	parents := make([]inotifyParent, 0, len(w.paths))
	for p := range w.paths {
		d := b.byPath[p]
		if d == nil {
			continue
		}
		roots := make([]*inotifyRoot, 0, len(d.roots))
		for r := range d.roots {
			roots = append(roots, r)
		}
		parents = append(parents, inotifyParent{path: p, roots: roots})
	}
	sort.Slice(parents, func(i, j int) bool { return parents[i].path < parents[j].path })
	return parents
}

// arrived handles a created or moved-in entry. A new directory (or a symlink
// to one, which the scanner follows) is recorded, then listed by the walk,
// before the event is emitted.
func (b *inotifyBackend) arrived(kind EventKind, parents []inotifyParent, name string, isDir bool) {
	if ignoredName(name) {
		return
	}
	if !isDir {
		isDir = symlinkToDir(filepath.Join(parents[0].path, name))
	}
	if isDir {
		if ignoredDir(name) {
			return
		}
		for _, p := range parents {
			b.walkRegister(filepath.Join(p.path, name), p.roots)
		}
	}
	for _, p := range parents {
		b.emit(Event{Kind: kind, Dir: p.path, Name: name, IsDir: isDir})
	}
}

// recordedDir reports whether name is recorded as a directory under one of
// parents. A followed symlink to a directory is recorded, but its events
// carry no IN_ISDIR.
func (b *inotifyBackend) recordedDir(parents []inotifyParent, name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range parents {
		if b.byPath[filepath.Join(p.path, name)] != nil {
			return true
		}
	}
	return false
}

// left handles a deleted or moved-out entry.
func (b *inotifyBackend) left(kind EventKind, parents []inotifyParent, name string, isDir bool) {
	isDir = isDir || b.recordedDir(parents, name)
	if isDir {
		// A directory moved out of the tree keeps its watches on the moved
		// inodes; drop them so no event arrives under a stale path.
		var lost []string
		b.mu.Lock()
		for _, p := range parents {
			lost = append(lost, b.dropSubtreeLocked(filepath.Join(p.path, name), b.rmWatchLocked)...)
		}
		b.mu.Unlock()
		b.loseRoots(lost)
	}
	if ignoredName(name) || (isDir && ignoredDir(name)) {
		return
	}
	for _, p := range parents {
		b.emit(Event{Kind: kind, Dir: p.path, Name: name, IsDir: isDir})
	}
}

func (b *inotifyBackend) movedOut(moves []inotifyMove) {
	for _, mv := range moves {
		b.left(EventMovedFrom, mv.parents, mv.name, mv.isDir)
	}
}

// renamed handles a move pair within the tree. A moved directory's paths are
// rewritten by re-recording its subtree under the new name: its watches are
// detached from the old paths but kept in the kernel, the walk re-adds them
// (inotify returns the same descriptors) under the new paths and roots, and
// only watches the new location no longer covers are removed.
//
// A followed symlink to a directory is handled as a directory on each side:
// the kernel flags neither half, so the old side is a directory when it was
// recorded as one, and the new side when it still resolves to one.
func (b *inotifyBackend) renamed(from inotifyMove, name string, isDir bool, parents []inotifyParent) {
	oldDir := isDir || b.recordedDir(from.parents, from.name)
	newDir := isDir || symlinkToDir(filepath.Join(parents[0].path, name))
	oldIgnored := ignoredName(from.name) || (oldDir && ignoredDir(from.name))
	newIgnored := ignoredName(name) || (newDir && ignoredDir(name))
	var detached []int
	if oldDir {
		var lost []string
		b.mu.Lock()
		for _, p := range from.parents {
			lost = append(lost, b.dropSubtreeLocked(filepath.Join(p.path, from.name), func(wd int) { detached = append(detached, wd) })...)
		}
		b.mu.Unlock()
		// A root inside the moved directory is gone from its configured
		// path; release it before the walk re-records the new location.
		b.loseRoots(lost)
	}
	if newDir && !newIgnored {
		for _, p := range parents {
			b.walkRegister(filepath.Join(p.path, name), p.roots)
		}
	}
	if len(detached) > 0 {
		b.mu.Lock()
		for _, wd := range detached {
			if b.byWD[wd] == nil {
				b.rmWatchLocked(wd)
			}
		}
		b.mu.Unlock()
	}
	emitRename(b.emit,
		renameSide{dirs: inotifyPaths(from.parents), name: from.name, isDir: oldDir, ignored: oldIgnored},
		renameSide{dirs: inotifyPaths(parents), name: name, isDir: newDir, ignored: newIgnored})
}

func inotifyPaths(parents []inotifyParent) []string {
	paths := make([]string, len(parents))
	for i, p := range parents {
		paths[i] = p.path
	}
	return paths
}

// walkRegister records a subtree that appeared at runtime for roots. Hitting
// the watch limit releases every affected root, so no status claims
// coverage with holes.
func (b *inotifyBackend) walkRegister(dir string, roots []*inotifyRoot) {
	err := walkTree(b.ctx, dir, walkVisitor{
		enter: func(d string) (bool, error) {
			ok, err := b.register(d, roots)
			if errors.Is(err, unix.ENOSPC) {
				return false, errWalkLimit
			}
			return ok, err
		},
		skip: func(d string) {
			for _, r := range roots {
				b.unregister(d, r)
			}
		},
	})
	if !errors.Is(err, errWalkLimit) {
		return
	}
	limit := b.hooks.maxUserWatches()
	for _, r := range roots {
		var limitErr *WatchLimitError
		b.mu.Lock()
		if b.roots[r.path] == r {
			limitErr = &WatchLimitError{Limit: limit, Directories: r.dirs}
			r.limitErr = limitErr
		}
		b.mu.Unlock()
		if limitErr == nil {
			continue // released already
		}
		b.RemoveRoot(r.path)
		b.emit(Event{Kind: EventLimitReached, Root: r.path, Err: limitErr})
	}
}

// flushUnmounted asks for one re-walk per root that had a filesystem
// unmounted below it during the last read.
func (b *inotifyBackend) flushUnmounted() {
	if len(b.unmounted) == 0 {
		return
	}
	roots := make([]string, 0, len(b.unmounted))
	for root := range b.unmounted {
		roots = append(roots, root)
	}
	clear(b.unmounted)
	sort.Strings(roots)
	for _, root := range roots {
		b.emit(Event{Kind: EventRewalk, Root: root})
	}
}

// handleSelf handles events about a watched directory itself. Deleting,
// moving, or unmounting a root loses it; the same events on other
// directories are covered by their parent's entry events.
func (b *inotifyBackend) handleSelf(wd int, mask uint32) {
	const selfMask = unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_UNMOUNT | unix.IN_IGNORED
	if mask&selfMask == 0 {
		return
	}
	b.mu.Lock()
	var lost []string
	if w := b.byWD[wd]; w != nil {
		for p := range w.paths {
			for r := range b.byPath[p].roots {
				if r.path == p {
					lost = append(lost, r.path)
				} else if mask&unix.IN_UNMOUNT != 0 {
					// A filesystem mounted inside the root went away. Its
					// paths are dropped with IN_IGNORED; the root is walked
					// again, and reconcile walks it once more when a
					// filesystem is mounted there again, which sends no
					// event.
					b.unmounted[r.path] = struct{}{}
				}
			}
		}
	}
	b.mu.Unlock()
	b.loseRoots(lost)
	if mask&unix.IN_IGNORED != 0 {
		// The kernel removed the watch (deleted directory or unmount).
		b.mu.Lock()
		if w := b.byWD[wd]; w != nil {
			for p := range w.paths {
				b.dropPathLocked(p, func(int) {})
			}
		}
		b.mu.Unlock()
	}
}
