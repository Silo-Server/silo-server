package scanner

import (
	"context"
	"errors"
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

func TestIsAudiobookLibraryType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"audiobooks", true},
		{"audiobook", true},
		{"Audiobook", true},
		{"  AUDIOBOOKS  ", true},
		{"movies", false},
		{"series", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isAudiobookLibraryType(tc.in); got != tc.want {
			t.Errorf("isAudiobookLibraryType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsPodcastLibraryType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"podcasts", true},
		{"podcast", true},
		{"Podcast", true},
		{"  PODCASTS  ", true},
		{"series", false},
		{"audiobooks", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isPodcastLibraryType(tc.in); got != tc.want {
			t.Errorf("isPodcastLibraryType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsEbookLibraryType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"ebooks", true},
		{"ebook", true},
		{"Ebook", true},
		{"  EBOOKS  ", true},
		{"audiobooks", false},
		{"movies", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isEbookLibraryType(tc.in); got != tc.want {
			t.Errorf("isEbookLibraryType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestWalkModeForEbookLibraryTypes(t *testing.T) {
	for _, libraryType := range []string{"ebook", "ebooks", " EBOOKS "} {
		if got := walkModeFor(libraryType); got != walkModeEbook {
			t.Fatalf("walkModeFor(%q) = %v, want %v", libraryType, got, walkModeEbook)
		}
	}
}

func TestWalkModeEbookAcceptsEbookExtensionsOnly(t *testing.T) {
	if !walkModeEbook.acceptsExt(".epub") {
		t.Fatal("walkModeEbook should accept .epub")
	}
	if !walkModeEbook.acceptsExt(".pdf") {
		t.Fatal("walkModeEbook should accept .pdf")
	}
	if walkModeEbook.acceptsExt(".mp4") {
		t.Fatal("walkModeEbook should reject video extensions")
	}
	if walkModeEbook.acceptsExt(".mp3") {
		t.Fatal("walkModeEbook should reject audio extensions")
	}
}

func TestScanFolderEbookLibraryRoutesToEbookScanner(t *testing.T) {
	scanner := &Scanner{}
	result, err := scanner.ScanFolder(context.Background(), &models.MediaFolder{
		ID:    0,
		Type:  " ebooks ",
		Paths: []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("ScanFolder ebook error = %v, want nil", err)
	}
	if result == nil {
		t.Fatal("ScanFolder ebook result = nil, want empty result")
	}
}

func TestScanSubtreeEbookLibraryReturnsNotImplemented(t *testing.T) {
	subtree := t.TempDir()
	ebookPath := filepath.Join(subtree, "Book.epub")
	if err := os.WriteFile(ebookPath, []byte("not a real epub"), 0o644); err != nil {
		t.Fatalf("write ebook fixture: %v", err)
	}

	scanner := &Scanner{}
	result, err := scanner.ScanSubtree(context.Background(), &models.MediaFolder{
		ID:    0,
		Type:  "ebook",
		Paths: []string{subtree},
	}, subtree)
	if !errors.Is(err, errEbookScanningNotImplemented) {
		t.Fatalf("ScanSubtree ebook error = %v, want %v", err, errEbookScanningNotImplemented)
	}
	if result != nil {
		t.Fatalf("ScanSubtree ebook result = %+v, want nil", result)
	}
}
