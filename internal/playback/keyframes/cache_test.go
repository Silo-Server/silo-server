package keyframes

import (
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
