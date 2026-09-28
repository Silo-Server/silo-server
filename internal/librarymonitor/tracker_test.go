package librarymonitor

import (
	"io/fs"
	"os"
	"reflect"
	"testing"
	"time"
)

// fakeFiles is a stat seam: path -> state; missing paths are ENOENT.
type fakeFiles map[string]fileState

func (f fakeFiles) stat(path string) (fileState, error) {
	st, ok := f[path]
	if !ok {
		return fileState{}, &fs.PathError{Op: "lstat", Path: path, Err: os.ErrNotExist}
	}
	return st, nil
}

func regular(nlink uint64, size int64, mtime time.Time) fileState {
	return fileState{mode: 0o644, nlink: nlink, size: size, mtime: mtime}
}

var t0 = time.Unix(1_000_000, 0)

func newTestTracker(files fakeFiles) *tracker {
	tr := newTracker(5*time.Second, 2*time.Minute, 30*time.Second)
	tr.stat = files.stat
	return tr
}

func assertReady(t *testing.T, tr *tracker, now time.Time, want ...change) {
	t.Helper()
	got := tr.ready(now)
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ready(%s) = %+v, want %+v", now.Sub(t0), got, want)
	}
}

func TestTrackerQuietWindowRestartsOnNewEvent(t *testing.T) {
	tr := newTestTracker(fakeFiles{})
	ev := Event{Kind: EventCloseWrite, Dir: "/lib/Movie", Name: "movie.mkv"}
	tr.observe(ev, t0)
	assertReady(t, tr, t0.Add(4*time.Second))
	tr.observe(ev, t0.Add(4*time.Second))
	assertReady(t, tr, t0.Add(8*time.Second))
	assertReady(t, tr, t0.Add(9*time.Second), change{path: "/lib/Movie/movie.mkv", kind: changeFile})
	assertReady(t, tr, t0.Add(20*time.Second))
}

func TestTrackerHardlinkIsCompleteButCopyWaitsForCloseWrite(t *testing.T) {
	files := fakeFiles{
		"/lib/M/hardlink.mkv": regular(2, 100, t0),
		"/lib/M/copy.mkv":     regular(1, 10, t0),
		"/lib/M/link.mkv":     {mode: fs.ModeSymlink | 0o777, nlink: 1},
	}
	tr := newTestTracker(files)
	for _, name := range []string{"hardlink.mkv", "copy.mkv", "link.mkv"} {
		tr.observe(Event{Kind: EventCreate, Dir: "/lib/M", Name: name}, t0)
	}
	assertReady(t, tr, t0.Add(5*time.Second),
		change{path: "/lib/M/hardlink.mkv", kind: changeFile},
		change{path: "/lib/M/link.mkv", kind: changeFile},
	)
	assertReady(t, tr, t0.Add(60*time.Second))
	tr.observe(Event{Kind: EventCloseWrite, Dir: "/lib/M", Name: "copy.mkv"}, t0.Add(61*time.Second))
	assertReady(t, tr, t0.Add(65*time.Second))
	assertReady(t, tr, t0.Add(66*time.Second), change{path: "/lib/M/copy.mkv", kind: changeFile})
}

func TestTrackerCreatedWithoutCloseWriteFallsBackToStabilityPolling(t *testing.T) {
	files := fakeFiles{"/lib/M/slow.mkv": regular(1, 10, t0)}
	tr := newTestTracker(files)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib/M", Name: "slow.mkv"}, t0)

	assertReady(t, tr, t0.Add(119*time.Second))
	// First check at the fallback records size and mtime.
	assertReady(t, tr, t0.Add(2*time.Minute))
	// Still growing at the next check.
	files["/lib/M/slow.mkv"] = regular(1, 20, t0.Add(140*time.Second))
	assertReady(t, tr, t0.Add(150*time.Second))
	// Polls only every StablePoll.
	assertReady(t, tr, t0.Add(170*time.Second))
	// Unchanged across two checks: reported.
	assertReady(t, tr, t0.Add(180*time.Second), change{path: "/lib/M/slow.mkv", kind: changeFile})
}

