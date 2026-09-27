//go:build linux

package librarymonitor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fanotifyInitFlags creates the process's notification group. Events carry
// the parent directory's file handle and the entry name instead of an open
// file descriptor, so reading them never opens anything.
const fanotifyInitFlags = unix.FAN_CLASS_NOTIF | unix.FAN_REPORT_DFID_NAME | unix.FAN_CLOEXEC | unix.FAN_NONBLOCK

// fanotifyMask is the filesystem mark's mask. There is deliberately no
// FAN_MODIFY: a copy always ends in FAN_CLOSE_WRITE, so per-write events
// would only add volume. FAN_ONDIR reports directory entries and events on
// directories themselves.
const fanotifyMask = unix.FAN_CREATE | unix.FAN_DELETE | unix.FAN_MOVED_FROM | unix.FAN_MOVED_TO |
	unix.FAN_CLOSE_WRITE | unix.FAN_DELETE_SELF | unix.FAN_MOVE_SELF | unix.FAN_ONDIR

// atHandleFID is AT_HANDLE_FID (Linux 6.5): name_to_handle_at encodes the
// handle the way fanotify encodes event handles. Older kernels reject the
// flag with EINVAL; their fanotify handles are the plain name_to_handle_at
// encoding.
const atHandleFID = 0x200

// fanotifyMoveWait bounds how long a FAN_MOVED_FROM that ends a read waits
// for its FAN_MOVED_TO. The kernel queues both halves of a rename together,
// so only a read buffer boundary can split them.
const fanotifyMoveWait = 20 * time.Millisecond

// fanotifyReadBuffer holds many events per read; one event with a
// directory handle and a name needs well under 512 bytes.
const fanotifyReadBuffer = 64 * 1024

// fanotifyMetadataSize is sizeof(struct fanotify_event_metadata).
const fanotifyMetadataSize = int(unsafe.Sizeof(unix.FanotifyEventMetadata{}))

// fanotifyInfoHead is the fixed part of a FID info record: the info header,
// the fsid, and the file_handle header (handle_bytes, handle_type).
const fanotifyInfoHead = 4 + 8 + 8

func init() {
	newFanotifyBackend = func(opts BackendOptions) (Backend, error) {
		// Not "return newFanotify(...)": on error that would wrap a nil
		// *fanotifyBackend in a non-nil Backend.
		b, err := newFanotify(opts, fanotifyHooks{})
		if err != nil {
			return nil, err
		}
		return b, nil
	}
}

// fanotifyHooks is the test seam for placing and removing marks, which an
// unprivileged test process cannot do.
type fanotifyHooks struct {
	mark func(fd int, flags uint, mask uint64, path string) error
}

// fsID is a filesystem's statfs f_fsid, as fanotify reports it.
type fsID [8]byte

// handleKey identifies a directory as fanotify reports it: the fsid, the
// file handle type, and the handle bytes.
type handleKey string

func newHandleKey(fsid []byte, handleType uint32, handle []byte) handleKey {
	buf := make([]byte, 0, len(fsid)+4+len(handle))
	buf = append(buf, fsid...)
	buf = binary.NativeEndian.AppendUint32(buf, handleType)
	buf = append(buf, handle...)
	return handleKey(buf)
}

func (k handleKey) fsid() fsID {
	var id fsID
	copy(id[:], k)
	return id
}

func fsidOf(st *unix.Statfs_t) fsID {
	var id fsID
	binary.NativeEndian.PutUint32(id[0:], uint32(st.Fsid.Val[0])) //nolint:gosec // raw bytes of __kernel_fsid_t.
	binary.NativeEndian.PutUint32(id[4:], uint32(st.Fsid.Val[1])) //nolint:gosec // raw bytes of __kernel_fsid_t.
	return id
}

