package keyframes

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	got, err := Load(path)
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
	got, err = Load(path)
	wantIndex(t, got, err)

	// A new modification time reads the file again.
	later := stamp.Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("err = %v, want ErrNoIndex for the changed file", err)
	}
}

func TestLoadReportsOtherFilesAsUnindexed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mp4")
	if err := os.WriteFile(path, []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("err = %v, want ErrNoIndex", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.mkv")); err == nil || errors.Is(err, ErrNoIndex) {
		t.Fatalf("missing file err = %v, want a read error", err)
	}
}

// The first Load returns the index, video end included, so the first
// playback of a file can plan from it (#2453).
func TestLoadReturnsTheIndexTheFirstTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.mkv")
	if err := os.WriteFile(path, file(layout{}), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	wantIndex(t, got, err)
	if got.VideoEnd <= got.Keyframes[len(got.Keyframes)-1] {
		t.Fatalf("VideoEnd = %v, want it after the last keyframe", got.VideoEnd)
	}
}

// A malformed file is a lasting fact, cached like a missing index, not
// read again on every playback.
func TestLoadCachesMalformedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.mkv")
	// Cues pointing at an element that isn't a cluster: with the Cues first,
	// cluster B ends the file.
	contents := file(layout{cuesFirst: true})
	broken := slices.Clone(contents)
	copy(broken[len(broken)-len(clusterB):], bytes.Repeat([]byte{0xEC}, 8)) // overwrite cluster B's header with Void
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("malformed file err = %v, want ErrNoIndex", err)
	}
	// Repaired in place with the same size and time: still the cached result.
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("second load err = %v, want the cached ErrNoIndex", err)
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
	got, err := Load(path)
	wantIndex(t, got, err)

	if err := Reject(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrIncompleteIndex) || !errors.Is(err, ErrNoIndex) {
		t.Fatalf("after Reject err = %v, want ErrIncompleteIndex (an ErrNoIndex)", err)
	}

	later := stamp.Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	wantIndex(t, got, err)
}
