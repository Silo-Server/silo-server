//go:build linux

package librarymonitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newTestInotify(t *testing.T, hooks inotifyHooks) *inotifyBackend {
	t.Helper()
	b, err := newInotifyBackend(BackendOptions{Logger: quietLogger()}, hooks)
	if err != nil {
		t.Fatalf("newInotifyBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b.(*inotifyBackend)
}

// nextEvents collects events until one satisfies stop, and returns them all.
func nextEvents(t *testing.T, b Backend, stop func(Event) bool) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-b.Events():
			if !ok {
				t.Fatalf("events closed; got %+v", got)
			}
			got = append(got, ev)
			if stop(ev) {
				return got
			}
		case <-deadline:
			t.Fatalf("timed out; got %+v", got)
		}
	}
}

func isEvent(kind EventKind, dir, name string) func(Event) bool {
	return func(ev Event) bool { return ev.Kind == kind && ev.Dir == dir && ev.Name == name }
}

func (b *inotifyBackend) watchCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.byWD)
}

func (b *inotifyBackend) pathsWithPrefix(prefix string) []string {
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

func TestInotifyOverlappingRootsShareWatches(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "Shows")
	mkdirs(t, outer, "Movies/M", "Shows/S/Season 1")
	b := newTestInotify(t, inotifyHooks{})
	ctx := context.Background()
	if err := b.AddRoot(ctx, outer); err != nil {
		t.Fatal(err)
	}
	if err := b.AddRoot(ctx, inner); err != nil {
		t.Fatal(err)
	}
	if got := b.Directories(outer); got != 6 {
		t.Fatalf("outer directories = %d, want 6", got)
	}
	if got := b.Directories(inner); got != 3 {
		t.Fatalf("inner directories = %d, want 3", got)
	}
	if got := b.watchCount(); got != 6 {
		t.Fatalf("watches = %d, want 6 shared across both roots", got)
	}

	// One event per logical path, even though two roots cover it.
	writeFile(t, filepath.Join(inner, "S", "a.mkv"), "x")
	writeFile(t, filepath.Join(outer, "sentinel"), "x")
	events := nextEvents(t, b, isEvent(EventCloseWrite, outer, "sentinel"))
	count := 0
	for _, ev := range events {
		if ev.Kind == EventCloseWrite && ev.Name == "a.mkv" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d close-writes for a.mkv, want 1: %+v", count, events)
	}

	// Removing the inner root keeps the watches the outer root needs.
	b.RemoveRoot(inner)
	if got := b.watchCount(); got != 6 {
		t.Fatalf("watches after removing the inner root = %d, want 6", got)
	}
	writeFile(t, filepath.Join(inner, "S", "b.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(inner, "S"), "b.mkv"))

	b.RemoveRoot(outer)
	if got := b.watchCount(); got != 0 {
		t.Fatalf("watches after removing both roots = %d, want 0", got)
	}
}

func TestInotifySymlinkRootReportsTheConfiguredPath(t *testing.T) {
	parent := t.TempDir()
	mkdirs(t, parent, "real/Movie")
	root := filepath.Join(parent, "library")
	if err := os.Symlink(filepath.Join(parent, "real"), root); err != nil {
		t.Fatal(err)
	}
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(parent, "real", "Movie", "m.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(root, "Movie"), "m.mkv"))
}

func TestInotifyRenameRewritesDescendantPaths(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Old/Season 1/Extras")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	watches := b.watchCount()
	if err := os.Rename(filepath.Join(root, "Old"), filepath.Join(root, "New")); err != nil {
		t.Fatal(err)
	}
	events := nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRename })
	rename := events[len(events)-1]
	if rename.OldDir != root || rename.OldName != "Old" || rename.Dir != root || rename.Name != "New" || !rename.IsDir {
		t.Fatalf("rename event = %+v", rename)
	}
	if stale := b.pathsWithPrefix(filepath.Join(root, "Old")); len(stale) != 0 {
		t.Fatalf("stale paths after rename: %v", stale)
	}
	if got := b.watchCount(); got != watches {
		t.Fatalf("watches = %d after rename, want %d", got, watches)
	}
	writeFile(t, filepath.Join(root, "New", "Season 1", "Extras", "x.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(root, "New", "Season 1", "Extras"), "x.mkv"))
}

func TestInotifyMovedOutDirectoryDropsItsWatches(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mkdirs(t, root, "Movie/Extras")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	mustRename(t, filepath.Join(root, "Movie"), filepath.Join(outside, "Movie"))
	nextEvents(t, b, isEvent(EventMovedFrom, root, "Movie"))
	if stale := b.pathsWithPrefix(filepath.Join(root, "Movie")); len(stale) != 0 {
		t.Fatalf("stale paths after moving out: %v", stale)
	}
	if got := b.watchCount(); got != 1 {
		t.Fatalf("watches = %d, want only the root's", got)
	}
}

func TestInotifyAliasAcrossRootsReportsBothPaths(t *testing.T) {
	parent := t.TempDir()
	mkdirs(t, parent, "a/Shared", "b")
	rootA := filepath.Join(parent, "a")
	rootB := filepath.Join(parent, "b")
	if err := os.Symlink(filepath.Join(rootA, "Shared"), filepath.Join(rootB, "Link")); err != nil {
		t.Fatal(err)
	}
	b := newTestInotify(t, inotifyHooks{})
	for _, root := range []string{rootA, rootB} {
		if err := b.AddRoot(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(rootA, "Shared", "x.mkv"), "x")
	events := nextEvents(t, b, func(ev Event) bool { return ev.Name == "x.mkv" && ev.Dir == filepath.Join(rootB, "Link") })
	if first := events[0]; first.Dir != filepath.Join(rootA, "Shared") {
		t.Fatalf("events = %+v, want the canonical path reported too", events)
	}
	b.RemoveRoot(rootA)
	writeFile(t, filepath.Join(rootA, "Shared", "y.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(rootB, "Link"), "y.mkv"))
}

func TestInotifyWatchLimitDuringInitialWalk(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A/1", "A/2", "B")
	added := 0
	b := newTestInotify(t, inotifyHooks{
		addWatch: func(fd int, path string, mask uint32) (int, error) {
			if added == 2 {
				return -1, unix.ENOSPC
			}
			added++
			return unix.InotifyAddWatch(fd, path, mask)
		},
		maxUserWatches: func() int { return 2 },
	})
	err := b.AddRoot(context.Background(), root)
	var limitErr *WatchLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("AddRoot error = %v, want *WatchLimitError", err)
	}
	if limitErr.Limit != 2 || limitErr.Directories != 5 {
		t.Fatalf("limit error = %+v, want limit 2 and all 5 directories counted", limitErr)
	}
	if b.watchCount() != 0 || b.Directories(root) != 0 {
		t.Fatalf("watches kept after the limit: %d", b.watchCount())
	}
}

func TestInotifyWatchLimitAtRuntimeReleasesTheRoot(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Movie")
	b := newTestInotify(t, inotifyHooks{
		addWatch: func(fd int, path string, mask uint32) (int, error) {
			if strings.HasSuffix(path, "New") {
				return -1, unix.ENOSPC
			}
			return unix.InotifyAddWatch(fd, path, mask)
		},
		maxUserWatches: func() int { return 99 },
	})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, root, "New")
	events := nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventLimitReached })
	ev := events[len(events)-1]
	var limitErr *WatchLimitError
	if ev.Root != root || !errors.As(ev.Err, &limitErr) || limitErr.Limit != 99 {
		t.Fatalf("limit event = %+v", ev)
	}
	if b.watchCount() != 0 {
		t.Fatalf("watches kept after the limit: %d", b.watchCount())
	}
}

func TestInotifyNestedRootLostThroughItsParent(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "Shows")
	mkdirs(t, outer, "Shows/S")
	b := newTestInotify(t, inotifyHooks{})
	for _, root := range []string{outer, inner} {
		if err := b.AddRoot(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(inner, filepath.Join(outer, "Renamed")); err != nil {
		t.Fatal(err)
	}
	nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRootLost && ev.Root == inner })
	if got := b.Directories(inner); got != 0 {
		t.Fatalf("lost root still records %d directories", got)
	}
	writeFile(t, filepath.Join(outer, "Renamed", "S", "x.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(outer, "Renamed", "S"), "x.mkv"))
}

func TestInotifyRootDeletedIsLost(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "media")
	mkdirs(t, parent, "media/Movie")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRootLost && ev.Root == root })
	if b.watchCount() != 0 {
		t.Fatalf("watches kept for a lost root: %d", b.watchCount())
	}
}

