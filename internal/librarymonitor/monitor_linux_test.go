//go:build linux

package librarymonitor

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakeMonitor starts a monitor whose roots all use one fake backend.
func fakeMonitor(t *testing.T, folders *fakeFolders, mutate func(*Config)) (*Monitor, *fakeBackend, *fakeQueue, *fakeStatus) {
	t.Helper()
	b := newFakeBackend("inotify")
	queue := newFakeQueue()
	status := newFakeStatus()
	cfg := testConfig(folders, queue, status)
	cfg.hooks.primary = func(BackendOptions) (Backend, error) { return b, nil }
	if mutate != nil {
		mutate(&cfg)
	}
	return startMonitor(t, cfg), b, queue, status
}

func rowIDs(rows []LibraryStatus) []int {
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.LibraryID)
	}
	return ids
}

func onlyMonitoring(ids ...int) func([]LibraryStatus) bool {
	return func(rows []LibraryStatus) bool {
		if !slices.Equal(rowIDs(rows), ids) {
			return false
		}
		for _, row := range rows {
			if row.State != StateMonitoring {
				return false
			}
		}
		return true
	}
}

func TestReconcileFollowsLibrariesAndTheServerSwitch(t *testing.T) {
	rootA, rootB, rootC := t.TempDir(), t.TempDir(), t.TempDir()
	disabled := library(3, t.TempDir())
	disabled.Enabled = false
	optedOut := library(4, t.TempDir())
	optedOut.RealtimeMonitoring = false
	folders := &fakeFolders{}
	libB := library(2, rootB, rootC)
	folders.set(
		library(1, rootA),
		libB,
		disabled,
		optedOut,
		library(5, filepath.Join(t.TempDir(), "not-on-this-node")),
	)
	m, b, _, status := fakeMonitor(t, folders, nil)

	rows := waitStatus(t, status, "libraries 1 and 2 monitoring over all roots", func(rows []LibraryStatus) bool {
		row, _ := statusOf(rows, 2)
		return onlyMonitoring(1, 2)(rows) && row.Directories == 6
	})
	if row, _ := statusOf(rows, 2); row.Backend != "inotify" {
		t.Fatalf("library 2 row = %+v, want inotify", row)
	}
	for _, root := range []string{disabled.Paths[0], optedOut.Paths[0]} {
		if b.addCount(root) != 0 {
			t.Fatalf("root %s of a library without monitoring was recorded", root)
		}
	}

	libB.RealtimeMonitoring = false
	folders.set(library(1, rootA), libB)
	m.Poke()
	waitCall(t, b, "remove "+rootB, "remove "+rootC)
	waitStatus(t, status, "only library 1", onlyMonitoring(1))

	m.SetServerEnabled(false)
	waitCall(t, b, "remove "+rootA)
	waitStatus(t, status, "no rows with the server switch off", onlyMonitoring())

	m.SetServerEnabled(true)
	waitCall(t, b, "add "+rootA)
	waitStatus(t, status, "library 1 back", onlyMonitoring(1))

	m.Stop()
	status.mu.Lock()
	defer status.mu.Unlock()
	if !slices.Equal(status.removed, []string{"node-test"}) {
		t.Fatalf("RemoveNode calls = %v, want one for node-test", status.removed)
	}
}

func TestDuplicateRootIsRecordedOnceForBothLibraries(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root), library(2, root))
	_, b, _, status := fakeMonitor(t, folders, nil)
	waitStatus(t, status, "both libraries monitoring", onlyMonitoring(1, 2))
	if n := b.addCount(root); n != 1 {
		t.Fatalf("AddRoot called %d times for a shared root, want 1", n)
	}
}

func TestWalksRunOneAtATimeInSortOrder(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	libFirst := library(9, first)
	libFirst.SortOrder = 1
	libSecond := library(1, second)
	libSecond.SortOrder = 2
	folders := &fakeFolders{}
	folders.set(libSecond, libFirst)

	b := newFakeBackend("inotify")
	release := make(chan struct{})
	b.block[first] = release
	queue, status := newFakeQueue(), newFakeStatus()
	cfg := testConfig(folders, queue, status)
	cfg.hooks.primary = func(BackendOptions) (Backend, error) { return b, nil }
	startMonitor(t, cfg)

	if call := <-b.calls; call != "add "+first {
		t.Fatalf("first walk = %q, want the library with the lowest sort order", call)
	}
	close(release)
	waitCall(t, b, "add "+second)
	waitStatus(t, status, "both monitoring", onlyMonitoring(9, 1))
}