func TestTrackerFallbackDropsFileThatVanished(t *testing.T) {
	files := fakeFiles{"/lib/M/gone.mkv": regular(1, 10, t0)}
	tr := newTestTracker(files)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib/M", Name: "gone.mkv"}, t0)
	delete(files, "/lib/M/gone.mkv")
	assertReady(t, tr, t0.Add(2*time.Minute))
	if len(tr.pending) != 0 {
		t.Fatalf("pending = %v, want empty", tr.pending)
	}
}

func TestTrackerNewDirectoryReportsOnceAfterItsContentsSettle(t *testing.T) {
	files := fakeFiles{
		"/lib/New":           {mode: fs.ModeDir | 0o755},
		"/lib/New/movie.mkv": regular(1, 10, t0),
		"/lib/New/movie.nfo": regular(1, 1, t0),
	}
	tr := newTestTracker(files)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib", Name: "New", IsDir: true}, t0)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib/New", Name: "movie.mkv"}, t0.Add(time.Second))
	tr.observe(Event{Kind: EventCloseWrite, Dir: "/lib/New", Name: "movie.nfo"}, t0.Add(time.Second))

	// The copy is in progress: nothing, not even the complete sidecar.
	assertReady(t, tr, t0.Add(30*time.Second))
	tr.observe(Event{Kind: EventCloseWrite, Dir: "/lib/New", Name: "movie.mkv"}, t0.Add(40*time.Second))
	assertReady(t, tr, t0.Add(44*time.Second))
	assertReady(t, tr, t0.Add(45*time.Second), change{path: "/lib/New", kind: changeDir})
	if len(tr.pending) != 0 {
		t.Fatalf("pending = %v, want the directory to absorb its contents", tr.pending)
	}
}

func TestTrackerRenamePairs(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		tr := newTestTracker(fakeFiles{})
		tr.observe(Event{Kind: EventRename, OldDir: "/lib/M", OldName: "a.mkv", Dir: "/lib/M", Name: "b.mkv"}, t0)
		assertReady(t, tr, t0.Add(5*time.Second),
			change{path: "/lib/M/a.mkv", kind: changeVanishedFile},
			change{path: "/lib/M/b.mkv", kind: changeFile},
		)
	})
	t.Run("directory keeps a copy in progress under its new path", func(t *testing.T) {
		files := fakeFiles{"/lib/Old/part.mkv": regular(1, 10, t0)}
		tr := newTestTracker(files)
		tr.observe(Event{Kind: EventCreate, Dir: "/lib/Old", Name: "part.mkv"}, t0)
		tr.observe(Event{Kind: EventRename, OldDir: "/lib", OldName: "Old", Dir: "/lib", Name: "New", IsDir: true}, t0.Add(time.Second))
		if _, ok := tr.pending["/lib/New/part.mkv"]; !ok {
			t.Fatalf("pending = %v, want part.mkv re-keyed under /lib/New", tr.pending)
		}
		// The new directory waits for the copy under it.
		assertReady(t, tr, t0.Add(10*time.Second), change{path: "/lib/Old", kind: changeVanishedDir})
		tr.observe(Event{Kind: EventCloseWrite, Dir: "/lib/New", Name: "part.mkv"}, t0.Add(20*time.Second))
		assertReady(t, tr, t0.Add(25*time.Second), change{path: "/lib/New", kind: changeDir})
	})
}

