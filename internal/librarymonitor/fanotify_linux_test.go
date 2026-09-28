//go:build linux

package librarymonitor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fanotifyMarkProbe reports why this process cannot use fanotify on dir's
// filesystem, or nil when it can: it places (and drops) a real filesystem
// mark and encodes dir's file handle.
func fanotifyMarkProbe(dir string) error {
	fd, err := unix.FanotifyInit(fanotifyInitFlags, unix.O_RDONLY|unix.O_CLOEXEC)
	if err != nil {
		return fmt.Errorf("fanotify_init: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.FanotifyMark(fd, unix.FAN_MARK_ADD|unix.FAN_MARK_FILESYSTEM, fanotifyMask, unix.AT_FDCWD, dir); err != nil {
		return fmt.Errorf("fanotify_mark: %w", err)
	}
	if _, _, err := (&fanotifyBackend{}).identify(dir); err != nil {
		return fmt.Errorf("name_to_handle_at: %w", err)
	}
	return nil
}

// requireFanotifyMark skips tests that need a real filesystem mark, which
// takes CAP_SYS_ADMIN, Linux 5.9 or newer, and file handle support.
func requireFanotifyMark(t *testing.T, dir string) {
	t.Helper()
	if err := fanotifyMarkProbe(dir); err != nil {
		t.Skipf("cannot place a fanotify filesystem mark on %s (needs CAP_SYS_ADMIN, Linux 5.9+, and file handles): %v", dir, err)
	}
}

// newTestFanotify builds a backend. Unprivileged processes may create the
// group on Linux 5.13+ but cannot place filesystem marks, so tests without
// the capability inject hooks.mark.
func newTestFanotify(t *testing.T, hooks fanotifyHooks) *fanotifyBackend {
	t.Helper()
	b, err := newFanotify(BackendOptions{Logger: quietLogger()}, hooks)
	if err != nil {
		t.Skipf("fanotify_init is unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("closing the fanotify backend: %v", err)
		}
	})
	return b
}

// markCall is one fanotify_mark call seen by recordMarks.
type markCall struct {
	flags uint
	mask  uint64
	path  string
}

type markRecorder struct {
	mu    sync.Mutex
	calls []markCall
	err   error
}

func (m *markRecorder) mark(_ int, flags uint, mask uint64, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, markCall{flags: flags, mask: mask, path: path})
	if flags&unix.FAN_MARK_ADD != 0 {
		return m.err
	}
	return nil
}

func (m *markRecorder) count(flag uint) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.flags&flag != 0 {
			n++
		}
	}
	return n
}

func (b *fanotifyBackend) pathsWithPrefix(prefix string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for p := range b.byPath {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			out = append(out, p)
		}
	}
	return out
}

func (b *fanotifyBackend) keyOf(t *testing.T, dir string) handleKey {
	t.Helper()
	key, _, err := b.identify(dir)
	if err != nil {
		t.Fatalf("identify %s: %v", dir, err)
	}
	return key
}

// kernelSuperblockMarks counts the group's filesystem marks as the kernel
// lists them in fdinfo.
func kernelSuperblockMarks(t *testing.T, fd int) int {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "fanotify sdev:")
}

// fanEvent encodes one FAN_REPORT_DFID_NAME event as the kernel does.
// infoType 0 encodes an event without an info record (overflow).
func fanEvent(mask uint64, key handleKey, infoType uint8, name string) []byte {
	var info []byte
	if infoType != 0 {
		k := []byte(key)
		info = make([]byte, 4, 64)
		info[0] = infoType
		info = append(info, k[:8]...)
		info = binary.NativeEndian.AppendUint32(info, uint32(len(k)-12)) //nolint:gosec // test handles are small.
		info = append(info, k[8:12]...)
		info = append(info, k[12:]...)
		if infoType == unix.FAN_EVENT_INFO_TYPE_DFID_NAME {
			info = append(info, name...)
			info = append(info, 0)
		}
		for len(info)%4 != 0 {
			info = append(info, 0)
		}
		binary.NativeEndian.PutUint16(info[2:], uint16(len(info))) //nolint:gosec // test records are small.
	}
	meta := make([]byte, fanotifyMetadataSize)
	binary.NativeEndian.PutUint32(meta[0:], uint32(len(meta)+len(info))) //nolint:gosec // test events are small.
	meta[4] = unix.FANOTIFY_METADATA_VERSION
	binary.NativeEndian.PutUint16(meta[6:], uint16(len(meta))) //nolint:gosec // constant size.
	binary.NativeEndian.PutUint64(meta[8:], mask)
	binary.NativeEndian.PutUint32(meta[16:], 0xffffffff) // FAN_NOFD
	return append(meta, info...)
}