func TestStalledWalkStopsHoldingOthersBack(t *testing.T) {
	hung, other := t.TempDir(), t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, hung), library(2, other))
	// Blocked before the monitor starts, or the walk can finish first.
	b := newFakeBackend("inotify")
	b.block[hung] = make(chan struct{}) // never released
	_, _, _, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.WalkStall = 30 * time.Millisecond
		cfg.hooks.primary = func(BackendOptions) (Backend, error) { return b, nil }
	})

	waitCall(t, b, "add "+other)
	rows := waitStatus(t, status, "library 2 monitoring", hasState(2, StateMonitoring))
	if row, _ := statusOf(rows, 1); row.State != StateStarting {
		t.Fatalf("hung library row = %+v, want starting", row)
	}
}

func TestRemovingARootCancelsItsWalk(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root))
	b := newFakeBackend("inotify")
	b.block[root] = make(chan struct{})
	m, _, _, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.primary = func(BackendOptions) (Backend, error) { return b, nil }
	})
	waitCall(t, b, "add "+root)

	folders.set()
	m.Poke()
	waitCall(t, b, "remove "+root)
	waitStatus(t, status, "no rows", onlyMonitoring())
}

func TestOverflowRewalksAndQueuesOneLibraryScanPerLibrary(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, rootA), library(2, rootB))
	_, b, queue, status := fakeMonitor(t, folders, nil)
	waitStatus(t, status, "monitoring", onlyMonitoring(1, 2))
	drain(b.calls)

	b.events <- Event{Kind: EventOverflow}
	waitCall(t, b, "add "+rootA, "add "+rootB)
	if b.addCount(rootA) != 2 || b.addCount(rootB) != 2 {
		t.Fatalf("add counts = %d, %d, want a re-walk of each root", b.addCount(rootA), b.addCount(rootB))
	}
	got := waitTargets(t, queue, "1 library  realtime_monitor", "2 library  realtime_monitor")
	if len(got) != 2 {
		t.Fatalf("enqueued %v, want exactly the two library scans", got)
	}
}

// A root whose first walk is still running already gets events; an overflow
// then may have hidden a directory created during the walk. The root must be
// walked again once the walk ends, and its library rescanned.
func TestOverflowDuringTheFirstWalkRewalksAndRescans(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root))
	b := newFakeBackend("inotify")
	release := make(chan struct{})
	b.block[root] = release
	queue, status := newFakeQueue(), newFakeStatus()
	cfg := testConfig(folders, queue, status)
	cfg.hooks.primary = func(BackendOptions) (Backend, error) { return b, nil }
	startMonitor(t, cfg)
	waitCall(t, b, "add "+root)

	b.events <- Event{Kind: EventOverflow}
	waitTargets(t, queue, "1 library  realtime_monitor")
	close(release)
	waitCall(t, b, "add "+root)
	waitStatus(t, status, "monitoring after the second walk", onlyMonitoring(1))
	if n := b.addCount(root); n != 2 {
		t.Fatalf("AddRoot called %d times, want the first walk and a re-walk", n)
	}
}

// RemoveRoot can hang on a dead mount. It must not run under the monitor's
// lock, which the status loop and Stop take.
func TestHungRemoveRootDoesNotHoldTheMonitorLock(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, rootA), library(2, rootB))
	m, b, _, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.stopWait = 20 * time.Millisecond
	})
	waitStatus(t, status, "monitoring", onlyMonitoring(1, 2))
	hung := make(chan struct{})
	t.Cleanup(func() { close(hung) })
	b.mu.Lock()
	b.removeBlock[rootA] = hung
	b.mu.Unlock()

	folders.set(library(2, rootB))
	m.Poke()
	waitCall(t, b, "removing "+rootA)
	waitStatus(t, status, "library 1 gone while its release hangs", onlyMonitoring(2))
	stopped := make(chan struct{})
	go func() {
		m.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(waitTimeout):
		t.Fatal("Stop waited for a hung RemoveRoot")
	}
}

