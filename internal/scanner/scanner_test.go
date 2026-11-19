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

type recordingQueueSyncer struct {
	folderID int
	scope    string
}

func (s *recordingQueueSyncer) SyncForFolder(context.Context, int) error {
	return nil
}

func (s *recordingQueueSyncer) SyncInScope(_ context.Context, folderID int, scopePath string) error {
	s.folderID = folderID
	s.scope = scopePath
	return nil
}

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

	files, walkFailures, err := collectLogicalFilePaths(t.Context(), []string{logicalRoot}, "series", nil)
	if err != nil {
		t.Fatalf("collect logical paths: %v", err)
	}
	if len(walkFailures) != 0 {
		t.Fatalf("walkFailures = %v, want none for a fully readable tree", walkFailures)
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

	files, walkFailures, err := collectLogicalFilePaths(t.Context(), []string{physicalRoot, aliasRoot}, "movie", nil)
	if err != nil {
		t.Fatalf("collect logical paths: %v", err)
	}
	if len(walkFailures) != 0 {
		t.Fatalf("walkFailures = %v, want none for a fully readable tree", walkFailures)
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

func TestFailedProbeRepairPreservesExistingProbe(t *testing.T) {
	if !shouldPreserveExistingProbeAfterProbeFailure([]string{"probe_repair"}, nil, false) {
		t.Fatal("migration-triggered repair failure would overwrite existing probe metadata")
	}
	if shouldPreserveExistingProbeAfterProbeFailure([]string{"probe_repair", "mtime_changed"}, nil, false) {
		t.Fatal("changed source must not preserve stale probe metadata")
	}
	if !shouldPreserveExistingProbeAfterProbeFailure([]string{"probe_repair", "root_assignment_changed", "group_assignment_changed"}, nil, false) {
		t.Fatal("identity-only changes must preserve probe metadata when repair probing fails")
	}
	if shouldPreserveExistingProbeAfterProbeFailure([]string{"probe_repair", "size_changed", "root_assignment_changed"}, nil, false) {
		t.Fatal("byte changes must not preserve stale probe metadata")
	}
	if !shouldPreserveExistingProbeAfterProbeFailure([]string{"probe_repair", "external_subtitle_changed"}, nil, false) {
		t.Fatal("subtitle-only changes must preserve probe metadata when repair probing fails")
	}
	if !shouldPreserveExistingProbeAfterProbeFailure([]string{"probe_repair", "external_subtitle_missing", "group_assignment_changed"}, nil, false) {
		t.Fatal("missing subtitle and identity changes must preserve probe metadata when repair probing fails")
	}
	if !shouldPreserveExistingProbeAfterProbeFailure([]string{"external_subtitle_changed"}, nil, true) {
		t.Fatal("a standing rejection must preserve probe metadata like a failed repair")
	}
}

func TestPersistIdentityUpdateUsesReturnedIDBeforeEnqueue(t *testing.T) {
	mf := &models.MediaFile{FilePath: "/library/movie.mkv"}
	var enqueuedID int
	applied, err := persistIdentityUpdate(
		mf,
		func(models.MediaFile) (int, error) { return 42, nil },
		func(file *models.MediaFile) error {
			enqueuedID = file.ID
			return nil
		},
	)
	if err != nil || !applied {
		t.Fatalf("persistIdentityUpdate = (%v, %v), want applied", applied, err)
	}
	if mf.ID != 42 || enqueuedID != 42 {
		t.Fatalf("updated/enqueued IDs = %d/%d, want 42/42", mf.ID, enqueuedID)
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

func TestSyncVanishedVideoQueuesUsesParentAndExactFileScopes(t *testing.T) {
	series := &recordingQueueSyncer{}
	movie := &recordingQueueSyncer{}
	filePath := filepath.Join("/media", "Movie", "Movie.mkv")
	scanner := &Scanner{seriesQueueSyncer: series, movieQueueSyncer: movie}

	if err := scanner.syncVanishedVideoQueues(context.Background(), 17, filePath); err != nil {
		t.Fatalf("syncVanishedVideoQueues: %v", err)
	}
	if series.folderID != 17 || series.scope != filepath.Dir(filePath) {
		t.Fatalf("series sync = folder %d scope %q", series.folderID, series.scope)
	}
	if movie.folderID != 17 || movie.scope != filepath.Clean(filePath) {
		t.Fatalf("movie sync = folder %d scope %q", movie.folderID, movie.scope)
	}
}
