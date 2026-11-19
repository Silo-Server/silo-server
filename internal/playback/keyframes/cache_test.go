package keyframes

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestLoadCachesUntilTheFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mkv")
	contents := file(layout{})
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.Context(), path)
	wantIndex(t, got, err)

	// Same size and time with different bytes: still the cached index.
	damaged := slices.Clone(contents)
	for i := range damaged[:8] {
		damaged[i] = 0
	}
	if err := os.WriteFile(path, damaged, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	got, err = Load(t.Context(), path)
	wantIndex(t, got, err)

	// A new modification time reads the file again.
	later := stamp.Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("err = %v, want ErrNoIndex for the changed file", err)
	}
}

func TestLoadReportsOtherFilesAsUnindexed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mp4")
	if err := os.WriteFile(path, []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("err = %v, want ErrNoIndex", err)
	}
	if _, err := Load(t.Context(), filepath.Join(t.TempDir(), "missing.mkv")); err == nil || errors.Is(err, ErrNoIndex) {
		t.Fatalf("missing file err = %v, want a read error", err)
	}
}

type readerAtFunc func(p []byte, off int64) (int, error)

func (f readerAtFunc) ReadAt(p []byte, off int64) (int, error) { return f(p, off) }

// A relayed read cut mid-transfer fails with an end-of-file error inside a
// path error. That is a failed read, not a file too short to be Matroska: it
// stays transient, so Load does not cache it as a lasting missing index.
func TestACutReadIsNotAShortFile(t *testing.T) {
	cut := readerAtFunc(func([]byte, int64) (int, error) {
		return 0, &fs.PathError{Op: "read", Path: "source", Err: io.ErrUnexpectedEOF}
	})
	if _, err := readPlanIndex(cut, 1<<20); err == nil || !transient(err) {
		t.Fatalf("err = %v, want the read error", err)
	}
	// The same for a read that delivered part of a header before it failed.
	partial := readerAtFunc(func(p []byte, _ int64) (int, error) {
		return copy(p, []byte{0x1A, 0x45}), &fs.PathError{Op: "read", Path: "source", Err: io.EOF}
	})
	if _, err := readPlanIndex(partial, 1<<20); err == nil || !transient(err) {
		t.Fatalf("partial read err = %v, want the read error", err)
	}
	// And for a read that fails inside the segment, after both headers.
	ebml := el(idEBML)
	data := append(ebml, el(idSegment, make([]byte, 64))...)
	inSegment := readerAtFunc(func(p []byte, off int64) (int, error) {
		if off > int64(len(ebml)) {
			return 0, &fs.PathError{Op: "read", Path: "source", Err: io.EOF}
		}
		return bytes.NewReader(data).ReadAt(p, off)
	})
	if _, err := readPlanIndex(inSegment, int64(len(data))); err == nil || !transient(err) {
		t.Fatalf("segment read err = %v, want the read error", err)
	}
}

// The first Load returns the index, video end included, so the first
// playback of a file can plan from it (#2453).
func TestLoadReturnsTheIndexTheFirstTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.mkv")
	if err := os.WriteFile(path, file(layout{}), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.Context(), path)
	wantIndex(t, got, err)
	if got.VideoEnd <= got.Keyframes[len(got.Keyframes)-1] {
		t.Fatalf("VideoEnd = %v, want it after the last keyframe", got.VideoEnd)
	}
}

// A file a playback rejected isn't planned from again until it changes.
func TestRejectHoldsUntilTheFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rejected.mkv")
	if err := os.WriteFile(path, file(layout{}), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.Context(), path)
	wantIndex(t, got, err)

	Reject(got.File)
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrIncompleteIndex) || !errors.Is(err, ErrNoIndex) {
		t.Fatalf("after Reject err = %v, want ErrIncompleteIndex (an ErrNoIndex)", err)
	}

	// A Load that read the file before the Reject and stores after it
	// doesn't undo it.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	key := Version{Path: path, Size: info.Size(), ModTime: info.ModTime()}
	if _, err := storeFirst(key, got, nil); !errors.Is(err, ErrIncompleteIndex) {
		t.Fatalf("a late load's store err = %v, want the rejection kept", err)
	}

	// Evicting the results doesn't forget the rejection.
	for i := range maxCachedFiles + 1 {
		storeFirst(Version{Path: filepath.Join("evict", strconv.Itoa(i))}, Index{}, ErrNoIndex)
	}
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrIncompleteIndex) {
		t.Fatalf("after evictions err = %v, want the rejection kept", err)
	}

	later := stamp.Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	rejected := got.File
	got, err = Load(t.Context(), path)
	wantIndex(t, got, err)

	// A late rejection of the old version, from a playback that read the
	// file before it changed, leaves the new version alone.
	Reject(rejected)
	got, err = Load(t.Context(), path)
	wantIndex(t, got, err)
}