func dirent(mask uint64, key handleKey, name string) []byte {
	return fanEvent(mask, key, unix.FAN_EVENT_INFO_TYPE_DFID_NAME, name)
}

func joinEvents(events ...[]byte) []byte {
	return bytes.Join(events, nil)
}

// collect returns every event the backend has emitted so far, without
// waiting: process emits synchronously.
func collect(b Backend) []Event {
	var got []Event
	for {
		select {
		case ev := <-b.Events():
			got = append(got, ev)
		default:
			return got
		}
	}
}

func eventStrings(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		s := fmt.Sprintf("%s %s", ev.Kind, filepath.Join(ev.Dir, ev.Name))
		if ev.Kind == EventRename {
			s = fmt.Sprintf("%s %s -> %s", ev.Kind, filepath.Join(ev.OldDir, ev.OldName), filepath.Join(ev.Dir, ev.Name))
		}
		if ev.Root != "" {
			s = fmt.Sprintf("%s %s", ev.Kind, ev.Root)
		}
		if ev.Kind == EventOverflow {
			s = ev.Kind.String()
		}
		out = append(out, s)
	}
	return out
}

func assertEvents(t *testing.T, got []Event, want ...string) {
	t.Helper()
	if strings.Join(eventStrings(got), "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n  %s\nwant:\n  %s", strings.Join(eventStrings(got), "\n  "), strings.Join(want, "\n  "))
	}
}

func TestParseFanotifyEvents(t *testing.T) {
	dirKey := newHandleKey([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 1, []byte{9, 9, 9, 9, 9, 9, 9, 9})
	fileKey := newHandleKey([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 1, []byte{7, 7, 7, 7, 7, 7, 7, 7})
	buf := joinEvents(
		dirent(unix.FAN_CREATE|unix.FAN_ONDIR, dirKey, "Movie"),
		fanEvent(unix.FAN_DELETE_SELF|unix.FAN_ONDIR, dirKey, unix.FAN_EVENT_INFO_TYPE_DFID, ""),
		fanEvent(unix.FAN_DELETE_SELF, fileKey, unix.FAN_EVENT_INFO_TYPE_FID, ""),
		fanEvent(unix.FAN_Q_OVERFLOW, "", 0, ""),
	)
	records, err := parseFanotifyEvents(buf)
	if err != nil {
		t.Fatal(err)
	}
	want := []fanotifyRecord{
		{mask: unix.FAN_CREATE | unix.FAN_ONDIR, fd: -1, dir: dirKey, name: "Movie"},
		{mask: unix.FAN_DELETE_SELF | unix.FAN_ONDIR, fd: -1, dir: dirKey},
		{mask: unix.FAN_DELETE_SELF, fd: -1},
		{mask: unix.FAN_Q_OVERFLOW, fd: -1},
	}
	if len(records) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(records), len(want), records)
	}
	for i := range want {
		if records[i] != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, records[i], want[i])
		}
	}

	truncated := buf[:len(buf)-3]
	if records, err := parseFanotifyEvents(truncated); !errors.Is(err, errFanotifyMalformed) || len(records) != 3 {
		t.Fatalf("truncated buffer: %d records, err %v; want 3 and a malformed error", len(records), err)
	}
}