// A release collected by one attempt runs after the monitor's lock is
// dropped. An attempt started in that gap must not record the root before
// the release ran, or the late release would drop the fresh walk.
func TestAttemptWaitsForAnEarlierRelease(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root))
	attempts := make(chan State, 16)
	m, b, _, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.afterAttempt = func(_ string, state State) { attempts <- state }
		// The hung attempt's walk ticket stops holding the next one back
		// at once, so only the pending release can.
		cfg.WalkStall = time.Millisecond
	})
	waitStatus(t, status, "monitoring", onlyMonitoring(1))
	<-attempts
	drain(b.calls)

	// A re-walk fails, so its attempt detaches the root; that release hangs.
	hung := make(chan struct{})
	var unhang sync.Once
	t.Cleanup(func() { unhang.Do(func() { close(hung) }) })
	b.mu.Lock()
	b.addErr[root] = errors.New("walk failed")
	b.removeBlock[root] = hung
	b.mu.Unlock()
	b.events <- Event{Kind: EventRewalk, Root: root}
	waitCall(t, b, "removing "+root)

	b.mu.Lock()
	delete(b.addErr, root)
	b.mu.Unlock()
	m.Poke()
	select {
	case call := <-b.calls:
		t.Fatalf("backend call %q while an earlier release of the root was pending", call)
	case <-time.After(200 * time.Millisecond):
	}
	unhang.Do(func() { close(hung) })
	waitCall(t, b, "remove "+root, "add "+root)
	waitStatus(t, status, "monitoring again", onlyMonitoring(1))
	if n := b.Directories(root); n == 0 {
		t.Fatal("the late release dropped the new walk")
	}
}

func TestRootLostReportsUnavailableAndQueuesNothing(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "media")
	mkdirs(t, parent, "media/Movie")
	folders := &fakeFolders{}
	folders.set(library(1, root))
	attempts := make(chan State, 16)
	m, b, queue, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.afterAttempt = func(_ string, state State) { attempts <- state }
	})
	waitStatus(t, status, "monitoring", onlyMonitoring(1))
	<-attempts

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	b.events <- Event{Kind: EventDelete, Dir: root, Name: "Movie", IsDir: true}
	b.events <- Event{Kind: EventRootLost, Root: root}
	waitStatus(t, status, "root unavailable", hasState(1, StateRootUnavailable))

	// Still missing: stays unavailable instead of disappearing.
	m.Poke()
	if state := <-attempts; state != StateRootUnavailable {
		t.Fatalf("retry of a missing root ended %q, want root_unavailable", state)
	}
	m.mu.Lock()
	rows := m.statusRowsLocked()
	m.mu.Unlock()
	if !hasState(1, StateRootUnavailable)(rows) {
		t.Fatalf("rows after retry = %+v", rows)
	}
	select {
	case batch := <-queue.receipts:
		t.Fatalf("queued %v for a lost root", batchKeys(batch))
	default:
	}

	mkdirs(t, parent, "media")
	m.Poke()
	waitStatus(t, status, "monitoring again", onlyMonitoring(1))
}

func TestUnsupportedAndFuseFilesystems(t *testing.T) {
	nfs, fuse := t.TempDir(), t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, nfs), library(2, fuse))
	_, b, _, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.classify = func(path string) (fsClass, error) {
			if path == nfs {
				return classifyFSType(0x6969), nil
			}
			return classifyFSType(0x65735546), nil
		}
	})
	rows := waitStatus(t, status, "classified", func(rows []LibraryStatus) bool {
		return hasState(1, StateUnsupportedFilesystem)(rows) && hasState(2, StateMonitoring)(rows)
	})
	unsupported, _ := statusOf(rows, 1)
	// Single-folder libraries: the detail needs no path prefix.
	if !strings.HasPrefix(unsupported.Detail, "NFS network filesystems") {
		t.Fatalf("unsupported detail = %q, want it to name NFS", unsupported.Detail)
	}
	caveat, _ := statusOf(rows, 2)
	if caveat.Detail != fuseCaveat {
		t.Fatalf("FUSE detail = %q", caveat.Detail)
	}
	if b.addCount(nfs) != 0 {
		t.Fatal("an unsupported filesystem was walked")
	}
}

func TestMonitoredRootReportsInotify(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root))
	_, _, _, status := fakeMonitor(t, folders, nil)
	rows := waitStatus(t, status, "monitoring", onlyMonitoring(1))
	if rows[0].Backend != "inotify" || rows[0].Detail != "" {
		t.Fatalf("row = %+v, want inotify with no detail", rows[0])
	}
}

