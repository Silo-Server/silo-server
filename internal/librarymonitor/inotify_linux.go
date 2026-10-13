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
	"slices"
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

// errFolderUnreadable marks a directory Silo may not read, so the kernel
// refuses to watch it.
var errFolderUnreadable = errors.New("the folder is not readable")

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

	// rewalks collects, during one read, the roots to walk again: a
	// filesystem was unmounted below them, or a path the walk reached
	// through a symlink was dropped (see inotifyDir.link). Only the read
	// loop uses it.
	rewalks map[string]struct{}
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
	// link is set when the walk reached this path through a symlink to a
	// directory. The walk records each physical directory once per root, so
	// the directory may also sit in the root under its own path, unrecorded;
	// dropping this path then needs a re-walk to record it there.
	link bool
	// skipped is set when the directory's ignore files exclude everything
	// below it: it keeps its watch, so a change to its .nomedia or .ignore
	// is seen, but nothing below it is recorded.
	skipped bool
	// markers counts re-evaluations of its ignore files (reevaluate). A walk
	// applies its own listing only if none ran since it recorded the
	// directory; otherwise the re-evaluation read a newer state.
	markers uint64
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
	// at is when the read loop saw the IN_MOVED_FROM.
	at time.Time
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

		rewalks: make(map[string]struct{}),
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
	seen := b.newMarkerSeen()
	err := walkTree(ctx, root, walkVisitor{
		enter: func(dir string, link bool) (bool, error) {
			total++
			if limited {
				return true, nil
			}
			ok, err := b.register(dir, link, []*inotifyRoot{r})
			if errors.Is(err, unix.ENOSPC) {
				limited = true
				return true, nil
			}
			if errors.Is(err, errFolderUnreadable) && dir != root {
				// The scanner cannot read it either. The status names it
				// instead of claiming it.
				skippedPathsFrom(ctx).addUnreadable(dir)
				ok, err = false, nil
			}
			if err != nil {
				return false, err
			}
			if !ok {
				total--
				return false, nil
			}
			seen.record(dir)
			return true, nil
		},
		listed: seen.listed,
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

// register adds a watch for dir and records it for roots; link is set when
// the walk reached dir through a symlink. It reports whether dir was
// recorded for any root: false when dir vanished while the walk ran (or is no
// longer a directory), or is an alias of a directory already recorded for the
// same root under another path. Any other failure is an error: ENOSPC (the
// watch limit), errFolderUnreadable when Silo may not read dir, or the
// kernel's error, so no status claims a directory it does not watch.
func (b *inotifyBackend) register(dir string, link bool, roots []*inotifyRoot) (bool, error) {
	wd, err := b.addWatch(dir)
	switch {
	case err == nil:
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
		return false, nil // gone, or replaced by a file, since it was listed
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return false, fmt.Errorf("watch %s: %w: %w", dir, errFolderUnreadable, err)
	case errors.Is(err, unix.ENOSPC), errors.Is(err, errBackendClosed):
		return false, err
	default:
		return false, fmt.Errorf("watch %s: %w", dir, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false, errBackendClosed
	}
	if d := b.byPath[dir]; d != nil && d.wd != wd {
		// The path now names a different directory than the one recorded.
		_, _ = b.dropSubtreeLocked(dir, b.rmWatchLocked)
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
		d.link = link
		recorded = true
	}
	if len(w.paths) == 0 {
		delete(b.byWD, wd)
		b.rmWatchLocked(wd)
	}
	return recorded, nil
}

// markerSeen is one walk's view of the directories it recorded: the
// re-evaluation count of each when the walk recorded it (see
// inotifyDir.markers). The walk calls record and listed from one goroutine.
type markerSeen struct {
	b    *inotifyBackend
	seen map[string]uint64
}

func (b *inotifyBackend) newMarkerSeen() *markerSeen {
	return &markerSeen{b: b, seen: make(map[string]uint64)}
}

// record notes dir's re-evaluation count right after the walk recorded it,
// before the walk lists it.
func (s *markerSeen) record(dir string) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if d := s.b.byPath[dir]; d != nil {
		s.seen[dir] = d.markers
	}
}

// listed applies the walk's listing of dir, whether its ignore files exclude
// what is below it, unless a re-evaluation ran since record: it read the
// directory after the walk may have, so its answer stands and is returned.
func (s *markerSeen) listed(dir string, skipped bool) bool {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	d := s.b.byPath[dir]
	if d == nil {
		return skipped
	}
	if seen, ok := s.seen[dir]; ok && d.markers != seen {
		return d.skipped
	}
	d.skipped = skipped
	return skipped
}

// inSkipped reports whether the directory parents name is one whose ignore
// files exclude everything below it.
func (b *inotifyBackend) inSkipped(parents []inotifyParent) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range parents {
		if d := b.byPath[p.path]; d != nil && d.skipped {
			return true
		}
	}
	return false
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
//
// Only paths the root's current walk generation recorded count. A re-walk
// after events were lost (an overflow) must not treat a renamed directory as
// an alias of its old path, which the same watch descriptor still carries
// until the walk's cleanup drops it.
func (b *inotifyBackend) aliasLocked(w *inotifyWatch, r *inotifyRoot) bool {
	for p := range w.paths {
		if d := b.byPath[p]; d != nil {
			if gen, ok := d.roots[r]; ok && gen == r.gen {
				return true
			}
		}
	}
	return false
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
// instead of) an event on the root itself. It also returns the other roots
// that covered a dropped path the walk reached through a symlink (see
// inotifyDir.link); they need a re-walk.
func (b *inotifyBackend) dropSubtreeLocked(path string, rm func(wd int)) (lost, aliased []string) {
	for _, p := range b.children.subtree(path) {
		if d := b.byPath[p]; d != nil {
			for r := range d.roots {
				switch {
				case r.path == p:
					lost = append(lost, r.path)
				case d.link:
					aliased = append(aliased, r.path)
				}
			}
		}
		b.dropPathLocked(p, rm)
	}
	return lost, aliased
}

// rewalkAliased asks for a re-walk of the aliased roots (see
// dropSubtreeLocked), except those in covered: lost roots, or roots a walk
// already records again.
func (b *inotifyBackend) rewalkAliased(aliased, covered []string) {
	for _, root := range aliased {
		if !slices.Contains(covered, root) {
			b.rewalks[root] = struct{}{}
		}
	}
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
		switch {
		case err == nil:
			moves = b.process(buf[:n], moves)
		case errors.Is(err, os.ErrDeadlineExceeded):
		default:
			if !b.isClosed() {
				b.log.Error("librarymonitor: inotify read failed", "component", "librarymonitor", "err", err)
			}
			return
		}
		// Checked after every read, not only when a read times out: a
		// steady stream of events would otherwise keep a move out pending,
		// and unreported, for as long as the stream lasts.
		moves = expireMoves(moves, time.Now(), inotifyMoveWait, func(mv inotifyMove) time.Time { return mv.at }, b.movedOut)
		b.flushRewalks()
		if len(moves) > 0 {
			if err := b.file.SetReadDeadline(moves[0].at.Add(inotifyMoveWait)); err != nil {
				b.movedOut(moves)
				moves = nil
				b.flushRewalks()
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
		return append(moves, inotifyMove{cookie: cookie, name: name, isDir: isDir, parents: parents, at: time.Now()})
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
		if !ignoredName(name) && !ignoreFile(name) && !b.inSkipped(parents) {
			for _, p := range parents {
				b.emit(Event{Kind: EventCloseWrite, Dir: p.path, Name: name})
			}
		}
		b.markerChanged(parents, name)
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
// before the event is emitted. Nothing that arrives in a skipped directory
// is recorded or reported. An ignore marker (.nomedia, .ignore) is never
// reported, only re-evaluated: the scanner has nothing to do with the file
// itself.
func (b *inotifyBackend) arrived(kind EventKind, parents []inotifyParent, name string, isDir bool) {
	defer b.markerChanged(parents, name)
	if ignoredName(name) || (!isDir && ignoreFile(name)) || b.inSkipped(parents) {
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
			b.walkRegister(filepath.Join(p.path, name), p.roots, kind == EventCreate)
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
	defer b.markerChanged(parents, name)
	isDir = isDir || b.recordedDir(parents, name)
	if isDir {
		// A directory moved out of the tree keeps its watches on the moved
		// inodes; drop them so no event arrives under a stale path.
		var lost, aliased []string
		b.mu.Lock()
		for _, p := range parents {
			l, a := b.dropSubtreeLocked(filepath.Join(p.path, name), b.rmWatchLocked)
			lost, aliased = append(lost, l...), append(aliased, a...)
		}
		b.mu.Unlock()
		b.loseRoots(lost)
		b.rewalkAliased(aliased, lost)
	}
	if ignoredName(name) || (isDir && ignoredDir(name)) || (!isDir && ignoreFile(name)) || b.inSkipped(parents) {
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
	// Nothing in a skipped directory is recorded or reported, and an ignore
	// marker is re-evaluated below instead of reported.
	oldIgnored := ignoredName(from.name) || (oldDir && ignoredDir(from.name)) || (!oldDir && ignoreFile(from.name)) || b.inSkipped(from.parents)
	newIgnored := ignoredName(name) || (newDir && ignoredDir(name)) || (!newDir && ignoreFile(name)) || b.inSkipped(parents)
	var detached []int
	var lost, aliased []string
	if oldDir {
		b.mu.Lock()
		for _, p := range from.parents {
			l, a := b.dropSubtreeLocked(filepath.Join(p.path, from.name), func(wd int) { detached = append(detached, wd) })
			lost, aliased = append(lost, l...), append(aliased, a...)
		}
		b.mu.Unlock()
		// A root inside the moved directory is gone from its configured
		// path; release it before the walk re-records the new location.
		b.loseRoots(lost)
	}
	// A lost root needs no re-walk, and the walk of the new location records
	// the moved aliases again for its roots.
	covered := slices.Clone(lost)
	if newDir && !newIgnored {
		for _, p := range parents {
			b.walkRegister(filepath.Join(p.path, name), p.roots, false)
			for _, r := range p.roots {
				covered = append(covered, r.path)
			}
		}
	}
	b.rewalkAliased(aliased, covered)
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
	b.markerChanged(from.parents, from.name)
	b.markerChanged(parents, name)
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
//
// For a directory that was just created (created), files found below it are
// reported as Found creates: a recursive copy can write them before the new
// directory's watch exists, and the tracker must wait for them before it
// reports the directory. A directory moved in arrives whole, so its files are
// not reported.
func (b *inotifyBackend) walkRegister(dir string, roots []*inotifyRoot, created bool) {
	seen := b.newMarkerSeen()
	v := walkVisitor{
		enter: func(d string, link bool) (bool, error) {
			ok, err := b.register(d, link, roots)
			if errors.Is(err, unix.ENOSPC) {
				return false, errWalkLimit
			}
			if ok {
				seen.record(d)
			}
			return ok, err
		},
		listed: seen.listed,
	}
	if created {
		v.file = func(path string) {
			b.emit(Event{Kind: EventCreate, Dir: filepath.Dir(path), Name: filepath.Base(path), Found: true})
		}
	}
	err := walkTree(b.ctx, dir, v)
	if err != nil && !errors.Is(err, errWalkLimit) {
		// A directory below could not be watched (unreadable, or a kernel
		// error). Walk the roots again: that walk names an unreadable
		// directory in the status, or fails and reports the error.
		if b.ctx.Err() == nil {
			b.mu.Lock()
			for _, r := range roots {
				if b.roots[r.path] == r {
					b.rewalks[r.path] = struct{}{}
				}
			}
			b.mu.Unlock()
		}
		return
	}
	if err == nil {
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

// flushRewalks asks for one re-walk per root collected during the last read
// (see rewalks).
func (b *inotifyBackend) flushRewalks() {
	if len(b.rewalks) == 0 {
		return
	}
	roots := make([]string, 0, len(b.rewalks))
	for root := range b.rewalks {
		roots = append(roots, root)
	}
	clear(b.rewalks)
	sort.Strings(roots)
	for _, root := range roots {
		b.emit(Event{Kind: EventRewalk, Root: root})
	}
}

// markerChanged re-evaluates the directories parents name after an event on
// name in them, when name is an ignore file (see ignoreFile).
func (b *inotifyBackend) markerChanged(parents []inotifyParent, name string) {
	if !ignoreFile(name) {
		return
	}
	for _, p := range parents {
		b.reevaluate(p, name != markerNoMedia)
	}
}

// reevaluate applies a directory's current ignore files. A directory they
// now exclude keeps its own watch but drops everything recorded below it. A
// directory they no longer exclude is walked. Either way it is reported, so
// a scan reconciles what the change excluded or included. When rules changed (an .ignore or .siloignore was created,
// rewritten, or removed) and the directory stays included, it is reported
// too: its patterns may now include or exclude entries in it.
func (b *inotifyBackend) reevaluate(p inotifyParent, rulesChanged bool) {
	entries, err := os.ReadDir(p.path)
	if err != nil {
		return // gone; its parent's event drops it
	}
	skipped := dirSkipped(p.path, entries)
	b.mu.Lock()
	d := b.byPath[p.path]
	if d != nil {
		d.markers++ // a walk that listed it earlier defers to this reading
	}
	if d == nil || (d.skipped == skipped && (skipped || !rulesChanged)) {
		b.mu.Unlock()
		return
	}
	isRoot := false
	for _, r := range p.roots {
		isRoot = isRoot || r.path == p.path
	}
	if d.skipped == skipped {
		b.mu.Unlock()
		reportFolder(b.emit, p.path, isRoot)
		return
	}
	d.skipped = skipped
	if skipped {
		// Only roots that reach p.path from above, or start there, stop at
		// the marker. A library folder configured below it is walked from its
		// own path, and markers above a root do not apply to it, as in the
		// scanner; it keeps everything it records.
		var aliased []string
		for _, child := range b.children.of(p.path) {
			for _, sub := range b.children.subtree(child) {
				sd := b.byPath[sub]
				if sd == nil {
					continue
				}
				for r := range sd.roots {
					if isBelow(r.path, p.path) {
						continue
					}
					if sd.link {
						aliased = append(aliased, r.path)
					}
					b.removeRootFromPathLocked(sub, sd, r)
				}
			}
		}
		b.mu.Unlock()
		b.rewalkAliased(aliased, nil)
		// The scan reconciles what the marker now excludes.
		reportFolder(b.emit, p.path, isRoot)
		return
	}
	b.mu.Unlock()
	b.walkRegister(p.path, p.roots, false)
	reportFolder(b.emit, p.path, isRoot)
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
			d := b.byPath[p]
			if d == nil {
				continue
			}
			for r := range d.roots {
				switch {
				case r.path == p:
					lost = append(lost, r.path)
				case mask&unix.IN_UNMOUNT != 0:
					// A filesystem mounted inside the root went away. Its
					// paths are dropped with IN_IGNORED; the root is walked
					// again, and reconcile walks it once more when a
					// filesystem is mounted there again, which sends no
					// event.
					b.rewalks[r.path] = struct{}{}
				case d.link && mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0:
					// A symlink's target was moved or deleted where it
					// really lives, which may be outside every watched
					// directory: no entry event reports it. Walk the root
					// again, so the stale path is dropped and a target now
					// in place is recorded.
					b.rewalks[r.path] = struct{}{}
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