func TestTrackerVanishedDirectorySubsumesChangesBelowIt(t *testing.T) {
	tr := newTestTracker(fakeFiles{})
	tr.observe(Event{Kind: EventDelete, Dir: "/lib/M", Name: "a.mkv"}, t0)
	tr.observe(Event{Kind: EventCloseWrite, Dir: "/lib/M/Extras", Name: "b.mkv"}, t0)
	tr.observe(Event{Kind: EventDelete, Dir: "/lib", Name: "M", IsDir: true}, t0.Add(time.Second))
	assertReady(t, tr, t0.Add(6*time.Second), change{path: "/lib/M", kind: changeVanishedDir})
}

func TestTrackerCreatedThenDeletedIsDropped(t *testing.T) {
	files := fakeFiles{"/lib/M/a.mkv": regular(1, 1, t0)}
	tr := newTestTracker(files)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib/M", Name: "a.mkv"}, t0)
	tr.observe(Event{Kind: EventDelete, Dir: "/lib/M", Name: "a.mkv"}, t0.Add(time.Second))
	assertReady(t, tr, t0.Add(6*time.Second))
	if len(tr.pending) != 0 {
		t.Fatalf("pending = %v, want nothing for a file that came and went", tr.pending)
	}
}

func TestTrackerTempFileRenamedIntoPlace(t *testing.T) {
	// rsync and downloaders write a hidden temp name, then rename it.
	files := fakeFiles{"/lib/M/.Movie.mkv.Xy12Ab": regular(1, 1, t0)}
	tr := newTestTracker(files)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib/M", Name: ".Movie.mkv.Xy12Ab"}, t0)
	tr.observe(Event{Kind: EventCloseWrite, Dir: "/lib/M", Name: ".Movie.mkv.Xy12Ab"}, t0.Add(time.Second))
	tr.observe(Event{Kind: EventRename, OldDir: "/lib/M", OldName: ".Movie.mkv.Xy12Ab", Dir: "/lib/M", Name: "Movie.mkv"}, t0.Add(2*time.Second))
	assertReady(t, tr, t0.Add(7*time.Second), change{path: "/lib/M/Movie.mkv", kind: changeFile})
}

func TestTrackerFolderRenamedRightAfterItWasMade(t *testing.T) {
	tr := newTestTracker(fakeFiles{})
	tr.observe(Event{Kind: EventCreate, Dir: "/lib", Name: "New folder", IsDir: true}, t0)
	tr.observe(Event{Kind: EventRename, OldDir: "/lib", OldName: "New folder", Dir: "/lib", Name: "Movie (2020)", IsDir: true}, t0.Add(time.Second))
	assertReady(t, tr, t0.Add(6*time.Second), change{path: "/lib/Movie (2020)", kind: changeDir})
}

func TestTrackerVanishedIsKeptWhenThePathMayHaveExisted(t *testing.T) {
	t.Run("moved in, then deleted", func(t *testing.T) {
		// A move in can replace an existing file; its delete must clean up.
		tr := newTestTracker(fakeFiles{})
		tr.observe(Event{Kind: EventMovedTo, Dir: "/lib/M", Name: "a.mkv"}, t0)
		tr.observe(Event{Kind: EventDelete, Dir: "/lib/M", Name: "a.mkv"}, t0.Add(time.Second))
		assertReady(t, tr, t0.Add(6*time.Second), change{path: "/lib/M/a.mkv", kind: changeVanishedFile})
	})
	t.Run("deleted, created, deleted", func(t *testing.T) {
		files := fakeFiles{"/lib/M/a.mkv": regular(1, 1, t0)}
		tr := newTestTracker(files)
		tr.observe(Event{Kind: EventDelete, Dir: "/lib/M", Name: "a.mkv"}, t0)
		tr.observe(Event{Kind: EventCreate, Dir: "/lib/M", Name: "a.mkv"}, t0.Add(time.Second))
		tr.observe(Event{Kind: EventDelete, Dir: "/lib/M", Name: "a.mkv"}, t0.Add(2*time.Second))
		assertReady(t, tr, t0.Add(7*time.Second), change{path: "/lib/M/a.mkv", kind: changeVanishedFile})
	})
	t.Run("reported, then deleted", func(t *testing.T) {
		files := fakeFiles{"/lib/M/a.mkv": regular(2, 1, t0)}
		tr := newTestTracker(files)
		tr.observe(Event{Kind: EventCreate, Dir: "/lib/M", Name: "a.mkv"}, t0)
		assertReady(t, tr, t0.Add(5*time.Second), change{path: "/lib/M/a.mkv", kind: changeFile})
		tr.observe(Event{Kind: EventDelete, Dir: "/lib/M", Name: "a.mkv"}, t0.Add(6*time.Second))
		assertReady(t, tr, t0.Add(11*time.Second), change{path: "/lib/M/a.mkv", kind: changeVanishedFile})
	})
}