// A directory created under a root while the root's walk is still running
// can hit the watch limit; the backend then releases the root, and the walk
// must fail with the limit instead of reporting a root it no longer holds.
func TestInotifyRuntimeLimitDuringAWalkFailsTheWalk(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A", "Z")
	atZ, releaseZ := make(chan struct{}), make(chan struct{})
	var once sync.Once
	b := newTestInotify(t, inotifyHooks{
		addWatch: func(fd int, path string, mask uint32) (int, error) {
			switch path {
			case filepath.Join(root, "Z"):
				once.Do(func() { close(atZ) })
				<-releaseZ
			case filepath.Join(root, "A", "new"):
				return -1, unix.ENOSPC
			}
			return unix.InotifyAddWatch(fd, path, mask)
		},
		maxUserWatches: func() int { return 42 },
	})
	added := make(chan error, 1)
	go func() { added <- b.AddRoot(context.Background(), root) }()
	<-atZ
	mkdirs(t, root, "A/new")
	nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventLimitReached && ev.Root == root })
	close(releaseZ)

	err := <-added
	var limitErr *WatchLimitError
	if !errors.As(err, &limitErr) || limitErr.Limit != 42 {
		t.Fatalf("AddRoot error = %v, want the watch limit", err)
	}
	if b.watchCount() != 0 || b.Directories(root) != 0 {
		t.Fatalf("%d watches kept for a released root", b.watchCount())
	}
}