func TestWatchLimitReleasesOnlyThatLibrary(t *testing.T) {
	tight, roomy := t.TempDir(), t.TempDir()
	mkdirs(t, tight, "A/1", "A/2", "B/1", "B/2")
	mkdirs(t, roomy, "X/1")
	folders := &fakeFolders{}
	folders.set(library(1, tight), library(2, roomy))

	var limit atomic.Int64
	limit.Store(1234)
	var watchCalls atomic.Int64
	var mu sync.Mutex
	failTight := true
	queue, status := newFakeQueue(), newFakeStatus()
	reconciled := make(chan struct{}, 16)
	cfg := testConfig(folders, queue, status)
	cfg.hooks.afterReconcile = func() { reconciled <- struct{}{} }
	cfg.hooks.inotify = inotifyHooks{
		addWatch: func(fd int, path string, mask uint32) (int, error) {
			watchCalls.Add(1)
			mu.Lock()
			fail := failTight && strings.HasPrefix(path, filepath.Join(tight, "B"))
			mu.Unlock()
			if fail {
				return -1, unix.ENOSPC
			}
			return unix.InotifyAddWatch(fd, path, mask)
		},
		maxUserWatches: func() int { return int(limit.Load()) },
	}
	m := startMonitor(t, cfg)

	rows := waitStatus(t, status, "limit reached", func(rows []LibraryStatus) bool {
		return hasState(1, StateLimitReached)(rows) && hasState(2, StateMonitoring)(rows)
	})
	row, _ := statusOf(rows, 1)
	if row.Directories != 7 || row.Backend != "inotify" || !strings.Contains(row.Detail, "(1234)") {
		t.Fatalf("limit row = %+v, want all 7 directories counted and the limit named", row)
	}
	if n := m.primary.Directories(tight); n != 0 {
		t.Fatalf("%d watches kept for a library over the limit, want 0", n)
	}

	// Unchanged limit: no retry.
	drain(reconciled)
	adds := watchCalls.Load()
	m.Poke()
	<-reconciled
	m.mu.Lock()
	rows = m.statusRowsLocked()
	m.mu.Unlock()
	if !hasState(1, StateLimitReached)(rows) || watchCalls.Load() != adds {
		t.Fatalf("an unchanged limit was retried: rows %+v", rows)
	}

	mu.Lock()
	failTight = false
	mu.Unlock()
	limit.Store(100_000)
	m.Poke()
	rows = waitStatus(t, status, "recovered after the limit was raised", onlyMonitoring(1, 2))
	if row, _ := statusOf(rows, 1); row.Directories != 7 {
		t.Fatalf("recovered row = %+v", row)
	}
}

// waitTargets waits until every wanted target key has been enqueued and
// returns all keys received.
func waitTargets(t *testing.T, q *fakeQueue, want ...string) []string {
	t.Helper()
	missing := make(map[string]bool, len(want))
	for _, key := range want {
		missing[key] = true
	}
	var got []string
	deadline := time.After(waitTimeout)
	for len(missing) > 0 {
		select {
		case batch := <-q.receipts:
			for _, target := range batch {
				key := targetKey(target)
				got = append(got, key)
				delete(missing, key)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for targets %v; got %v", keys(missing), got)
		}
	}
	return got
}

func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func drain[T any](ch chan T) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// The reviewer's case: while a root's first walk is still running, a folder
// created in a part already walked hits the watch limit. The root must end
// limit_reached with no watches, never monitoring with nothing recorded.
func TestRuntimeLimitDuringTheFirstWalkEndsLimitReached(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A", "Z")
	folders := &fakeFolders{}
	folders.set(library(1, root))
	atZ, releaseZ := make(chan struct{}), make(chan struct{})
	var once sync.Once
	attempts := make(chan State, 16)
	queue, status := newFakeQueue(), newFakeStatus()
	cfg := testConfig(folders, queue, status)
	cfg.hooks.afterAttempt = func(_ string, state State) { attempts <- state }
	cfg.hooks.inotify = inotifyHooks{
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
	}
	m := startMonitor(t, cfg)

	<-atZ
	mkdirs(t, root, "A/new")
	// The limit event reaches the monitor while the walk is still held.
	waitStatus(t, status, "limit reached while walking", hasState(1, StateLimitReached))
	close(releaseZ)
	if state := <-attempts; state != StateLimitReached {
		t.Fatalf("first attempt ended %q, want limit_reached", state)
	}
	m.mu.Lock()
	rows := m.statusRowsLocked()
	primary := m.primary
	m.mu.Unlock()
	if row, _ := statusOf(rows, 1); row.State != StateLimitReached || !strings.Contains(row.Detail, "(42)") {
		t.Fatalf("row = %+v, want limit_reached naming the limit", row)
	}
	if n := primary.Directories(root); n != 0 {
		t.Fatalf("%d directories recorded for a root over the limit", n)
	}
}

// An overflow re-walk that races a runtime limit hit must not overwrite
// limit_reached with its own "monitoring".
func TestLimitHitDuringARewalkWins(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root))
	attempts := make(chan State, 16)
	_, b, _, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.afterAttempt = func(_ string, state State) { attempts <- state }
	})
	waitStatus(t, status, "monitoring", onlyMonitoring(1))
	<-attempts
	drain(b.calls)

	release := make(chan struct{})
	b.mu.Lock()
	b.block[root] = release
	b.mu.Unlock()
	b.events <- Event{Kind: EventOverflow}
	waitCall(t, b, "add "+root)
	b.events <- Event{Kind: EventLimitReached, Root: root, Err: &WatchLimitError{Limit: 7, Directories: 9}}
	waitStatus(t, status, "limit reached during the re-walk", hasState(1, StateLimitReached))
	close(release)

	if state := <-attempts; state != StateLimitReached {
		t.Fatalf("re-walk ended %q, want limit_reached", state)
	}
	waitCall(t, b, "remove "+root)
}