// fanotifyBackend is the privileged backend: one fanotify group for the
// process and one FAN_MARK_FILESYSTEM mark per filesystem that holds a
// root. A filesystem mark reports every change on that filesystem, so the
// backend keeps its own map of recorded directories, keyed by the handle
// events carry, and drops every event whose directory is not in it.
//
// It never calls open_by_handle_at: that needs CAP_DAC_READ_SEARCH, and
// because the mark sees the whole filesystem it could reach host files
// outside a container's bind mount. Handles are only ever compared with the
// ones name_to_handle_at returned for directories inside roots.
//
// As with inotify, a directory can be reachable under several logical
// paths (overlapping roots, symlink aliases). Each logical path records
// which roots cover it, and an event is reported once per logical path.
type fanotifyBackend struct {
	fd      int
	file    *os.File
	log     *slog.Logger
	hooks   fanotifyHooks
	events  chan Event
	ctx     context.Context
	cancel  context.CancelFunc
	closing chan struct{}
	done    chan struct{}
	once    sync.Once
	// fdMu keeps the descriptor open while fanotify_mark runs. The call
	// resolves a path, which can block on a hung mount, so it runs outside
	// mu: status reads of the maps must never wait on a mount.
	fdMu sync.RWMutex
	// noHandleFID is set once the kernel rejected AT_HANDLE_FID.
	noHandleFID atomic.Bool

	// markMu serializes mark changes with their reference counts, so a
	// mark is never removed while another root is placing it.
	markMu sync.Mutex
	marks  map[fsID]*fanotifyMark

	mu     sync.Mutex
	closed bool
	// mountFSIDs maps the mount ID of each marked mount to the fsid its
	// mark was placed with, which is the fsid the kernel reports in every
	// event from that filesystem. A btrfs subvolume below a marked
	// directory has its own statfs fsid but reports the mark's.
	mountFSIDs map[int]fsID
	byHandle   map[handleKey]*fanotifyHandle
	byPath     map[string]*fanotifyDir
	children   childDirs
	roots      map[string]*fanotifyRoot
}

// fanotifyMark is one filesystem mark and the roots that hold it.
type fanotifyMark struct {
	path  string
	roots map[*fanotifyRoot]struct{}
	// stale is set when the kernel may have dropped the mark with its
	// superblock (see checkMarks); the next root to record a directory on
	// the filesystem places it again.
	stale bool
}

// fanotifyHandle is one recorded directory and its logical paths.
type fanotifyHandle struct {
	paths map[string]struct{}
}

type fanotifyDir struct {
	key handleKey
	// roots maps each covering root to the walk generation that last
	// recorded this path for it; a re-walk drops paths it did not reach.
	roots map[*fanotifyRoot]uint64
}

type fanotifyRoot struct {
	path string
	dirs int
	gen  uint64
	// fsids and released are guarded by markMu.
	fsids    map[fsID]struct{}
	released bool
}

// fanotifyParent is one logical path of a recorded directory, with its roots.
type fanotifyParent struct {
	path  string
	roots []*fanotifyRoot
}

// fanotifyMove is a FAN_MOVED_FROM waiting for its FAN_MOVED_TO. key is the
// moved directory's handle, which a rename keeps; it pairs the halves of a
// directory rename.
type fanotifyMove struct {
	name    string
	isDir   bool
	key     handleKey
	parents []fanotifyParent
}

// fanotifyRecord is one parsed event.
type fanotifyRecord struct {
	mask uint64
	fd   int32
	// dir is the directory handle, empty when the event has none (for
	// example FAN_DELETE_SELF on a file, which only carries the file's own
	// handle).
	dir handleKey
	// name is the entry name; "" or "." for an event on dir itself.
	name string
}

func newFanotify(opts BackendOptions, hooks fanotifyHooks) (*fanotifyBackend, error) {
	fd, err := unix.FanotifyInit(fanotifyInitFlags, unix.O_RDONLY|unix.O_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("fanotify_init: %w", err)
	}
	if hooks.mark == nil {
		hooks.mark = func(fd int, flags uint, mask uint64, path string) error {
			return unix.FanotifyMark(fd, flags, mask, unix.AT_FDCWD, path)
		}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &fanotifyBackend{
		fd: fd,
		// A non-blocking descriptor makes the file pollable, so Close
		// unblocks Read and read deadlines work.
		file:       os.NewFile(uintptr(fd), "fanotify"),
		log:        logger,
		hooks:      hooks,
		events:     make(chan Event, 1024),
		ctx:        ctx,
		cancel:     cancel,
		closing:    make(chan struct{}),
		done:       make(chan struct{}),
		marks:      make(map[fsID]*fanotifyMark),
		mountFSIDs: make(map[int]fsID),
		byHandle:   make(map[handleKey]*fanotifyHandle),
		byPath:     make(map[string]*fanotifyDir),
		children:   make(childDirs),
		roots:      make(map[string]*fanotifyRoot),
	}
	go b.readLoop()
	return b, nil
}

func (b *fanotifyBackend) Name() string { return BackendFanotify }

func (b *fanotifyBackend) Events() <-chan Event { return b.events }

// Close releases the group; closing its descriptor removes every mark.
func (b *fanotifyBackend) Close() error {
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

func (b *fanotifyBackend) Directories(root string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.roots[root]; r != nil {
		return r.dirs
	}
	return 0
}

func (b *fanotifyBackend) AddRoot(ctx context.Context, root string) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errBackendClosed
	}
	r := b.roots[root]
	if r == nil {
		r = &fanotifyRoot{path: root, fsids: make(map[fsID]struct{})}
		b.roots[root] = r
	}
	r.gen++
	gen := r.gen
	b.mu.Unlock()

	// A root without file handles fails here, before the walk, so the
	// monitor falls back to inotify with the reason.
	if _, _, err := b.identify(root); err != nil {
		return fmt.Errorf("name_to_handle_at %s: %w", root, err)
	}
	err := walkTree(ctx, root, walkVisitor{
		enter: func(dir string) (bool, error) {
			return b.register(dir, []*fanotifyRoot{r})
		},
		skip: func(dir string) { b.unregister(dir, r) },
	})
	if err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.roots[root] != r {
		// Released while the walk ran: the root was lost, or a directory
		// created under it could not be recorded.
		return errRootReleased
	}
	for path, d := range b.byPath {
		if g, ok := d.roots[r]; ok && g < gen {
			b.removeRootFromPathLocked(path, d, r)
		}
	}
	return nil
}