// A symlink to a directory is walked and recorded like one, but its events
// carry no IN_ISDIR. Renaming or deleting it must still move or drop the
// watches recorded under its name.
func TestInotifySymlinkedDirectoryRenamedAndDeleted(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	mkdirs(t, outside, "Target/Sub")
	if err := os.Symlink(filepath.Join(outside, "Target"), filepath.Join(root, "Link")); err != nil {
		t.Fatal(err)
	}
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if got := b.Directories(root); got != 3 {
		t.Fatalf("directories = %d, want the root plus the symlinked folder and its child", got)
	}

	mustRename(t, filepath.Join(root, "Link"), filepath.Join(root, "Link2"))
	events := nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRename })
	if rename := events[len(events)-1]; !rename.IsDir || rename.OldName != "Link" || rename.Name != "Link2" {
		t.Fatalf("rename event = %+v, want a directory rename", rename)
	}
	if stale := b.pathsWithPrefix(filepath.Join(root, "Link")); len(stale) != 0 {
		t.Fatalf("stale paths after renaming the symlink: %v", stale)
	}
	writeFile(t, filepath.Join(outside, "Target", "Sub", "x.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(root, "Link2", "Sub"), "x.mkv"))

	if err := os.Remove(filepath.Join(root, "Link2")); err != nil {
		t.Fatal(err)
	}
	events = nextEvents(t, b, isEvent(EventDelete, root, "Link2"))
	if del := events[len(events)-1]; !del.IsDir {
		t.Fatalf("delete event = %+v, want a vanished directory", del)
	}
	if stale := b.pathsWithPrefix(filepath.Join(root, "Link2")); len(stale) != 0 {
		t.Fatalf("stale paths after deleting the symlink: %v", stale)
	}
	if got := b.watchCount(); got != 1 {
		t.Fatalf("watches = %d, want only the root's", got)
	}
}

// A symlink created at runtime that points onto a network filesystem is not
// walked: it is reported like a file, and none of its target is watched.
func TestInotifyRuntimeSymlinkOntoNetworkMountIsNotWalked(t *testing.T) {
	root, share := t.TempDir(), t.TempDir()
	mkdirs(t, share, "Movie/Extras")
	setWalkMounts(t, map[string]bool{share: true})
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join(share, "Movie"), filepath.Join(root, "Linked Movie")); err != nil {
		t.Fatal(err)
	}
	events := nextEvents(t, b, isEvent(EventCreate, root, "Linked Movie"))
	if created := events[len(events)-1]; created.IsDir {
		t.Fatalf("create event = %+v, want the link reported as a file", created)
	}
	if got := b.watchCount(); got != 1 {
		t.Fatalf("watches = %d, want only the root's", got)
	}
}
