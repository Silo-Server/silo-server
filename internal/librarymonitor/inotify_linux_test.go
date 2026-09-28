//go:build linux

package librarymonitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("closing the inotify backend: %v", err)
		}
	})
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

// A folder with .nomedia keeps its own watch, so removing the marker is seen:
// the folder is then recorded below and reported for a scan. Adding the
// marker again drops what is below it.
func TestInotifyIgnoreMarkerKeepsItsFolderWatched(t *testing.T) {
	root := t.TempDir()
	movie := filepath.Join(root, "Movie")
	mkdirs(t, root, "Movie/Extras")
	writeFile(t, filepath.Join(movie, ".nomedia"), "")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if got := b.pathsWithPrefix(movie); len(got) != 1 {
		t.Fatalf("recorded %v, want only the ignored folder itself", got)
	}

	writeFile(t, filepath.Join(movie, "a.mkv"), "x")
	if err := os.Remove(filepath.Join(movie, ".nomedia")); err != nil {
		t.Fatal(err)
	}
	events := nextEvents(t, b, isEvent(EventMovedTo, root, "Movie"))
	if got := eventStrings(events); len(got) != 1 || !events[0].IsDir {
		t.Fatalf("events = %v, want only the folder reported", got)
	}
	if got := b.pathsWithPrefix(movie); len(got) != 2 {
		t.Fatalf("recorded %v after the marker went, want the folder and Extras", got)
	}
	writeFile(t, filepath.Join(movie, "Extras", "x.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(movie, "Extras"), "x.mkv"))

	writeFile(t, filepath.Join(movie, ".nomedia"), "")
	// The sentinel's event follows the marker's handling.
	writeFile(t, filepath.Join(root, "sentinel"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, root, "sentinel"))
	if got := b.pathsWithPrefix(movie); len(got) != 1 {
		t.Fatalf("recorded %v after the marker came back, want only the folder", got)
	}
}

// A library folder with .nomedia is recorded, so its status does not claim
// monitoring with nothing watched, and removing the marker reports every
// entry in it (the folder's parent is outside the library).
func TestInotifyIgnoreMarkerAtTheLibraryFolder(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Movie")
	writeFile(t, filepath.Join(root, ".nomedia"), "")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if got := b.Directories(root); got != 1 {
		t.Fatalf("directories = %d, want the library folder itself", got)
	}
	if err := os.Remove(filepath.Join(root, ".nomedia")); err != nil {
		t.Fatal(err)
	}
	nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRescanRoot && ev.Root == root })
	if got := b.Directories(root); got != 2 {
		t.Fatalf("directories = %d after the marker went, want 2", got)
	}
}

// The walk records a directory once per root: with Linked -> Target in the
// root, Target is recorded only as Linked. Deleting the link must ask for a
// re-walk, which records Target under its own path.
func TestInotifyDroppedSymlinkAliasAsksForARewalk(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Target/Deep")
	if err := os.Symlink(filepath.Join(root, "Target"), filepath.Join(root, "Linked")); err != nil {
		t.Fatal(err)
	}
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if got := b.pathsWithPrefix(filepath.Join(root, "Target")); len(got) != 0 {
		t.Fatalf("recorded %v, want Target only through Linked", got)
	}
	if err := os.Remove(filepath.Join(root, "Linked")); err != nil {
		t.Fatal(err)
	}
	nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRewalk && ev.Root == root })
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "Target", "Deep", "x.mkv"), "x")
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(root, "Target", "Deep"), "x.mkv"))
}

// A move out of the tree is reported even while other events keep every
// read well inside the move wait.
func TestInotifyMoveOutReportedDuringASteadyStream(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	mkdirs(t, root, "Movie", "Busy")
	writeFile(t, filepath.Join(root, "Movie", "a.mkv"), "x")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		busy := filepath.Join(root, "Busy", "busy.mkv")
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := os.WriteFile(busy, []byte("x"), 0o644); err != nil {
				t.Errorf("writing the busy file: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() {
		close(stop)
		<-stopped
	}()
	nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(root, "Busy"), "busy.mkv"))
	mustRename(t, filepath.Join(root, "Movie", "a.mkv"), filepath.Join(outside, "a.mkv"))
	nextEvents(t, b, isEvent(EventMovedFrom, filepath.Join(root, "Movie"), "a.mkv"))
}