func TestFanotifyMarkSharedPerFilesystem(t *testing.T) {
	marks := &markRecorder{}
	b := newTestFanotify(t, fanotifyHooks{mark: marks.mark})
	ctx := context.Background()
	parent := t.TempDir()
	mkdirs(t, parent, "Movies/M", "Shows/S", "Gone")
	movies, shows, gone := filepath.Join(parent, "Movies"), filepath.Join(parent, "Shows"), filepath.Join(parent, "Gone")

	for _, root := range []string{movies, shows, gone} {
		if err := b.AddRoot(ctx, root); err != nil {
			t.Fatalf("AddRoot %s: %v", root, err)
		}
	}
	if got := marks.count(unix.FAN_MARK_ADD); got != 1 {
		t.Fatalf("placed %d marks for three roots on one filesystem, want 1", got)
	}
	first := marks.calls[0]
	if first.flags != unix.FAN_MARK_ADD|unix.FAN_MARK_FILESYSTEM || first.mask != fanotifyMask {
		t.Fatalf("mark call = %+v, want a filesystem mark with the fanotify mask", first)
	}
	if first.mask&unix.FAN_MODIFY != 0 {
		t.Fatal("the mark subscribes to FAN_MODIFY")
	}
	if got := b.Directories(movies); got != 2 {
		t.Fatalf("Movies directories = %d, want 2", got)
	}

	b.RemoveRoot(movies)
	b.RemoveRoot(shows)
	if got := marks.count(unix.FAN_MARK_REMOVE); got != 0 {
		t.Fatalf("removed the mark while a root still needs it (%d removals)", got)
	}
	// The last root is gone from disk: the mark is removed through a path
	// that still exists on the same filesystem.
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	b.RemoveRoot(gone)
	if got := marks.count(unix.FAN_MARK_REMOVE); got != 1 {
		t.Fatalf("removals after the last root = %d, want 1", got)
	}
	if last := marks.calls[len(marks.calls)-1]; last.path != parent {
		t.Fatalf("removed the mark through %q, want the surviving parent %q", last.path, parent)
	}
	if len(b.marks) != 0 || len(b.byPath) != 0 || len(b.byHandle) != 0 {
		t.Fatalf("state left after removing every root: %d marks, %d paths, %d handles", len(b.marks), len(b.byPath), len(b.byHandle))
	}

	// A root added again places the mark again.
	if err := b.AddRoot(ctx, movies); err != nil {
		t.Fatal(err)
	}
	if got := marks.count(unix.FAN_MARK_ADD); got != 2 {
		t.Fatalf("marks placed after re-adding a root = %d, want 2", got)
	}

	// A root on another filesystem gets its own mark.
	other := "/dev/shm"
	var a, o unix.Statfs_t
	if unix.Statfs(parent, &a) != nil || unix.Statfs(other, &o) != nil || fsidOf(&a) == fsidOf(&o) || fsidOf(&o) == (fsID{}) {
		t.Log("no second filesystem with an fsid to test against")
		return
	}
	shm, err := os.MkdirTemp(other, "librarymonitor-*")
	if err != nil {
		t.Logf("cannot use %s: %v", other, err)
		return
	}
	t.Cleanup(func() { _ = os.RemoveAll(shm) })
	if err := b.AddRoot(ctx, shm); err != nil {
		t.Fatal(err)
	}
	if got := marks.count(unix.FAN_MARK_ADD); got != 3 {
		t.Fatalf("marks placed with a second filesystem = %d, want 3", got)
	}
	b.RemoveRoot(shm)
	if got := marks.count(unix.FAN_MARK_REMOVE); got != 2 {
		t.Fatalf("removals after dropping the second filesystem's root = %d, want 2", got)
	}
}

func TestFanotifyMarkErrorsFallBackToInotify(t *testing.T) {
	for _, errno := range []unix.Errno{unix.EPERM, unix.EINVAL, unix.EXDEV, unix.EOPNOTSUPP, unix.ENODEV} {
		t.Run(errno.Error(), func(t *testing.T) {
			marks := &markRecorder{err: errno}
			b := newTestFanotify(t, fanotifyHooks{mark: marks.mark})
			root := t.TempDir()
			mkdirs(t, root, "Movie")
			err := b.AddRoot(context.Background(), root)
			if !errors.Is(err, errno) {
				t.Fatalf("AddRoot error = %v, want one wrapping %v", err, errno)
			}
			reason := fallbackReason(err)
			if !strings.HasPrefix(reason, "fanotify unavailable: ") || strings.Contains(reason, err.Error()) {
				t.Fatalf("fallback reason = %q, want a specific one", reason)
			}
			b.RemoveRoot(root)
			if b.Directories(root) != 0 || len(b.marks) != 0 || len(b.byPath) != 0 {
				t.Fatal("a refused root left state behind")
			}

			// Through the monitor: the library runs on inotify, with the
			// reason in its status.
			folders := &fakeFolders{}
			folders.set(library(1, root))
			queue, status := newFakeQueue(), newFakeStatus()
			cfg := testConfig(folders, queue, status)
			cfg.hooks.fanotify = func(opts BackendOptions) (Backend, error) {
				return newFanotify(opts, fanotifyHooks{mark: marks.mark})
			}
			startMonitor(t, cfg)
			rows := waitStatus(t, status, "monitoring", hasState(1, StateMonitoring))
			if rows[0].Backend != BackendInotify || !strings.Contains(rows[0].Detail, reason) {
				t.Fatalf("row = %+v, want inotify with %q", rows[0], reason)
			}
		})
	}
}