func TestRewalkEventWalksAgainAndQueuesNothing(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root))
	walks := make(chan string, 16)
	_, b, queue, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.afterWalk = func(path, _ string) { walks <- path }
	})
	waitStatus(t, status, "monitoring", onlyMonitoring(1))
	<-walks

	b.events <- Event{Kind: EventRewalk, Root: root}
	<-walks
	if n := b.addCount(root); n != 2 {
		t.Fatalf("AddRoot called %d times, want a second walk", n)
	}
	select {
	case batch := <-queue.receipts:
		t.Fatalf("a re-walk queued %v", batchKeys(batch))
	default:
	}
}

// fakeMountTable is a mutable mount table for the mounts hook.
type fakeMountTable struct {
	mu      sync.Mutex
	entries []mountEntry
}

func (f *fakeMountTable) set(entries ...mountEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = entries
}

func (f *fakeMountTable) read() ([]mountEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mountEntry(nil), f.entries...), nil
}

func TestMountChangesBelowARootWalkItAgain(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root))
	table := &fakeMountTable{}
	base := mountEntry{id: 1, dev: "8:1", point: "/", fsType: "ext4"}
	table.set(base)
	walks := make(chan string, 16)
	attempts := make(chan State, 16)
	m, b, _, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.mounts = table.read
		cfg.hooks.afterWalk = func(_, mounts string) { walks <- mounts }
		cfg.hooks.afterAttempt = func(_ string, state State) { attempts <- state }
	})
	waitStatus(t, status, "monitoring", onlyMonitoring(1))
	if got := <-walks; got != base.key() {
		t.Fatalf("recorded mounts = %q, want the covering mount", got)
	}

	// A disk mounted inside the root: walked again.
	disk := mountEntry{id: 2, dev: "7:0", point: filepath.Join(root, "disk1"), fsType: "xfs"}
	table.set(base, disk)
	m.Poke()
	if got := <-walks; !strings.Contains(got, disk.key()) {
		t.Fatalf("re-walk recorded %q, want the new mount", got)
	}

	// Nothing changed: the next check keeps the root without a walk.
	drain(attempts)
	m.Poke()
	<-attempts
	if n := b.addCount(root); n != 2 {
		t.Fatalf("AddRoot called %d times, want no walk for an unchanged mount table", n)
	}

	// A network filesystem mounted inside the root is walked around and
	// named in the detail.
	nas := mountEntry{id: 3, dev: "0:50", point: filepath.Join(root, "nas"), fsType: mountTypeNFS4}
	table.set(base, disk, nas)
	m.Poke()
	rows := waitStatus(t, status, "network mount noted", func(rows []LibraryStatus) bool {
		row, ok := statusOf(rows, 1)
		return ok && strings.Contains(row.Detail, "aren't monitored")
	})
	if row, _ := statusOf(rows, 1); !strings.Contains(row.Detail, filepath.Join(root, "nas")+" (NFS)") {
		t.Fatalf("detail = %q, want the NFS mount named", row.Detail)
	}
}