func TestExpireMovesReportsExpiredAndExcessMoves(t *testing.T) {
	now := time.Now()
	type move struct {
		id int
		at time.Time
	}
	var out []int
	movedOut := func(moves []move) {
		for _, mv := range moves {
			out = append(out, mv.id)
		}
	}
	at := func(mv move) time.Time { return mv.at }

	moves := []move{{1, now.Add(-30 * time.Millisecond)}, {2, now.Add(-20 * time.Millisecond)}, {3, now.Add(-time.Millisecond)}}
	kept := expireMoves(moves, now, 20*time.Millisecond, at, movedOut)
	if len(kept) != 1 || kept[0].id != 3 || !slices.Equal(out, []int{1, 2}) {
		t.Fatalf("kept %v, moved out %v; want 3 kept and 1, 2 moved out", kept, out)
	}

	out = nil
	moves = nil
	for i := range maxPendingMoves + 2 {
		moves = append(moves, move{i, now})
	}
	kept = expireMoves(moves, now, time.Hour, at, movedOut)
	if len(kept) != maxPendingMoves || !slices.Equal(out, []int{0, 1}) {
		t.Fatalf("kept %d, moved out %v; want %d kept and the two oldest moved out", len(kept), out, maxPendingMoves)
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

// A recursive copy can write into a new directory before the directory's
// watch exists. The walk of the new directory reports what it finds as Found
// creates, so the tracker waits for those files.
func TestInotifyReportsFilesFoundInANewDirectory(t *testing.T) {
	root := t.TempDir()
	newDir := filepath.Join(root, "New")
	b := newTestInotify(t, inotifyHooks{addWatch: func(fd int, path string, mask uint32) (int, error) {
		if path == newDir {
			// The copy got here first.
			writeFile(t, filepath.Join(newDir, "a.mkv"), "partial")
		}
		return unix.InotifyAddWatch(fd, path, mask)
	}})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, root, "New")
	events := nextEvents(t, b, isEvent(EventCreate, root, "New"))
	found := false
	for _, ev := range events {
		if ev.Kind == EventCreate && ev.Dir == newDir && ev.Name == "a.mkv" && ev.Found {
			found = true
		}
	}
	if !found {
		t.Fatalf("events %v, want a Found create for New/a.mkv", eventStrings(events))
	}
}

// eventLog drains a backend's events in the background, so its reader never
// blocks, and lets a test wait for one.
type eventLog struct {
	mu     sync.Mutex
	events []Event
	added  chan struct{}
}

func drainEvents(b Backend) *eventLog {
	l := &eventLog{added: make(chan struct{}, 1)}
	go func() {
		for ev := range b.Events() {
			l.mu.Lock()
			l.events = append(l.events, ev)
			l.mu.Unlock()
			select {
			case l.added <- struct{}{}:
			default:
			}
		}
	}()
	return l
}

func (l *eventLog) waitFor(t *testing.T, what string, match func(Event) bool) {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		l.mu.Lock()
		for _, ev := range l.events {
			if match(ev) {
				l.mu.Unlock()
				return
			}
		}
		l.mu.Unlock()
		select {
		case <-l.added:
		case <-deadline:
			t.Fatalf("no %s event", what)
		}
	}
}

// When the kernel queue overflows, a directory rename can be lost. The
// re-walk that follows must record the renamed directory even though its
// watch descriptor still carries the old path.
func TestInotifyRewalkAfterALostRenameRecordsTheNewName(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Before/Deep", "flood")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	log := drainEvents(b)

	// Stall the reader and queue more events than the kernel keeps
	// (fs.inotify.max_queued_events), so the rename that follows is dropped.
	// Each file queues a create and a close-write.
	maxQueued := 16384
	if raw, err := os.ReadFile("/proc/sys/fs/inotify/max_queued_events"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			maxQueued = n
		}
	}
	if maxQueued > 200_000 {
		t.Skipf("fs.inotify.max_queued_events=%d is too large to overflow in a test", maxQueued)
	}
	b.mu.Lock()
	for i := range maxQueued/2 + 2000 {
		f, err := os.Create(filepath.Join(root, "flood", fmt.Sprintf("f%05d", i)))
		if err != nil {
			b.mu.Unlock()
			t.Fatal(err)
		}
		_ = f.Close()
	}
	renameErr := os.Rename(filepath.Join(root, "Before"), filepath.Join(root, "After"))
	b.mu.Unlock()
	if renameErr != nil {
		t.Fatal(renameErr)
	}
	log.waitFor(t, "overflow", func(ev Event) bool { return ev.Kind == EventOverflow })

	if err := b.AddRoot(context.Background(), root); err != nil { // the monitor's re-walk
		t.Fatal(err)
	}
	if got := b.pathsWithPrefix(filepath.Join(root, "After")); len(got) != 2 {
		t.Fatalf("recorded %v, want After and After/Deep", got)
	}
	if stale := b.pathsWithPrefix(filepath.Join(root, "Before")); len(stale) != 0 {
		t.Fatalf("stale paths %v", stale)
	}
	writeFile(t, filepath.Join(root, "After", "Deep", "x.mkv"), "x")
	log.waitFor(t, "close-write under After/Deep", isEvent(EventCloseWrite, filepath.Join(root, "After", "Deep"), "x.mkv"))
}