// TestFanotifyFallbackWhenMarkFails runs the registered factory against the
// real kernel where this process cannot place a filesystem mark (no
// CAP_SYS_ADMIN, a container's seccomp profile, a btrfs subvolume, a
// filesystem without file handles).
func TestFanotifyFallbackWhenMarkFails(t *testing.T) {
	root := t.TempDir()
	probeErr := fanotifyMarkProbe(root)
	if probeErr == nil {
		t.Skip("this process can place fanotify filesystem marks here")
	}
	t.Logf("fanotify refused: %v", probeErr)
	folders := &fakeFolders{}
	folders.set(library(1, root))
	queue, status := newFakeQueue(), newFakeStatus()
	cfg := testConfig(folders, queue, status)
	startMonitor(t, cfg)
	rows := waitStatus(t, status, "monitoring", hasState(1, StateMonitoring))
	want := fallbackReason(probeErr)
	if rows[0].Backend != BackendInotify || !strings.Contains(rows[0].Detail, want) {
		t.Fatalf("row = %+v, want inotify with %q", rows[0], want)
	}
	t.Logf("status detail: %s", rows[0].Detail)
}

// TestFanotifyEventTranslation feeds kernel-format events carrying real
// directory handles to the backend, so it runs without a filesystem mark.
func TestFanotifyEventTranslation(t *testing.T) {
	setup := func(t *testing.T) (*fanotifyBackend, string, string) {
		b := newTestFanotify(t, fanotifyHooks{mark: (&markRecorder{}).mark})
		parent := t.TempDir()
		root := filepath.Join(parent, "library")
		mkdirs(t, parent, "library/Movie A/Extras", "library/Movie B", "outside/Elsewhere")
		if err := b.AddRoot(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		return b, root, filepath.Join(parent, "outside")
	}

	t.Run("entries in recorded directories use the configured path", func(t *testing.T) {
		b, root, outside := setup(t)
		writeFile(t, filepath.Join(outside, "download.mkv"), "x")
		writeFile(t, filepath.Join(root, "Movie A", "a.mkv"), "x")
		moves := b.process(joinEvents(
			// Outside every root: dropped.
			dirent(unix.FAN_CREATE, b.keyOf(t, outside), "download.mkv"),
			dirent(unix.FAN_CREATE|unix.FAN_ONDIR, b.keyOf(t, outside), "Elsewhere"),
			// A file's own handle (FAN_DELETE_SELF on a file): dropped.
			fanEvent(unix.FAN_DELETE_SELF, b.keyOf(t, filepath.Join(root, "Movie A")), unix.FAN_EVENT_INFO_TYPE_FID, ""),
			dirent(unix.FAN_CREATE, b.keyOf(t, filepath.Join(root, "Movie A")), "a.mkv"),
			dirent(unix.FAN_CLOSE_WRITE, b.keyOf(t, filepath.Join(root, "Movie A")), "a.mkv"),
		), nil)
		if len(moves) != 0 {
			t.Fatalf("pending moves = %+v", moves)
		}
		dir := filepath.Join(root, "Movie A")
		assertEvents(t, collect(b), "create "+filepath.Join(dir, "a.mkv"), "close_write "+filepath.Join(dir, "a.mkv"))
	})

	t.Run("a directory rename rewrites the recorded paths", func(t *testing.T) {
		b, root, _ := setup(t)
		rootKey := b.keyOf(t, root)
		mustRename(t, filepath.Join(root, "Movie A"), filepath.Join(root, "Movie A2"))
		moves := b.process(joinEvents(
			dirent(unix.FAN_MOVED_FROM|unix.FAN_ONDIR, rootKey, "Movie A"),
			dirent(unix.FAN_MOVED_TO|unix.FAN_ONDIR, rootKey, "Movie A2"),
		), nil)
		if len(moves) != 0 {
			t.Fatalf("pending moves = %+v", moves)
		}
		assertEvents(t, collect(b), "rename "+filepath.Join(root, "Movie A")+" -> "+filepath.Join(root, "Movie A2"))
		if stale := b.pathsWithPrefix(filepath.Join(root, "Movie A")); len(stale) != 0 {
			t.Fatalf("stale paths after the rename: %v", stale)
		}
		extras := filepath.Join(root, "Movie A2", "Extras")
		if got := b.pathsWithPrefix(extras); len(got) != 1 {
			t.Fatalf("renamed subtree not recorded: %v", got)
		}
		if got := b.Directories(root); got != 4 {
			t.Fatalf("directories = %d, want 4", got)
		}
		// The moved directory's own events resolve to its new path.
		writeFile(t, filepath.Join(extras, "x.mkv"), "x")
		b.process(dirent(unix.FAN_CLOSE_WRITE, b.keyOf(t, extras), "x.mkv"), nil)
		assertEvents(t, collect(b), "close_write "+filepath.Join(extras, "x.mkv"))
	})

	t.Run("a directory moved out is dropped when the read ends unpaired", func(t *testing.T) {
		b, root, outside := setup(t)
		movie := filepath.Join(root, "Movie A")
		extrasKey := b.keyOf(t, filepath.Join(movie, "Extras"))
		mustRename(t, movie, filepath.Join(outside, "Movie A"))
		moves := b.process(dirent(unix.FAN_MOVED_FROM|unix.FAN_ONDIR, b.keyOf(t, root), "Movie A"), nil)
		if len(moves) != 1 {
			t.Fatalf("pending moves = %d, want 1", len(moves))
		}
		b.movedOut(moves)
		assertEvents(t, collect(b), "moved_from "+movie)
		if stale := b.pathsWithPrefix(movie); len(stale) != 0 {
			t.Fatalf("stale paths after the move out: %v", stale)
		}
		b.process(dirent(unix.FAN_CREATE, extrasKey, "late.mkv"), nil)
		if got := collect(b); len(got) != 0 {
			t.Fatalf("event from a moved-out directory: %v", eventStrings(got))
		}
	})

	t.Run("a directory moved in is recorded", func(t *testing.T) {
		b, root, outside := setup(t)
		mustRename(t, filepath.Join(outside, "Elsewhere"), filepath.Join(root, "Elsewhere"))
		moves := b.process(joinEvents(
			dirent(unix.FAN_MOVED_FROM|unix.FAN_ONDIR, b.keyOf(t, outside), "Elsewhere"),
			dirent(unix.FAN_MOVED_TO|unix.FAN_ONDIR, b.keyOf(t, root), "Elsewhere"),
		), nil)
		if len(moves) != 0 {
			t.Fatalf("pending moves = %+v", moves)
		}
		assertEvents(t, collect(b), "moved_to "+filepath.Join(root, "Elsewhere"))
		if got := b.pathsWithPrefix(filepath.Join(root, "Elsewhere")); len(got) != 1 {
			t.Fatalf("moved-in directory not recorded: %v", got)
		}
	})

	t.Run("merged events settle on the current state", func(t *testing.T) {
		b, root, _ := setup(t)
		dir := filepath.Join(root, "Movie B")
		key := b.keyOf(t, dir)
		// A download written under a temp name and renamed: the kernel
		// merges the temp name's create, close-write, and move-from.
		writeFile(t, filepath.Join(dir, "b.mkv.part"), "x")
		mustRename(t, filepath.Join(dir, "b.mkv.part"), filepath.Join(dir, "b.mkv"))
		// A file created and deleted before the read: gone now.
		// A file deleted and created again: present now.
		writeFile(t, filepath.Join(dir, "again.mkv"), "x")
		moves := b.process(joinEvents(
			dirent(unix.FAN_CREATE|unix.FAN_CLOSE_WRITE|unix.FAN_MOVED_FROM, key, "b.mkv.part"),
			dirent(unix.FAN_MOVED_TO, key, "b.mkv"),
			dirent(unix.FAN_CREATE|unix.FAN_CLOSE_WRITE|unix.FAN_DELETE, key, "gone.mkv"),
			dirent(unix.FAN_DELETE|unix.FAN_CREATE|unix.FAN_CLOSE_WRITE, key, "again.mkv"),
		), nil)
		if len(moves) != 0 {
			t.Fatalf("pending moves = %+v", moves)
		}
		assertEvents(t, collect(b),
			"moved_to "+filepath.Join(dir, "b.mkv"),
			"create "+filepath.Join(dir, "gone.mkv"),
			"close_write "+filepath.Join(dir, "gone.mkv"),
			"delete "+filepath.Join(dir, "gone.mkv"),
			"delete "+filepath.Join(dir, "again.mkv"),
			"create "+filepath.Join(dir, "again.mkv"),
			"close_write "+filepath.Join(dir, "again.mkv"),
		)
	})

	t.Run("a root deleted or moved is lost", func(t *testing.T) {
		b, root, _ := setup(t)
		b.process(fanEvent(unix.FAN_DELETE_SELF|unix.FAN_ONDIR, b.keyOf(t, root), unix.FAN_EVENT_INFO_TYPE_DFID_NAME, "."), nil)
		assertEvents(t, collect(b), "root_lost "+root)
		if got := b.Directories(root); got != 0 {
			t.Fatalf("directories after losing the root = %d", got)
		}
		if len(b.byPath) != 0 || len(b.marks) != 0 {
			t.Fatal("a lost root left state behind")
		}
	})

	t.Run("overflow and unreadable events rescan", func(t *testing.T) {
		b, _, _ := setup(t)
		b.process(fanEvent(unix.FAN_Q_OVERFLOW, "", 0, ""), nil)
		b.process([]byte{1, 2, 3}, nil)
		assertEvents(t, collect(b), "overflow", "overflow")
	})
}

// TestFanotifyIgnoreMarkersAndSymlinkAliases feeds kernel-format events, like
// TestFanotifyEventTranslation, for the cases where a change must reach
// directories the walk did not record.
func TestFanotifyIgnoreMarkersAndSymlinkAliases(t *testing.T) {
	setup := func(t *testing.T, dirs ...string) (*fanotifyBackend, string) {
		b := newTestFanotify(t, fanotifyHooks{mark: (&markRecorder{}).mark})
		root := filepath.Join(t.TempDir(), "library")
		mkdirs(t, root, dirs...)
		return b, root
	}

	t.Run("a folder with .nomedia stays recorded and is re-evaluated", func(t *testing.T) {
		b, root := setup(t, "Movie/Extras")
		movie := filepath.Join(root, "Movie")
		writeFile(t, filepath.Join(movie, ".nomedia"), "")
		if err := b.AddRoot(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		if got := b.pathsWithPrefix(movie); len(got) != 1 {
			t.Fatalf("recorded %v, want only the ignored folder itself", got)
		}
		key := b.keyOf(t, movie)

		writeFile(t, filepath.Join(movie, "a.mkv"), "x")
		if err := os.Remove(filepath.Join(movie, ".nomedia")); err != nil {
			t.Fatal(err)
		}
		b.process(joinEvents(
			dirent(unix.FAN_CREATE|unix.FAN_CLOSE_WRITE, key, "a.mkv"),
			dirent(unix.FAN_DELETE, key, ".nomedia"),
		), nil)
		assertEvents(t, collect(b), "moved_to "+movie)
		if got := b.pathsWithPrefix(movie); len(got) != 2 {
			t.Fatalf("recorded %v after the marker went, want the folder and Extras", got)
		}

		writeFile(t, filepath.Join(movie, ".nomedia"), "")
		b.process(dirent(unix.FAN_CREATE|unix.FAN_CLOSE_WRITE, key, ".nomedia"), nil)
		assertEvents(t, collect(b))
		if got := b.pathsWithPrefix(movie); len(got) != 1 {
			t.Fatalf("recorded %v after the marker came back, want only the folder", got)
		}
	})

	t.Run("a library folder with .nomedia is recorded", func(t *testing.T) {
		b, root := setup(t, "Movie")
		writeFile(t, filepath.Join(root, ".nomedia"), "")
		if err := b.AddRoot(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		if got := b.Directories(root); got != 1 {
			t.Fatalf("directories = %d, want the library folder itself", got)
		}
		if err := os.Remove(filepath.Join(root, ".nomedia")); err != nil {
			t.Fatal(err)
		}
		b.process(dirent(unix.FAN_DELETE, b.keyOf(t, root), ".nomedia"), nil)
		assertEvents(t, collect(b), "moved_to "+filepath.Join(root, "Movie"))
		if got := b.Directories(root); got != 2 {
			t.Fatalf("directories = %d after the marker went, want 2", got)
		}
	})

	t.Run("a dropped symlink alias asks for a re-walk", func(t *testing.T) {
		b, root := setup(t, "Target/Deep")
		linked := filepath.Join(root, "Linked")
		if err := os.Symlink(filepath.Join(root, "Target"), linked); err != nil {
			t.Fatal(err)
		}
		if err := b.AddRoot(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		if got := b.pathsWithPrefix(filepath.Join(root, "Target")); len(got) != 0 {
			t.Fatalf("recorded %v, want Target only through Linked", got)
		}
		if err := os.Remove(linked); err != nil {
			t.Fatal(err)
		}
		b.process(dirent(unix.FAN_DELETE, b.keyOf(t, root), "Linked"), nil)
		assertEvents(t, collect(b), "delete "+linked, "rewalk "+root)
		if err := b.AddRoot(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		if got := b.pathsWithPrefix(filepath.Join(root, "Target")); len(got) != 2 {
			t.Fatalf("recorded %v after the re-walk, want Target and Target/Deep", got)
		}
	})
}

// TestFanotifyHandlesMatchEventDirectories proves on the filesystem holding
// the test's temp directory that name_to_handle_at encodes a directory
// exactly as fanotify reports it in events. The backend's handle map relies
// on this byte-for-byte equality; a filesystem where it fails must use
// inotify. Run it on each filesystem to verify (TMPDIR selects it).
func TestFanotifyHandlesMatchEventDirectories(t *testing.T) {
	base := t.TempDir()
	requireFanotifyMark(t, base)
	var st unix.Statfs_t
	if err := unix.Statfs(base, &st); err != nil {
		t.Fatal(err)
	}

	fd, err := unix.FanotifyInit(fanotifyInitFlags, unix.O_RDONLY|unix.O_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.FanotifyMark(fd, unix.FAN_MARK_ADD|unix.FAN_MARK_FILESYSTEM, fanotifyMask, unix.AT_FDCWD, base); err != nil {
		t.Fatal(err)
	}

	mkdirs(t, base, "Movies/Movie A/Extras", "Shows/Show/Season 01", "real")
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "Movies", "linked")); err != nil {
		t.Fatal(err)
	}
	dirs := []string{
		base,
		filepath.Join(base, "Movies"),
		filepath.Join(base, "Movies", "Movie A"),
		filepath.Join(base, "Movies", "Movie A", "Extras"),
		filepath.Join(base, "Shows", "Show", "Season 01"),
		// A logical path through a symlink identifies the real directory.
		filepath.Join(base, "Movies", "linked"),
	}
	b := &fanotifyBackend{}
	want := make(map[string]handleKey, len(dirs))
	for i, dir := range dirs {
		name := fmt.Sprintf("probe-%d.mkv", i)
		want[name] = b.keyOf(t, dir)
		writeFile(t, filepath.Join(dir, name), "x")
	}

	seen := make(map[string]bool)
	buf := make([]byte, fanotifyReadBuffer)
	deadline := time.Now().Add(waitTimeout)
	for len(seen) < len(want) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out; matched %d of %d directories", len(seen), len(want))
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}} //nolint:gosec // descriptors fit in int32.
		if _, err := unix.Poll(fds, int(remaining/time.Millisecond)); err != nil && !errors.Is(err, unix.EINTR) {
			t.Fatal(err)
		}
		n, err := unix.Read(fd, buf)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		records, err := parseFanotifyEvents(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		for _, rec := range records {
			key, ok := want[rec.name]
			if !ok || rec.mask&unix.FAN_CREATE == 0 {
				continue
			}
			if !bytes.Equal([]byte(rec.dir), []byte(key)) {
				t.Fatalf("%s: event directory handle %x, name_to_handle_at %x", rec.name, rec.dir, key)
			}
			seen[rec.name] = true
		}
	}
	sample := []byte(want["probe-0.mkv"])
	t.Logf("filesystem type %#x: event handles equal name_to_handle_at handles for %d directories (handle type %d, %d bytes, AT_HANDLE_FID %v)",
		st.Type, len(seen), binary.NativeEndian.Uint32(sample[8:]), len(sample)-12, !b.noHandleFID.Load())
}

func TestFanotifyDropsEventsOutsideRoots(t *testing.T) {
	parent := t.TempDir()
	requireFanotifyMark(t, parent)
	root := filepath.Join(parent, "library")
	mkdirs(t, parent, "library/Movie", "downloads/incomplete", "transcode")
	b := newTestFanotify(t, fanotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if got := kernelSuperblockMarks(t, b.fd); got != 1 {
		t.Fatalf("kernel lists %d filesystem marks, want 1", got)
	}

	writeFile(t, filepath.Join(parent, "downloads", "incomplete", "a.mkv"), "x")
	mkdirs(t, parent, "downloads/Movie")
	writeFile(t, filepath.Join(parent, "transcode", "seg.ts"), "x")
	if err := os.Remove(filepath.Join(parent, "downloads", "incomplete", "a.mkv")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "Movie", "sentinel.mkv"), "x")

	events := nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(root, "Movie"), "sentinel.mkv"))
	for _, ev := range events {
		if ev.Dir != root && !strings.HasPrefix(ev.Dir, root+"/") {
			t.Errorf("event outside the library: %+v", ev)
		}
	}
}