func TestTrackerMovedFromAndToAreComplete(t *testing.T) {
	tr := newTestTracker(fakeFiles{})
	tr.observe(Event{Kind: EventMovedTo, Dir: "/lib", Name: "Import", IsDir: true}, t0)
	tr.observe(Event{Kind: EventMovedTo, Dir: "/lib/M", Name: "in.mkv"}, t0)
	tr.observe(Event{Kind: EventMovedFrom, Dir: "/lib/M", Name: "out.mkv"}, t0)
	tr.observe(Event{Kind: EventMovedFrom, Dir: "/lib", Name: "Gone", IsDir: true}, t0)
	assertReady(t, tr, t0.Add(5*time.Second),
		change{path: "/lib/Gone", kind: changeVanishedDir},
		change{path: "/lib/Import", kind: changeDir},
		change{path: "/lib/M/in.mkv", kind: changeFile},
		change{path: "/lib/M/out.mkv", kind: changeVanishedFile},
	)
}

func TestTrackerDropUnderForgetsARoot(t *testing.T) {
	tr := newTestTracker(fakeFiles{})
	tr.observe(Event{Kind: EventDelete, Dir: "/lib/M", Name: "a.mkv"}, t0)
	tr.observe(Event{Kind: EventDelete, Dir: "/other", Name: "b.mkv"}, t0)
	tr.dropUnder("/lib")
	assertReady(t, tr, t0.Add(5*time.Second), change{path: "/other/b.mkv", kind: changeVanishedFile})
}

// A file a walk found in a new directory may have been written before the
// directory was watched, so its close-write may never come. The directory
// waits for it; the file counts as complete once its size and mtime hold
// across a quiet window.
func TestTrackerFoundFileHoldsItsNewDirectoryUntilStable(t *testing.T) {
	files := fakeFiles{
		"/lib/New":       {mode: fs.ModeDir | 0o755},
		"/lib/New/a.mkv": regular(1, 100, t0),
	}
	tr := newTestTracker(files)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib", Name: "New", IsDir: true}, t0)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib/New", Name: "a.mkv", Found: true}, t0)

	assertReady(t, tr, t0.Add(6*time.Second)) // first stat
	files["/lib/New/a.mkv"] = regular(1, 200, t0.Add(8*time.Second))
	assertReady(t, tr, t0.Add(11*time.Second)) // still growing
	assertReady(t, tr, t0.Add(16*time.Second), change{path: "/lib/New", kind: changeDir})
}

func TestTrackerFoundFileCompletesAtItsCloseWrite(t *testing.T) {
	files := fakeFiles{
		"/lib/New":       {mode: fs.ModeDir | 0o755},
		"/lib/New/a.mkv": regular(1, 100, t0),
	}
	tr := newTestTracker(files)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib", Name: "New", IsDir: true}, t0)
	tr.observe(Event{Kind: EventCreate, Dir: "/lib/New", Name: "a.mkv", Found: true}, t0)
	tr.observe(Event{Kind: EventCloseWrite, Dir: "/lib/New", Name: "a.mkv"}, t0.Add(3*time.Second))
	assertReady(t, tr, t0.Add(8*time.Second), change{path: "/lib/New", kind: changeDir})
}