// A symlinked directory's target can move where it really lives, outside
// every watched directory. The backend asks for a re-walk, which drops the
// stale path.
func TestInotifySymlinkTargetMovedOutsideAsksForARewalk(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	mkdirs(t, outside, "Target/Sub")
	if err := os.Symlink(filepath.Join(outside, "Target"), filepath.Join(root, "Link")); err != nil {
		t.Fatal(err)
	}
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	mustRename(t, filepath.Join(outside, "Target"), filepath.Join(outside, "Moved"))
	nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRewalk && ev.Root == root })

	if err := b.AddRoot(context.Background(), root); err != nil { // the monitor's re-walk
		t.Fatal(err)
	}
	if stale := b.pathsWithPrefix(filepath.Join(root, "Link")); len(stale) != 0 {
		t.Fatalf("stale paths after the target moved: %v", stale)
	}
	if got := b.watchCount(); got != 1 {
		t.Fatalf("watches = %d, want only the root's", got)
	}
}

// failWatch makes the kernel refuse to watch the given paths with err.
func failWatch(err error, paths ...string) inotifyHooks {
	return inotifyHooks{addWatch: func(fd int, path string, mask uint32) (int, error) {
		if slices.Contains(paths, path) {
			return -1, err
		}
		return unix.InotifyAddWatch(fd, path, mask)
	}}
}

// A directory Silo may not read cannot be watched. The walk skips it, and
// the root's status names it instead of claiming it.
func TestInotifyUnreadableFolderIsReportedNotClaimed(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Open", "Locked/Inside")
	locked := filepath.Join(root, "Locked")
	b := newTestInotify(t, failWatch(unix.EACCES, locked))
	ctx, skipped := withSkippedPaths(context.Background())
	if err := b.AddRoot(ctx, root); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	if _, unreadable := skipped.lists(); !slices.Equal(unreadable, []string{locked}) {
		t.Fatalf("unreadable = %v, want [%s]", unreadable, locked)
	}
	if got := b.pathsWithPrefix(locked); len(got) != 0 {
		t.Fatalf("recorded %v under the unreadable folder", got)
	}
	if got := b.Directories(root); got != 2 {
		t.Fatalf("directories = %d, want the root and Open", got)
	}
}

// A root that cannot be watched, or a kernel error other than the directory
// vanishing, fails the walk instead of passing as monitored.
func TestInotifyWatchFailuresFailTheWalk(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		sub  bool
	}{
		{name: "unreadable root", err: unix.EACCES},
		{name: "out of kernel memory below the root", err: unix.ENOMEM, sub: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mkdirs(t, root, "Sub")
			target := root
			if tc.sub {
				target = filepath.Join(root, "Sub")
			}
			b := newTestInotify(t, failWatch(tc.err, target))
			err := b.AddRoot(context.Background(), root)
			if !errors.Is(err, tc.err) {
				t.Fatalf("AddRoot error = %v, want one wrapping %v", err, tc.err)
			}
		})
	}
}

// A new directory that cannot be watched at runtime asks for a re-walk, so
// the status names it.
func TestInotifyUnwatchableNewFolderAsksForARewalk(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "Locked")
	b := newTestInotify(t, failWatch(unix.EACCES, locked))
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, root, "Locked")
	nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRewalk && ev.Root == root })
}

