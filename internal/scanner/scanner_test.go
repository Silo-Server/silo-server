package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCollectLogicalFilePaths_PreservesLogicalSymlinkRootPaths(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	physicalRoot := filepath.Join(base, "real")
	logicalRoot := filepath.Join(base, "library")
	seasonDir := filepath.Join(physicalRoot, "Season 1")

	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatalf("mkdir season dir: %v", err)
	}
	filePath := filepath.Join(seasonDir, "Episode 01.mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if err := os.Symlink(physicalRoot, logicalRoot); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	files, err := collectLogicalFilePaths(context.Background(), []string{logicalRoot}, "series")
	if err != nil {
		t.Fatalf("collect logical paths: %v", err)
	}

	want := filepath.Join(logicalRoot, "Season 1", "Episode 01.mkv")
	if len(files) != 1 {
		t.Fatalf("files len = %d, want 1 (%v)", len(files), files)
	}
	if files[0] != want {
		t.Fatalf("files[0] = %q, want %q", files[0], want)
	}
}

func TestCollectLogicalFilePaths_DedupesSharedPhysicalDirsAndCycles(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	physicalRoot := filepath.Join(base, "real")
	aliasRoot := filepath.Join(base, "alias")
	loopPath := filepath.Join(physicalRoot, "loop")

	if err := os.MkdirAll(physicalRoot, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	filePath := filepath.Join(physicalRoot, "Movie.mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if err := os.Symlink(physicalRoot, aliasRoot); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}
	if err := os.Symlink(physicalRoot, loopPath); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	files, err := collectLogicalFilePaths(context.Background(), []string{physicalRoot, aliasRoot}, "movie")
	if err != nil {
		t.Fatalf("collect logical paths: %v", err)
	}

	sort.Strings(files)
	want := []string{filepath.Join(physicalRoot, "Movie.mkv")}
	if len(files) != len(want) {
		t.Fatalf("files len = %d, want %d (%v)", len(files), len(want), files)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files[%d] = %q, want %q", i, files[i], want[i])
		}
	}
}

func TestShouldSkipStableConfirmedFile_DoesNotSkipAssignmentChanges(t *testing.T) {
	t.Parallel()

	modifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	existing := &models.MediaFile{
		ContentID:      "matched-content",
		FileSize:       1_000,
		FileModifiedAt: &modifiedAt,
	}

	if shouldSkipStableConfirmedFile(existing, "matched", 1_000, modifiedAt, []string{"root_assignment_changed"}, false) {
		t.Fatal("expected assignment changes to bypass stable-file skip")
	}
	if !shouldSkipStableConfirmedFile(existing, "matched", 1_000, modifiedAt, nil, false) {
		t.Fatal("expected unchanged matched file to use stable-file skip")
	}
}

func TestScanStateUpdateReasons_DetectsMissingExternalSubtitle(t *testing.T) {
	t.Parallel()

	modifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	existing := &scanStateFile{
		ContentID:             "matched-content",
		FileSize:              1_000,
		FileModifiedAt:        &modifiedAt,
		ExternalSubtitlePaths: []string{filepath.Join(t.TempDir(), "Movie.en.srt")},
	}

	reasons := scanStateUpdateReasons(existing, 1_000, modifiedAt, nil, false, fileRootAssignment{}, fileGroupAssignment{}, "movies", false)
	if !testStringSliceContains(reasons, "external_subtitle_missing") {
		t.Fatalf("expected external_subtitle_missing reason, got %#v", reasons)
	}
	if shouldSkipStableConfirmedScanState(existing, "matched", 1_000, modifiedAt, reasons, false) {
		t.Fatal("expected missing external subtitle to bypass stable scan-state skip")
	}
}

func TestScanStateUpdateReasons_DetectsExternalSubtitleInventoryChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	modifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	existing := &scanStateFile{
		ContentID:             "matched-content",
		FileSize:              1_000,
		FileModifiedAt:        &modifiedAt,
		ExternalSubtitlePaths: []string{filepath.Join(dir, "Movie.en.srt")},
	}

	reasons := scanStateUpdateReasons(
		existing,
		1_000,
		modifiedAt,
		[]string{filepath.Join(dir, "Movie.en.srt"), filepath.Join(dir, "Movie.fr.srt")},
		true,
		fileRootAssignment{},
		fileGroupAssignment{},
		"movies",
		false,
	)
	if !testStringSliceContains(reasons, "external_subtitle_changed") {
		t.Fatalf("expected external_subtitle_changed reason, got %#v", reasons)
	}
	if shouldSkipStableConfirmedScanState(existing, "matched", 1_000, modifiedAt, reasons, false) {
		t.Fatal("expected external subtitle change to bypass stable scan-state skip")
	}
}

func testStringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
