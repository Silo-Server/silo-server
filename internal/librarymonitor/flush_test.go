package librarymonitor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

// newFlushMonitor builds a monitor whose flush is driven directly by the
// test with a fake clock, without starting its loops.
func newFlushMonitor(t *testing.T, resolver *fakeResolver, queue *fakeQueue, libraries ...*models.MediaFolder) *Monitor {
	t.Helper()
	m, err := New(Config{
		NodeID:      "node-test",
		Folders:     &fakeFolders{},
		NewResolver: func(scantrigger.FolderRepository) Resolver { return resolver },
		Queue:       queue,
		Logger:      quietLogger(),
		QuietWindow: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, lib := range libraries {
		m.desired[lib.ID] = lib
	}
	return m
}

func dirTarget(lib *models.MediaFolder, path string) *scantrigger.Target {
	return &scantrigger.Target{Folder: lib, Mode: scantrigger.ModeSubtree, Path: path, Trigger: Trigger}
}

func takeBatch(t *testing.T, q *fakeQueue) []string {
	t.Helper()
	select {
	case batch := <-q.receipts:
		return batchKeys(batch)
	default:
		t.Fatal("no enqueue happened")
		return nil
	}
}

func assertNoBatch(t *testing.T, q *fakeQueue) {
	t.Helper()
	select {
	case batch := <-q.receipts:
		t.Fatalf("unexpected enqueue %v", batchKeys(batch))
	default:
	}
}

func TestFlushResolvesEachKindAndDeduplicates(t *testing.T) {
	lib := library(1, "/lib")
	resolver := &fakeResolver{fn: func(kind changeKind, path string) (*scantrigger.Target, error) {
		switch {
		case strings.HasSuffix(path, ".mkv") && kind == changeFile:
			return dirTarget(lib, "/lib/M"), nil
		case kind == changeVanishedFile:
			return dirTarget(lib, "/lib/M"), nil
		case kind == changeVanishedDir:
			return dirTarget(lib, path), nil
		}
		return nil, &scantrigger.RequestError{Status: http.StatusBadRequest, Reason: scantrigger.ReasonUnsupportedExtension}
	}}
	queue := newFakeQueue()
	m := newFlushMonitor(t, resolver, queue, lib)

	m.tracker.observe(Event{Kind: EventCloseWrite, Dir: "/lib/M", Name: "a.mkv"}, t0)
	m.tracker.observe(Event{Kind: EventCloseWrite, Dir: "/lib/M", Name: "b.mkv"}, t0)
	m.tracker.observe(Event{Kind: EventCloseWrite, Dir: "/lib/M", Name: "poster.jpg"}, t0)
	m.tracker.observe(Event{Kind: EventDelete, Dir: "/lib/M", Name: "old.mkv"}, t0)
	m.tracker.observe(Event{Kind: EventDelete, Dir: "/lib", Name: "Gone", IsDir: true}, t0)

	m.flush(context.Background(), t0.Add(time.Second))
	assertNoBatch(t, queue)
	m.flush(context.Background(), t0.Add(5*time.Second))
	got := takeBatch(t, queue)
	want := []string{"1 subtree /lib/Gone realtime_monitor", "1 subtree /lib/M realtime_monitor"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enqueued %v, want %v", got, want)
	}
}

func TestFlushNeverWidensAPathToALibraryAndSkipsOptedOutLibraries(t *testing.T) {
	on := library(1, "/on")
	off := library(2, "/off")
	resolver := &fakeResolver{fn: func(_ changeKind, path string) (*scantrigger.Target, error) {
		if strings.HasPrefix(path, "/off") {
			return dirTarget(off, "/off/M"), nil
		}
		return &scantrigger.Target{Folder: on, Mode: scantrigger.ModeLibrary, Trigger: Trigger}, nil
	}}
	queue := newFakeQueue()
	m := newFlushMonitor(t, resolver, queue, on)

	m.tracker.observe(Event{Kind: EventDelete, Dir: "/on", Name: "poster.jpg"}, t0)
	m.tracker.observe(Event{Kind: EventCloseWrite, Dir: "/off/M", Name: "a.mkv"}, t0)
	m.flush(context.Background(), t0.Add(5*time.Second))
	assertNoBatch(t, queue)
}

func TestFlushCollapsesMoreThanTheCapToOneLibraryScan(t *testing.T) {
	big := library(1, "/big")
	small := library(2, "/small")
	resolver := &fakeResolver{fn: func(_ changeKind, path string) (*scantrigger.Target, error) {
		if strings.HasPrefix(path, "/small") {
			return dirTarget(small, path), nil
		}
		return dirTarget(big, path), nil
	}}
	queue := newFakeQueue()
	m := newFlushMonitor(t, resolver, queue, big, small)
	for i := 0; i <= maxTargetsPerLibrary; i++ {
		m.tracker.observe(Event{Kind: EventDelete, Dir: "/big", Name: fmt.Sprintf("Dir %04d", i), IsDir: true}, t0)
	}
	m.tracker.observe(Event{Kind: EventDelete, Dir: "/small", Name: "A", IsDir: true}, t0)
	m.tracker.observe(Event{Kind: EventDelete, Dir: "/small", Name: "B", IsDir: true}, t0)

	m.flush(context.Background(), t0.Add(5*time.Second))
	got := takeBatch(t, queue)
	want := []string{
		"1 library  realtime_monitor",
		"2 subtree /small/A realtime_monitor",
		"2 subtree /small/B realtime_monitor",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enqueued %v, want %v", got, want)
	}
}

func TestFlushExactlyAtTheCapDoesNotCollapse(t *testing.T) {
	lib := library(1, "/lib")
	resolver := &fakeResolver{fn: func(_ changeKind, path string) (*scantrigger.Target, error) {
		return dirTarget(lib, path), nil
	}}
	queue := newFakeQueue()
	m := newFlushMonitor(t, resolver, queue, lib)
	for i := 0; i < maxTargetsPerLibrary; i++ {
		m.tracker.observe(Event{Kind: EventDelete, Dir: "/lib", Name: fmt.Sprintf("Dir %04d", i), IsDir: true}, t0)
	}
	m.flush(context.Background(), t0.Add(5*time.Second))
	if got := takeBatch(t, queue); len(got) != maxTargetsPerLibrary {
		t.Fatalf("enqueued %d targets, want %d", len(got), maxTargetsPerLibrary)
	}
}

func TestFlushRetriesATransientResolveFailureOnce(t *testing.T) {
	lib := library(1, "/lib")
	resolver := &fakeResolver{fn: func(changeKind, string) (*scantrigger.Target, error) {
		return nil, errors.New("database unavailable")
	}}
	queue := newFakeQueue()
	m := newFlushMonitor(t, resolver, queue, lib)
	m.tracker.observe(Event{Kind: EventDelete, Dir: "/lib/M", Name: "a.mkv"}, t0)

	for i := 0; i < 4; i++ {
		m.flush(context.Background(), t0.Add(time.Duration(5+i)*time.Second))
	}
	if got := resolver.count("/lib/M/a.mkv"); got != 2 {
		t.Fatalf("resolved %d times, want 2 (one retry)", got)
	}
	assertNoBatch(t, queue)
}

func TestFlushRetriesAFailedEnqueueOnce(t *testing.T) {
	lib := library(1, "/lib")
	resolver := &fakeResolver{fn: func(_ changeKind, path string) (*scantrigger.Target, error) {
		return dirTarget(lib, path), nil
	}}
	queue := newFakeQueue()
	queue.failures = 2
	m := newFlushMonitor(t, resolver, queue, lib)
	m.tracker.observe(Event{Kind: EventDelete, Dir: "/lib", Name: "A", IsDir: true}, t0)

	m.flush(context.Background(), t0.Add(5*time.Second))
	first := takeBatch(t, queue)
	m.flush(context.Background(), t0.Add(6*time.Second))
	second := takeBatch(t, queue)
	if !reflect.DeepEqual(first, second) || len(first) != 1 {
		t.Fatalf("first %v, retry %v, want the same single target", first, second)
	}
	m.flush(context.Background(), t0.Add(7*time.Second))
	assertNoBatch(t, queue)
}

// A target kept for a retry after a failed enqueue is dropped when its
// library turned monitoring off before the retry.
func TestFlushDropsRetryTargetsOfLibrariesNoLongerMonitored(t *testing.T) {
	lib := library(1, "/lib")
	resolver := &fakeResolver{fn: func(_ changeKind, path string) (*scantrigger.Target, error) {
		return dirTarget(lib, path), nil
	}}
	queue := newFakeQueue()
	queue.failures = 1
	m := newFlushMonitor(t, resolver, queue, lib)
	m.tracker.observe(Event{Kind: EventDelete, Dir: "/lib", Name: "A", IsDir: true}, t0)

	m.flush(context.Background(), t0.Add(5*time.Second))
	if got := takeBatch(t, queue); len(got) != 1 {
		t.Fatalf("first batch %v, want the one target", got)
	}
	m.mu.Lock()
	m.desired = map[int]*models.MediaFolder{}
	m.mu.Unlock()
	m.flush(context.Background(), t0.Add(6*time.Second))
	assertNoBatch(t, queue)
}

func TestFlushOverflowLibraryScanSupersedesPathTargets(t *testing.T) {
	lib := library(1, "/lib")
	other := library(2, "/other")
	resolver := &fakeResolver{fn: func(_ changeKind, path string) (*scantrigger.Target, error) {
		if strings.HasPrefix(path, "/other") {
			return dirTarget(other, path), nil
		}
		return dirTarget(lib, path), nil
	}}
	queue := newFakeQueue()
	m := newFlushMonitor(t, resolver, queue, lib, other)
	m.libraryScans[1] = struct{}{}
	m.libraryScans[3] = struct{}{} // not monitored any more: dropped
	m.tracker.observe(Event{Kind: EventDelete, Dir: "/lib", Name: "A", IsDir: true}, t0)
	m.tracker.observe(Event{Kind: EventDelete, Dir: "/other", Name: "B", IsDir: true}, t0)

	m.flush(context.Background(), t0.Add(5*time.Second))
	got := takeBatch(t, queue)
	want := []string{"1 library  realtime_monitor", "2 subtree /other/B realtime_monitor"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enqueued %v, want %v", got, want)
	}
}

func TestHandleEventDropsIgnoredNames(t *testing.T) {
	lib := library(1, "/lib")
	var resolved []string
	resolver := &fakeResolver{fn: func(kind changeKind, path string) (*scantrigger.Target, error) {
		resolved = append(resolved, kind.String()+" "+path)
		return dirTarget(lib, "/lib/M"), nil
	}}
	m := newFlushMonitor(t, resolver, newFakeQueue(), lib)
	b := newFakeBackend("inotify")
	for _, ev := range []Event{
		{Kind: EventCloseWrite, Dir: "/lib/M", Name: "movie.mkv.part"},
		{Kind: EventDelete, Dir: "/lib/M", Name: "x.tmp"},
		{Kind: EventCreate, Dir: "/lib", Name: "@eaDir", IsDir: true},
		// A finished download renamed from its temp name arrives as a move in.
		{Kind: EventRename, OldDir: "/lib/M", OldName: "movie.mkv.!qB", Dir: "/lib/M", Name: "movie.mkv"},
		// Renamed to a temp name: gone as far as the library is concerned.
		{Kind: EventRename, OldDir: "/lib/M", OldName: "old.mkv", Dir: "/lib/M", Name: "old.mkv.partial"},
	} {
		m.handleEvent(backendEvent{backend: b, ev: ev}, t0)
	}
	m.flush(context.Background(), t0.Add(5*time.Second))
	want := []string{"file /lib/M/movie.mkv", "vanished_file /lib/M/old.mkv"}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved %v, want %v", resolved, want)
	}
}

// countingFolders counts library listings, which production serves from the
// database.
type countingFolders struct {
	fakeFolders
	lists atomic.Int32
}

func (f *countingFolders) List(ctx context.Context) ([]*models.MediaFolder, error) {
	f.lists.Add(1)
	return f.fakeFolders.List(ctx)
}

// A burst resolves against the libraries the last reconcile listed, with the
// real resolver: no library listing per changed path, and more targets than
// the cap still collapse to one library scan.
func TestFlushResolvesABurstWithoutListingLibrariesPerPath(t *testing.T) {
	root := t.TempDir()
	lib := library(1, root)
	lib.Type = "movies"
	folders := &countingFolders{}
	folders.set(lib)
	built := 0
	queue := newFakeQueue()
	m, err := New(Config{
		NodeID:  "node-test",
		Folders: folders,
		NewResolver: func(f scantrigger.FolderRepository) Resolver {
			built++
			return scantrigger.NewResolver(f)
		},
		Queue:       queue,
		Logger:      quietLogger(),
		QuietWindow: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.desired[lib.ID] = lib
	m.listed = []*models.MediaFolder{lib}
	for i := 0; i <= maxTargetsPerLibrary; i++ {
		name := fmt.Sprintf("Movie %04d (2020)", i)
		mkdirs(t, root, name)
		writeFile(t, filepath.Join(root, name, name+".mkv"), "x")
		m.tracker.observe(Event{Kind: EventCloseWrite, Dir: filepath.Join(root, name), Name: name + ".mkv"}, t0)
	}

	m.flush(context.Background(), t0.Add(5*time.Second))
	if got, want := takeBatch(t, queue), []string{"1 library  realtime_monitor"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("enqueued %v, want %v", got, want)
	}
	if n := folders.lists.Load(); n != 0 {
		t.Fatalf("flush listed libraries %d times, want none", n)
	}
	if built != 1 {
		t.Fatalf("built %d resolvers, want one per flush", built)
	}
}