// A marker in an outer library folder stops only that folder's walk. A
// library folder configured below it keeps everything it records: markers
// above a library folder do not apply to it, as in the scanner.
func TestInotifyMarkerInAnOuterRootKeepsANestedRoot(t *testing.T) {
	outer := t.TempDir()
	mkdirs(t, outer, "Shows/Show A", "Movies/Movie A")
	nested := filepath.Join(outer, "Shows")
	b := newTestInotify(t, inotifyHooks{})
	for _, root := range []string{outer, nested} {
		if err := b.AddRoot(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}

	// Events are handled in order, so once the file written after the
	// marker is reported, the marker has been handled too.
	writeFile(t, filepath.Join(outer, ".nomedia"), "")
	writeFile(t, filepath.Join(nested, "Show A", "e01.mkv"), "x")
	events := nextEvents(t, b, isEvent(EventCloseWrite, filepath.Join(nested, "Show A"), "e01.mkv"))
	for _, ev := range events {
		if ev.Kind == EventRootLost {
			t.Fatalf("root lost: %s", ev.Root)
		}
	}
	if got := b.pathsWithPrefix(filepath.Join(outer, "Movies")); len(got) != 0 {
		t.Fatalf("outer folder still records %v below its marker", got)
	}
	if got := b.Directories(nested); got != 2 {
		t.Fatalf("nested root records %d directories, want Shows and Show A", got)
	}
}

// Editing the patterns of an ignore file can include or exclude entries
// without excluding the directory, so the directory is reported for a scan.
// The ignore files themselves are never reported.
func TestInotifyIgnoreRuleChangesRescanTheFolder(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Movie A", "Other")
	writeFile(t, filepath.Join(root, "Movie A", ".ignore"), "*.nfo\n")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(root, "Movie A", ".ignore"), "*.mkv\n")
	events := nextEvents(t, b, func(ev Event) bool {
		return ev.Kind == EventMovedTo && ev.Dir == root && ev.Name == "Movie A" && ev.IsDir
	})
	// On the library folder itself, only a library scan applies the rules
	// to entries directly in it.
	writeFile(t, filepath.Join(root, ".siloignore"), "Other\n")
	more := nextEvents(t, b, func(ev Event) bool { return ev.Kind == EventRescanRoot && ev.Root == root })
	for _, ev := range append(events, more...) {
		if ignoreFile(ev.Name) {
			t.Fatalf("an ignore file was reported: %v", eventStrings([]Event{ev}))
		}
	}
}

// A walk lists a directory, then applies what it saw. If a marker changed in
// between and the read loop re-evaluated the directory, the walk's listing
// is stale: the re-evaluation's answer stands, and the walk follows it.
func TestInotifyWalkListingDefersToANewerMarkerCheck(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Dir")
	dir := filepath.Join(root, "Dir")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	skippedNow := func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.byPath[dir].skipped
	}

	seen := b.newMarkerSeen()
	seen.record(dir)
	// The read loop re-evaluates after a marker appeared.
	b.mu.Lock()
	b.byPath[dir].markers++
	b.byPath[dir].skipped = true
	b.mu.Unlock()
	if got := seen.listed(dir, false); !got || !skippedNow() {
		t.Fatalf("stale listing applied: listed = %v, skipped = %v, want both true", got, skippedNow())
	}

	// Without a re-evaluation in between, the walk's listing applies.
	seen = b.newMarkerSeen()
	seen.record(dir)
	if got := seen.listed(dir, false); got || skippedNow() {
		t.Fatalf("listing not applied: listed = %v, skipped = %v, want both false", got, skippedNow())
	}
}

// A marker that now excludes a directory is reported like one that stops
// excluding it, so a scan reconciles the media it hides.
func TestInotifyNewMarkerReportsTheFolder(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Movie A/Extras")
	b := newTestInotify(t, inotifyHooks{})
	if err := b.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "Movie A", ".nomedia"), "")
	nextEvents(t, b, func(ev Event) bool {
		return ev.Kind == EventMovedTo && ev.Dir == root && ev.Name == "Movie A" && ev.IsDir
	})
	if got := b.pathsWithPrefix(filepath.Join(root, "Movie A", "Extras")); len(got) != 0 {
		t.Fatalf("still records %v below the marker", got)
	}
}