func (b *fanotifyBackend) RemoveRoot(root string) {
	b.mu.Lock()
	r := b.roots[root]
	if r == nil {
		b.mu.Unlock()
		return
	}
	delete(b.roots, root)
	for path, d := range b.byPath {
		if _, ok := d.roots[r]; ok {
			b.removeRootFromPathLocked(path, d, r)
		}
	}
	b.mu.Unlock()
	b.releaseMarks(r)
}

// nameToHandle returns dir's file handle, following a final symlink: roots
// and the directories below them are logical paths.
func (b *fanotifyBackend) nameToHandle(dir string) (unix.FileHandle, int, error) {
	if !b.noHandleFID.Load() {
		fh, mountID, err := unix.NameToHandleAt(unix.AT_FDCWD, dir, unix.AT_SYMLINK_FOLLOW|atHandleFID)
		if !errors.Is(err, unix.EINVAL) {
			return fh, mountID, err
		}
		b.noHandleFID.Store(true)
	}
	return unix.NameToHandleAt(unix.AT_FDCWD, dir, unix.AT_SYMLINK_FOLLOW)
}

// identify returns the key fanotify events use for directory dir, and the
// mount ID dir was reached through. The fsid is the one of the mark already
// covering that mount, or else dir's own, which a new mark would use.
func (b *fanotifyBackend) identify(dir string) (handleKey, int, error) {
	fh, mountID, err := b.nameToHandle(dir)
	if err != nil {
		return "", 0, err
	}
	b.mu.Lock()
	id, ok := b.mountFSIDs[mountID]
	b.mu.Unlock()
	if !ok {
		var st unix.Statfs_t
		if err := unix.Statfs(dir, &st); err != nil {
			return "", 0, err
		}
		id = fsidOf(&st)
	}
	return newHandleKey(id[:], uint32(fh.Type()), fh.Bytes()), mountID, nil //nolint:gosec // raw bytes of handle_type.
}

// noFileHandles reports whether err means the filesystem cannot encode file
// handles, so fanotify cannot identify its directories.
func noFileHandles(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENODEV)
}

// vanishedPath reports whether err means the path is simply not (or no
// longer) a reachable directory, which a walk skips.
func vanishedPath(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) ||
		errors.Is(err, unix.EACCES) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENAMETOOLONG)
}

// register records dir for roots by its file handle and holds a mark on its
// filesystem for each root. It reports whether dir was recorded for any
// root: false when dir vanished or cannot be read, or is an alias of a
// directory already recorded for the same root under another path. A
// directory on a filesystem fanotify cannot identify or mark is an error:
// the root would be monitored with holes.
func (b *fanotifyBackend) register(dir string, roots []*fanotifyRoot) (bool, error) {
	key, mountID, err := b.identify(dir)
	if err != nil {
		if noFileHandles(err) {
			return false, fmt.Errorf("name_to_handle_at %s: %w", dir, err)
		}
		return false, nil
	}
	for _, r := range roots {
		if err := b.holdMark(r, key.fsid(), mountID, dir); err != nil {
			if vanishedPath(err) {
				return false, nil
			}
			return false, err
		}
	}
	stale := b.staleAliases(key, dir)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return false, errBackendClosed
	}
	// Paths that no longer name this directory are where it was before a
	// move whose events have not been handled yet (merged or reordered
	// events); forget them so the new path is not mistaken for an alias.
	var lost []string
	for _, p := range stale {
		if d := b.byPath[p]; d != nil && d.key == key {
			lost = append(lost, b.dropSubtreeLocked(p)...)
		}
	}
	if d := b.byPath[dir]; d != nil && d.key != key {
		// The path now names a different directory than the one recorded.
		b.dropSubtreeLocked(dir)
	}
	h := b.byHandle[key]
	if h == nil {
		h = &fanotifyHandle{paths: make(map[string]struct{})}
		b.byHandle[key] = h
	}
	d := b.byPath[dir]
	recorded := false
	for _, r := range roots {
		if b.roots[r.path] != r {
			continue // removed while the walk ran
		}
		if d == nil && b.aliasLocked(h, r) {
			continue
		}
		if d == nil {
			d = &fanotifyDir{key: key, roots: make(map[*fanotifyRoot]uint64)}
			b.byPath[dir] = d
			h.paths[dir] = struct{}{}
			b.children.add(dir)
		}
		if _, ok := d.roots[r]; !ok {
			r.dirs++
		}
		d.roots[r] = r.gen
		recorded = true
	}
	if len(h.paths) == 0 {
		delete(b.byHandle, key)
	}
	b.mu.Unlock()
	b.loseRoots(lost)
	return recorded, nil
}