func TestFanotifySharedMarkInKernel(t *testing.T) {
	parent := t.TempDir()
	requireFanotifyMark(t, parent)
	mkdirs(t, parent, "Movies/M", "Shows/S")
	movies, shows := filepath.Join(parent, "Movies"), filepath.Join(parent, "Shows")
	b := newTestFanotify(t, fanotifyHooks{})
	ctx := context.Background()
	for _, root := range []string{movies, shows} {
		if err := b.AddRoot(ctx, root); err != nil {
			t.Fatal(err)
		}
	}
	if got := kernelSuperblockMarks(t, b.fd); got != 1 {
		t.Fatalf("kernel lists %d filesystem marks for two roots, want 1", got)
	}

	b.RemoveRoot(movies)
	if got := kernelSuperblockMarks(t, b.fd); got != 1 {
		t.Fatalf("kernel lists %d filesystem marks after removing one root, want 1", got)
	}
	writeFile(t, filepath.Join(shows, "S", "a.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(shows, "S"), "a.mkv"))

	b.RemoveRoot(shows)
	if got := kernelSuperblockMarks(t, b.fd); got != 0 {
		t.Fatalf("kernel lists %d filesystem marks after removing every root, want 0", got)
	}
}

func TestFanotifyRootLost(t *testing.T) {
	parent := t.TempDir()
	requireFanotifyMark(t, parent)
	mkdirs(t, parent, "deleted/Movie", "moved/Movie")
	deleted, moved := filepath.Join(parent, "deleted"), filepath.Join(parent, "moved")
	b := newTestFanotify(t, fanotifyHooks{})
	ctx := context.Background()
	for _, root := range []string{deleted, moved} {
		if err := b.AddRoot(ctx, root); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.RemoveAll(deleted); err != nil {
		t.Fatal(err)
	}
	nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRootLost && ev.Root == deleted })
	mustRename(t, moved, filepath.Join(parent, "renamed"))
	nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRootLost && ev.Root == moved })
	if b.Directories(deleted) != 0 || b.Directories(moved) != 0 {
		t.Fatal("lost roots are still recorded")
	}
	if got := kernelSuperblockMarks(t, b.fd); got != 0 {
		t.Fatalf("kernel lists %d filesystem marks after losing every root, want 0", got)
	}
}

