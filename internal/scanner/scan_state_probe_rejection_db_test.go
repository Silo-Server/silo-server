package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestScanStateCarriesProbeRejectionDB checks the scan state a library scan
// reads: a file ffprobe rejected keeps its mark there, so a rescan of the
// unchanged file skips the probe repair.
func TestScanStateCarriesProbeRejectionDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("probe-rejection-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	modifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	rejectedAt := modifiedAt.Add(time.Minute)
	repo := NewFileRepository(pool)
	if _, err := repo.Upsert(ctx, models.MediaFile{
		MediaFolderID:  folderID,
		FilePath:       fmt.Sprintf("/tmp/probe-rejection-%d.mkv", suffix),
		FileSize:       1_000,
		FileModifiedAt: &modifiedAt,
		ProbeFailedAt:  &rejectedAt,
	}); err != nil {
		t.Fatalf("seed rejected file: %v", err)
	}

	states, err := repo.GetScanStateByFolder(ctx, folderID)
	if err != nil {
		t.Fatalf("GetScanStateByFolder: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("scan state rows = %d, want 1", len(states))
	}
	state := states[0]
	if state.ProbeFailedAt == nil || state.ProbeUpdatedAt != nil {
		t.Fatalf("scan state probe_failed_at = %v, probe_updated_at = %v, want a rejection and no probe", state.ProbeFailedAt, state.ProbeUpdatedAt)
	}
	reasons := scanStateUpdateReasons(state, 1_000, modifiedAt, nil, false, fileRootAssignment{}, fileGroupAssignment{}, "movies", true)
	if testStringSliceContains(reasons, "probe_repair") {
		t.Fatalf("rescan reasons = %#v, want no probe_repair for the unchanged rejected file", reasons)
	}
}

// TestRescanKeepsProbeFactsOfARejectedUnchangedFileDB covers a row probed
// before rejections were tracked: it keeps old probe facts without
// probe_updated_at, and ffprobe now rejects it. A rescan for a new subtitle
// sidecar probes it again and fails; that must record the subtitle without
// overwriting the probe facts the row still has.
func TestRescanKeepsProbeFactsOfARejectedUnchangedFileDB(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := t.Context()
	root := t.TempDir()
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("rejected-legacy-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}
	moviePath := filepath.Join(root, "Legacy Movie (2020).mkv")
	if err := os.WriteFile(moviePath, []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(moviePath)
	if err != nil {
		t.Fatal(err)
	}
	modifiedAt := info.ModTime().UTC()
	rejectedAt := time.Now().UTC()
	repo := NewFileRepository(pool)
	if _, err := repo.Upsert(ctx, models.MediaFile{
		MediaFolderID:     folderID,
		FilePath:          moviePath,
		FileSize:          info.Size(),
		FileModifiedAt:    &modifiedAt,
		CodecVideo:        "h264",
		Container:         "matroska",
		Duration:          5400,
		ProbeFailedAt:     &rejectedAt,
		SubtitleTracks:    []models.SubtitleTrack{},
		ExternalSubtitles: []models.ExternalSubtitle{},
	}); err != nil {
		t.Fatalf("seed rejected legacy file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Legacy Movie (2020).en.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(repo, writeRejectingFFprobe(t), nil, 1, false, 0)
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	stored, err := repo.GetByPath(ctx, moviePath)
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if stored.CodecVideo != "h264" || stored.Container != "matroska" || stored.Duration != 5400 {
		t.Fatalf("probe facts after rescan = %q %q %d, want h264 matroska 5400", stored.CodecVideo, stored.Container, stored.Duration)
	}
	if len(stored.ExternalSubtitles) != 1 {
		t.Fatalf("external subtitles after rescan = %+v, want the new sidecar", stored.ExternalSubtitles)
	}
	if stored.ProbeFailedAt == nil || stored.ProbeUpdatedAt != nil {
		t.Fatalf("probe state after rescan: failed=%v updated=%v, want the rejection kept", stored.ProbeFailedAt, stored.ProbeUpdatedAt)
	}
}