// staleAliases returns the other recorded paths of key that no longer name
// that directory. It runs the checks outside mu.
func (b *fanotifyBackend) staleAliases(key handleKey, dir string) []string {
	b.mu.Lock()
	var others []string
	if h := b.byHandle[key]; h != nil {
		for p := range h.paths {
			if p != dir {
				others = append(others, p)
			}
		}
	}
	b.mu.Unlock()
	var stale []string
	for _, p := range others {
		got, _, err := b.identify(p)
		if (err != nil && vanishedPath(err)) || (err == nil && got != key) {
			stale = append(stale, p)
		}
	}
	return stale
}

// holdMark makes r hold the filesystem mark for id, placing the mark
// through path if no root holds it yet, and maps path's mount to id.
func (b *fanotifyBackend) holdMark(r *fanotifyRoot, id fsID, mountID int, path string) error {
	b.markMu.Lock()
	defer b.markMu.Unlock()
	if r.released {
		return nil // removed while the walk ran; register skips it
	}
	m := b.marks[id]
	if m == nil || m.stale {
		// Adding a mark the group already holds on this superblock is a
		// no-op, so a stale mark that survived is simply confirmed.
		if err := b.markPath(unix.FAN_MARK_ADD|unix.FAN_MARK_FILESYSTEM, path); err != nil {
			return fmt.Errorf("fanotify_mark %s: %w", path, err)
		}
		if m == nil {
			m = &fanotifyMark{roots: make(map[*fanotifyRoot]struct{})}
			b.marks[id] = m
		}
		m.path = path
		m.stale = false
	}
	if _, ok := r.fsids[id]; !ok {
		m.roots[r] = struct{}{}
		r.fsids[id] = struct{}{}
	}
	b.mu.Lock()
	b.mountFSIDs[mountID] = id
	b.mu.Unlock()
	return nil
}

// checkMarks compares the filesystem marks the kernel lists for the group
// with the ones the backend holds. The kernel drops a mark with its
// superblock when the filesystem is unmounted, without an event, and a
// filesystem mounted again (even at once, with the same device, fsid, and
// mount ID) gets a new superblock with no mark. When the kernel lists fewer
// marks, every mark is treated as stale and every root is walked again,
// which places the missing marks anew; walks confirm the live ones with an
// idempotent FAN_MARK_ADD.
func (b *fanotifyBackend) checkMarks() {
	b.markMu.Lock()
	held := 0
	for _, m := range b.marks {
		if !m.stale {
			held++
		}
	}
	if held == 0 {
		b.markMu.Unlock()
		return
	}
	listed, err := b.kernelMarks()
	if err != nil || listed >= held {
		b.markMu.Unlock()
		return
	}
	for _, m := range b.marks {
		m.stale = true
	}
	b.mu.Lock()
	// A mount ID can be reused by the next mount; forget the old mapping.
	clear(b.mountFSIDs)
	roots := make([]string, 0, len(b.roots))
	for root := range b.roots {
		roots = append(roots, root)
	}
	b.mu.Unlock()
	b.markMu.Unlock()
	sort.Strings(roots)
	b.log.Warn("librarymonitor: a fanotify filesystem mark was dropped (filesystem unmounted); recording library folders again",
		"component", "librarymonitor", "marks", held, "listed", listed)
	// The monitor's event loop calls this; emit from elsewhere so a full
	// channel cannot block it.
	go func() {
		for _, root := range roots {
			b.emit(Event{Kind: EventRewalk, Root: root})
		}
	}()
}