// TestFanotifyIntegrationScenarios runs the backend-agnostic scenario table
// against the real kernel through the registered fanotify factory.
func TestFanotifyIntegrationScenarios(t *testing.T) {
	requireFanotifyMark(t, t.TempDir())
	backend := integrationBackend{
		name: BackendFanotify,
		configure: func(_ *testing.T, cfg *Config) {
			cfg.hooks.noFanotify = false
			cfg.hooks.fanotify = nil
		},
	}
	for _, sc := range integrationScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			runScenario(t, backend, sc)
		})
	}
}

// TestFanotifyNestedSubvolumes covers btrfs subvolumes. A root inside a
// subvolume cannot be marked on kernels that refuse subvolume marks (the
// fallback test covers that with TMPDIR inside a subvolume). Subvolumes
// below a marked root share its superblock mark: their events carry the
// mark's fsid, and btrfs handles name the subvolume, so they are monitored,
// including subvolumes created at runtime. It runs only as root with TMPDIR
// on a btrfs top-level volume.
func TestFanotifyNestedSubvolumes(t *testing.T) {
	const btrfsMagic = 0x9123683e
	root := t.TempDir()
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil || st.Type != btrfsMagic {
		t.Skip("TMPDIR is not on btrfs")
	}
	requireFanotifyMark(t, root)
	btrfs, err := exec.LookPath("btrfs")
	if err != nil {
		t.Skip("the btrfs tool is not installed")
	}
	subvolume := func(path string) {
		t.Helper()
		if out, err := exec.Command(btrfs, "-q", "subvolume", "create", path).CombinedOutput(); err != nil {
			t.Fatalf("btrfs subvolume create: %v: %s", err, out)
		}
	}
	mkdirs(t, root, "Movie")
	existing := filepath.Join(root, "Movie", "Pool")
	subvolume(existing)
	mkdirs(t, existing, "Extras")
	t.Logf("subvolume mark refused: %v", fanotifyMarkProbe(existing))

	b := newTestFanotify(t, fanotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatalf("AddRoot over a nested subvolume: %v", err)
	}
	if got := b.Directories(root); got != 4 {
		t.Fatalf("directories = %d, want 4", got)
	}
	writeFile(t, filepath.Join(existing, "Extras", "a.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(existing, "Extras"), "a.mkv"))

	created := filepath.Join(root, "Movie", "New")
	subvolume(created)
	nextEvents(t, b, isEvent(EventCreate, filepath.Join(root, "Movie"), "New"))
	writeFile(t, filepath.Join(created, "b.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, created, "b.mkv"))
	if got := kernelSuperblockMarks(t, b.fd); got != 1 {
		t.Fatalf("kernel lists %d filesystem marks, want 1", got)
	}
}