// A stat on a hung mount can hold the event loop; Stop must still return.
func TestStopDoesNotWaitForAStuckEventLoop(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root))
	stuck, unstick := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(unstick) })
	m, b, _, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.stopWait = 20 * time.Millisecond
		cfg.hooks.stat = func(string) (fileState, error) {
			close(stuck)
			<-unstick
			return fileState{}, os.ErrNotExist
		}
	})
	waitStatus(t, status, "monitoring", onlyMonitoring(1))
	b.events <- Event{Kind: EventCreate, Dir: root, Name: "a.mkv"}
	<-stuck

	stopped := make(chan struct{})
	go func() {
		m.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(waitTimeout):
		t.Fatal("Stop waited for an event loop stuck in a stat")
	}
}

// A backend whose event stream ends while the monitor runs (its reader
// failed) watches nothing. Its roots must be recorded again with a new
// backend instead of reporting monitoring over a dead stream.
func TestFailedBackendIsReplaced(t *testing.T) {
	root := t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, root))
	var (
		mu      sync.Mutex
		created []*fakeBackend
	)
	attempts := make(chan State, 16)
	m, _, _, status := fakeMonitor(t, folders, func(cfg *Config) {
		cfg.hooks.primary = func(BackendOptions) (Backend, error) {
			b := newFakeBackend("inotify")
			mu.Lock()
			created = append(created, b)
			mu.Unlock()
			return b, nil
		}
		cfg.hooks.afterAttempt = func(_ string, state State) { attempts <- state }
	})
	waitStatus(t, status, "monitoring", onlyMonitoring(1))
	if state := <-attempts; state != StateMonitoring {
		t.Fatalf("first attempt ended %q, want monitoring", state)
	}
	mu.Lock()
	first := created[0]
	mu.Unlock()
	waitCall(t, first, "add "+root)

	// The status loop reports only rows that changed, and a replacement fast
	// enough to coalesce "starting" away leaves nothing new to report. Wait
	// for the attempt that records the root again instead.
	_ = first.Close() // the reader fails
	deadline := time.After(waitTimeout)
	for state := StateStarting; state != StateMonitoring; {
		select {
		case state = <-attempts:
		case <-deadline:
			t.Fatal("timed out waiting for the root to be recorded again")
		}
	}
	mu.Lock()
	backends := len(created)
	replaced := backends == 2 && created[1].addCount(root) == 1
	mu.Unlock()
	m.mu.Lock()
	rows := m.statusRowsLocked()
	m.mu.Unlock()
	if !replaced || !onlyMonitoring(1)(rows) {
		t.Fatalf("after the backend failed: %d backends, rows %+v; want monitoring on a second backend", backends, rows)
	}
}

// A rescan request for a library folder queues one library scan for its
// library and leaves other libraries alone.
func TestRescanRootQueuesALibraryScan(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	folders := &fakeFolders{}
	folders.set(library(1, rootA), library(2, rootB))
	_, b, queue, status := fakeMonitor(t, folders, nil)
	waitStatus(t, status, "monitoring", onlyMonitoring(1, 2))

	b.events <- Event{Kind: EventRescanRoot, Root: rootA}
	if got := waitTargets(t, queue, "1 library  realtime_monitor"); len(got) != 1 {
		t.Fatalf("enqueued %v, want only library 1's scan", got)
	}
}

// A directory Silo could not read is named in the status. Nothing reports a
// permission change on a directory that is not watched, so reconcile checks
// it again and walks the folder once it is readable.
func TestUnreadableFolderIsRecordedOnceReadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}
	root := t.TempDir()
	mkdirs(t, root, "Locked/Inside")
	locked := filepath.Join(root, "Locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	folders := &fakeFolders{}
	folders.set(library(1, root))
	status := newFakeStatus()
	m := startMonitor(t, testConfig(folders, newFakeQueue(), status))
	waitStatus(t, status, "the unreadable folder named", func(rows []LibraryStatus) bool {
		row, ok := statusOf(rows, 1)
		return ok && row.State == StateMonitoring && strings.Contains(row.Detail, locked)
	})

	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	m.Poke()
	waitStatus(t, status, "the folder recorded", func(rows []LibraryStatus) bool {
		row, ok := statusOf(rows, 1)
		return ok && row.State == StateMonitoring && row.Detail == "" && row.Directories == 3
	})
}