// kernelMarks counts the group's filesystem marks as the kernel lists them
// in fdinfo ("fanotify sdev:..." lines; inode marks start "fanotify ino:").
func (b *fanotifyBackend) kernelMarks() (int, error) {
	b.fdMu.RLock()
	defer b.fdMu.RUnlock()
	if b.isClosed() {
		return 0, errBackendClosed
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", b.fd))
	if err != nil {
		return 0, err
	}
	return bytes.Count(data, []byte("fanotify sdev:")), nil
}

// releaseMarks drops r's hold on its marks and removes every mark no other
// root holds.
func (b *fanotifyBackend) releaseMarks(r *fanotifyRoot) {
	b.markMu.Lock()
	defer b.markMu.Unlock()
	r.released = true
	for id := range r.fsids {
		delete(r.fsids, id)
		m := b.marks[id]
		if m == nil {
			continue
		}
		delete(m.roots, r)
		if len(m.roots) > 0 {
			continue
		}
		delete(b.marks, id)
		b.mu.Lock()
		for mountID, mounted := range b.mountFSIDs {
			if mounted == id {
				delete(b.mountFSIDs, mountID)
			}
		}
		b.mu.Unlock()
		b.removeMarkLocked(id, r.path, m.path)
	}
}

// removeMarkLocked removes the mark of filesystem id. fanotify_mark needs
// a path on that filesystem, and the root may be gone, so it tries the
// given paths and then their ancestors on the same filesystem. When none is
// left the filesystem is gone, and the kernel dropped the mark with it.
func (b *fanotifyBackend) removeMarkLocked(id fsID, paths ...string) {
	for _, p := range paths {
		for dir := p; ; dir = filepath.Dir(dir) {
			var st unix.Statfs_t
			if unix.Statfs(dir, &st) == nil && fsidOf(&st) == id {
				err := b.markPath(unix.FAN_MARK_REMOVE|unix.FAN_MARK_FILESYSTEM, dir)
				if err == nil || errors.Is(err, unix.ENOENT) || errors.Is(err, errBackendClosed) {
					return
				}
				b.log.Debug("librarymonitor: removing fanotify mark failed", "path", dir, "err", err)
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
}

func (b *fanotifyBackend) markPath(flags uint, path string) error {
	b.fdMu.RLock()
	defer b.fdMu.RUnlock()
	if b.isClosed() {
		return errBackendClosed
	}
	return b.hooks.mark(b.fd, flags, fanotifyMask, path)
}

func (b *fanotifyBackend) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// aliasLocked reports whether directory h is already recorded for r under
// some logical path, which makes another path to it a symlink alias.
func (b *fanotifyBackend) aliasLocked(h *fanotifyHandle, r *fanotifyRoot) bool {
	for p := range h.paths {
		if d := b.byPath[p]; d != nil {
			if _, ok := d.roots[r]; ok {
				return true
			}
		}
	}
	return false
}

func (b *fanotifyBackend) unregister(dir string, r *fanotifyRoot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d := b.byPath[dir]; d != nil {
		if _, ok := d.roots[r]; ok {
			b.removeRootFromPathLocked(dir, d, r)
		}
	}
}

func (b *fanotifyBackend) removeRootFromPathLocked(path string, d *fanotifyDir, r *fanotifyRoot) {
	delete(d.roots, r)
	r.dirs--
	if len(d.roots) == 0 {
		b.dropPathLocked(path)
	}
}

// dropPathLocked forgets one logical path.
func (b *fanotifyBackend) dropPathLocked(path string) {
	d := b.byPath[path]
	if d == nil {
		return
	}
	for r := range d.roots {
		r.dirs--
	}
	delete(b.byPath, path)
	b.children.remove(path)
	if h := b.byHandle[d.key]; h != nil {
		delete(h.paths, path)
		if len(h.paths) == 0 {
			delete(b.byHandle, d.key)
		}
	}
}

// dropSubtreeLocked forgets path and every recorded path below it. It
// returns the roots whose own directory was among them: a root nested in
// another root's tree is lost through its parent's event.
func (b *fanotifyBackend) dropSubtreeLocked(path string) []string {
	var lost []string
	for _, p := range b.children.subtree(path) {
		if d := b.byPath[p]; d != nil {
			for r := range d.roots {
				if r.path == p {
					lost = append(lost, r.path)
				}
			}
		}
		b.dropPathLocked(p)
	}
	return lost
}

// loseRoots releases roots that disappeared and reports them.
func (b *fanotifyBackend) loseRoots(roots []string) {
	sort.Strings(roots)
	for _, root := range roots {
		b.RemoveRoot(root)
		b.emit(Event{Kind: EventRootLost, Root: root})
	}
}

func (b *fanotifyBackend) emit(ev Event) {
	select {
	case b.events <- ev:
	case <-b.closing:
	}
}

func (b *fanotifyBackend) readLoop() {
	defer close(b.done)
	defer close(b.events)
	buf := make([]byte, fanotifyReadBuffer)
	var moves []fanotifyMove
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
				b.log.Error("librarymonitor: fanotify read failed", "component", "librarymonitor", "err", err)
			}
			return
		}
		moves = b.process(buf[:n], moves)
		if len(moves) > 0 {
			if err := b.file.SetReadDeadline(time.Now().Add(fanotifyMoveWait)); err != nil {
				b.movedOut(moves)
				moves = nil
			}
		} else {
			_ = b.file.SetReadDeadline(time.Time{})
		}
	}
}

var errFanotifyMalformed = errors.New("librarymonitor: malformed fanotify event")

// parseFanotifyEvents decodes one read of FAN_REPORT_DFID_NAME events. It
// keeps each event's first directory record (DFID_NAME or DFID); FID
// records name the object itself and are ignored. A malformed buffer
// returns the events before the fault with an error.
func parseFanotifyEvents(buf []byte) ([]fanotifyRecord, error) {
	var out []fanotifyRecord
	for off := 0; off < len(buf); {
		if off+fanotifyMetadataSize > len(buf) {
			return out, errFanotifyMalformed
		}
		eventLen := int(binary.NativeEndian.Uint32(buf[off:]))
		metaLen := int(binary.NativeEndian.Uint16(buf[off+6:]))
		if eventLen < fanotifyMetadataSize || off+eventLen > len(buf) ||
			metaLen < fanotifyMetadataSize || metaLen > eventLen {
			return out, errFanotifyMalformed
		}
		if vers := buf[off+4]; vers != unix.FANOTIFY_METADATA_VERSION {
			return out, fmt.Errorf("%w: metadata version %d", errFanotifyMalformed, vers)
		}
		rec := fanotifyRecord{
			mask: binary.NativeEndian.Uint64(buf[off+8:]),
			fd:   int32(binary.NativeEndian.Uint32(buf[off+16:])), //nolint:gosec // the kernel writes fd as a signed 32-bit value.
		}
		for info := buf[off+metaLen : off+eventLen]; len(info) >= 4; {
			infoType := info[0]
			infoLen := int(binary.NativeEndian.Uint16(info[2:]))
			if infoLen < 4 || infoLen > len(info) {
				return out, errFanotifyMalformed
			}
			record := info[:infoLen]
			info = info[infoLen:]
			if rec.dir != "" || (infoType != unix.FAN_EVENT_INFO_TYPE_DFID_NAME && infoType != unix.FAN_EVENT_INFO_TYPE_DFID) {
				continue
			}
			dir, name, ok := parseDirRecord(record, infoType == unix.FAN_EVENT_INFO_TYPE_DFID_NAME)
			if !ok {
				return out, errFanotifyMalformed
			}
			rec.dir, rec.name = dir, name
		}
		out = append(out, rec)
		off += eventLen
	}
	return out, nil
}

// parseDirRecord decodes a DFID or DFID_NAME info record: the info header and
// the fsid (bytes 4-12), the file_handle header (handle_bytes at 12,
// handle_type at 16), the handle, and for DFID_NAME the entry name. ok is
// false when the record is too short for the handle it declares.
func parseDirRecord(record []byte, hasName bool) (dir handleKey, name string, ok bool) {
	if len(record) < fanotifyInfoHead {
		return "", "", false
	}
	handleLen := int(binary.NativeEndian.Uint32(record[12:]))
	if fanotifyInfoHead+handleLen > len(record) {
		return "", "", false
	}
	handleType := binary.NativeEndian.Uint32(record[16:])
	dir = newHandleKey(record[4:12], handleType, record[fanotifyInfoHead:fanotifyInfoHead+handleLen])
	if hasName {
		name = eventName(record[fanotifyInfoHead+handleLen:])
	}
	return dir, name, true
}

func (b *fanotifyBackend) process(buf []byte, moves []fanotifyMove) []fanotifyMove {
	records, err := parseFanotifyEvents(buf)
	for _, rec := range records {
		if rec.fd >= 0 {
			// FID groups never pass descriptors; never leak one if they do.
			_ = unix.Close(int(rec.fd))
		}
		moves = b.handle(rec, moves)
	}
	if err != nil {
		// Events after the fault are lost; treat it like an overflow.
		b.log.Error("librarymonitor: dropping unreadable fanotify events", "component", "librarymonitor", "err", err)
		b.movedOut(moves)
		b.emit(Event{Kind: EventOverflow})
		return nil
	}
	return moves
}

// handle translates one event. The kernel merges queued events on the same
// directory entry into one mask, which loses their order; when a mask holds
// both an arrival and a departure, the entry's current state decides which
// came last.
func (b *fanotifyBackend) handle(rec fanotifyRecord, moves []fanotifyMove) []fanotifyMove {
	mask := rec.mask
	if mask&unix.FAN_Q_OVERFLOW != 0 {
		b.movedOut(moves)
		b.emit(Event{Kind: EventOverflow})
		return nil
	}
	if rec.dir == "" {
		return moves
	}
	if rec.name == "" || rec.name == "." {
		b.handleSelf(rec.dir, mask)
		return moves
	}
	parents := b.parentsOf(rec.dir)
	if len(parents) == 0 {
		return moves // outside every root
	}
	name := rec.name
	isDir := mask&unix.FAN_ONDIR != 0
	arrive := mask&(unix.FAN_CREATE|unix.FAN_MOVED_TO) != 0
	leave := mask&(unix.FAN_DELETE|unix.FAN_MOVED_FROM) != 0
	closeWrite := mask&unix.FAN_CLOSE_WRITE != 0 && !isDir

	if arrive && leave {
		if _, err := os.Lstat(filepath.Join(parents[0].path, name)); err == nil {
			// Present now: the departure came first.
			kind := EventMovedFrom
			if mask&unix.FAN_DELETE != 0 {
				kind = EventDelete
			}
			b.left(kind, parents, name, isDir)
			moves = b.arrival(mask, parents, name, isDir, moves)
			if closeWrite {
				b.closeWritten(parents, name)
			}
			return moves
		}
		// Gone now: the arrival came first.
		kind := EventMovedTo
		if mask&unix.FAN_CREATE != 0 {
			kind = EventCreate
		}
		b.arrived(kind, parents, name, isDir)
		arrive = false
	}
	switch {
	case leave:
		if closeWrite {
			b.closeWritten(parents, name)
		}
		if mask&unix.FAN_MOVED_FROM != 0 {
			return append(moves, b.moveFrom(parents, name, isDir))
		}
		b.left(EventDelete, parents, name, isDir)
	case arrive:
		moves = b.arrival(mask, parents, name, isDir, moves)
		if closeWrite {
			b.closeWritten(parents, name)
		}
	case closeWrite:
		b.closeWritten(parents, name)
	}
	return moves
}

// arrival handles a created or moved-in entry; a moved-in one may complete
// a pending move.
func (b *fanotifyBackend) arrival(mask uint64, parents []fanotifyParent, name string, isDir bool, moves []fanotifyMove) []fanotifyMove {
	if mask&unix.FAN_CREATE != 0 {
		b.arrived(EventCreate, parents, name, isDir)
		return moves
	}
	return b.movedIn(parents, name, isDir, moves)
}

// moveFrom captures a FAN_MOVED_FROM until its FAN_MOVED_TO arrives or the
// read ends.
func (b *fanotifyBackend) moveFrom(parents []fanotifyParent, name string, isDir bool) fanotifyMove {
	mv := fanotifyMove{name: name, isDir: isDir, parents: parents}
	if isDir {
		b.mu.Lock()
		for _, p := range parents {
			if d := b.byPath[filepath.Join(p.path, name)]; d != nil {
				mv.key = d.key
				break
			}
		}
		b.mu.Unlock()
	}
	return mv
}

// movedIn pairs a FAN_MOVED_TO with a pending FAN_MOVED_FROM. A directory
// keeps its handle across a rename, so directory halves pair exactly; file
// halves pair in order, which is equivalent for files because a file rename
// reports the same changes as a move out plus a move in.
func (b *fanotifyBackend) movedIn(parents []fanotifyParent, name string, isDir bool, moves []fanotifyMove) []fanotifyMove {
	match := -1
	if isDir {
		if key, _, err := b.identify(filepath.Join(parents[0].path, name)); err == nil {
			for i, mv := range moves {
				if mv.isDir && mv.key == key {
					match = i
					break
				}
			}
		}
	} else {
		for i, mv := range moves {
			if !mv.isDir {
				match = i
				break
			}
		}
	}
	if match < 0 {
		b.arrived(EventMovedTo, parents, name, isDir)
		return moves
	}
	from := moves[match]
	moves = append(moves[:match:match], moves[match+1:]...)
	b.renamed(from, name, isDir, parents)
	return moves
}

// parentsOf snapshots the logical paths of directory key, with their roots.
func (b *fanotifyBackend) parentsOf(key handleKey) []fanotifyParent {
	b.mu.Lock()
	defer b.mu.Unlock()
	h := b.byHandle[key]
	if h == nil {
		return nil
	}
	parents := make([]fanotifyParent, 0, len(h.paths))
	for p := range h.paths {
		d := b.byPath[p]
		if d == nil {
			continue
		}
		roots := make([]*fanotifyRoot, 0, len(d.roots))
		for r := range d.roots {
			roots = append(roots, r)
		}
		parents = append(parents, fanotifyParent{path: p, roots: roots})
	}
	sort.Slice(parents, func(i, j int) bool { return parents[i].path < parents[j].path })
	return parents
}

// arrived handles a created or moved-in entry. A new directory (or a
// symlink to one, which the scanner follows) is recorded, then listed by
// the walk, before the event is emitted.
func (b *fanotifyBackend) arrived(kind EventKind, parents []fanotifyParent, name string, isDir bool) {
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
// carry no FAN_ONDIR.
func (b *fanotifyBackend) recordedDir(parents []fanotifyParent, name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range parents {
		if b.byPath[filepath.Join(p.path, name)] != nil {
			return true
		}
	}
	return false
}

// left handles a deleted or moved-out entry. A directory's recorded
// subtree is dropped, so no event arrives under a stale path.
func (b *fanotifyBackend) left(kind EventKind, parents []fanotifyParent, name string, isDir bool) {
	isDir = isDir || b.recordedDir(parents, name)
	if isDir {
		var lost []string
		b.mu.Lock()
		for _, p := range parents {
			lost = append(lost, b.dropSubtreeLocked(filepath.Join(p.path, name))...)
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

func (b *fanotifyBackend) closeWritten(parents []fanotifyParent, name string) {
	if ignoredName(name) {
		return
	}
	for _, p := range parents {
		b.emit(Event{Kind: EventCloseWrite, Dir: p.path, Name: name})
	}
}

func (b *fanotifyBackend) movedOut(moves []fanotifyMove) {
	for _, mv := range moves {
		b.left(EventMovedFrom, mv.parents, mv.name, mv.isDir)
	}
}

// renamed handles a move pair within the tree. A moved directory's paths
// are rewritten by re-recording its subtree under the new name: the old
// paths are dropped and the walk records the same handles under the new
// paths and roots. A followed symlink to a directory is handled as a
// directory on each side, as in the inotify backend.
func (b *fanotifyBackend) renamed(from fanotifyMove, name string, isDir bool, parents []fanotifyParent) {
	oldDir := isDir || b.recordedDir(from.parents, from.name)
	newDir := isDir || symlinkToDir(filepath.Join(parents[0].path, name))
	oldIgnored := ignoredName(from.name) || (oldDir && ignoredDir(from.name))
	newIgnored := ignoredName(name) || (newDir && ignoredDir(name))
	if oldDir {
		var lost []string
		b.mu.Lock()
		for _, p := range from.parents {
			lost = append(lost, b.dropSubtreeLocked(filepath.Join(p.path, from.name))...)
		}
		b.mu.Unlock()
		// A root inside the moved directory is gone from its configured
		// path; release it before the walk records the new location.
		b.loseRoots(lost)
	}
	if newDir && !newIgnored {
		for _, p := range parents {
			b.walkRegister(filepath.Join(p.path, name), p.roots)
		}
	}
	emitRename(b.emit,
		renameSide{dirs: fanotifyPaths(from.parents), name: from.name, isDir: oldDir, ignored: oldIgnored},
		renameSide{dirs: fanotifyPaths(parents), name: name, isDir: newDir, ignored: newIgnored})
}

func fanotifyPaths(parents []fanotifyParent) []string {
	paths := make([]string, len(parents))
	for i, p := range parents {
		paths[i] = p.path
	}
	return paths
}

// walkRegister records a subtree that appeared at runtime for roots. A
// directory in it that fanotify cannot identify or mark (for example a new
// btrfs subvolume) would leave a hole, so the affected roots are released
// and reported lost instead; reconcile attaches them again, and fanotify's
// refusal then moves them to inotify.
func (b *fanotifyBackend) walkRegister(dir string, roots []*fanotifyRoot) {
	err := walkTree(b.ctx, dir, walkVisitor{
		enter: func(d string) (bool, error) {
			return b.register(d, roots)
		},
		skip: func(d string) {
			for _, r := range roots {
				b.unregister(d, r)
			}
		},
	})
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, errBackendClosed) {
		return
	}
	var lost []string
	b.mu.Lock()
	for _, r := range roots {
		if b.roots[r.path] == r {
			lost = append(lost, r.path)
		}
	}
	b.mu.Unlock()
	if len(lost) > 0 {
		b.log.Warn("librarymonitor: fanotify can't record a new folder; releasing its library folders",
			"component", "librarymonitor", "path", dir, "err", err)
	}
	b.loseRoots(lost)
}

// handleSelf handles events on a recorded directory itself. Deleting or
// moving a root loses it; the same events on other directories are covered
// by their parent's entry events.
func (b *fanotifyBackend) handleSelf(key handleKey, mask uint64) {
	if mask&(unix.FAN_DELETE_SELF|unix.FAN_MOVE_SELF) == 0 {
		return
	}
	b.mu.Lock()
	var lost []string
	if h := b.byHandle[key]; h != nil {
		for p := range h.paths {
			for r := range b.byPath[p].roots {
				if r.path == p {
					lost = append(lost, r.path)
				}
			}
		}
	}
	b.mu.Unlock()
	b.loseRoots(lost)
}
