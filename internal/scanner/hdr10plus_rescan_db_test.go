package scanner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A rescan reprobes an unchanged file whenever something beside it changes,
// such as a downloaded subtitle. When that rescan's first-frame read fails,
// the HDR10+ status an earlier read recorded must survive.
func TestRescanKeepsHDR10PlusWhenTheFrameReadFailsDB(t *testing.T) {
	ctx := t.Context()
	pool := newScannerTestPool(t)

	root := t.TempDir()
	moviePath := filepath.Join(root, "Heat (1995) {tmdb-949}", "Heat (1995) {tmdb-949} [Bluray-2160p].mkv")
	writeTestFile(t, moviePath, "movie")

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', 'HDR10+ Rescan Test', true)
		RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folderID, root); err != nil {
		t.Fatalf("seed folder path: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1
			)`, folderID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}, Enabled: true}
	repo := NewFileRepository(pool)

	readsHDR10Plus, _ := writeHDR10PlusFFprobe(t, hdr10PlusStreamsJSON, "printf '%s' '"+hdr10PlusFrameJSON+"'")
	if _, err := NewScanner(repo, readsHDR10Plus, nil, 1, false, 0).ScanFolder(ctx, folder); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	first, err := repo.GetByPath(ctx, moviePath)
	if err != nil {
		t.Fatalf("reading the scanned file: %v", err)
	}
	if !first.VideoTracks[0].HDR10Plus {
		t.Fatalf("first scan stored %+v, want HDR10+", first.VideoTracks[0])
	}

	writeTestFile(t, filepath.Join(filepath.Dir(moviePath), "Heat (1995) {tmdb-949} [Bluray-2160p].en.srt"), "1\n00:00:01,000 --> 00:00:02,000\nHi\n")
	frameReadFails, _ := writeHDR10PlusFFprobe(t, hdr10PlusStreamsJSON, "echo 'movie.mkv: Input/output error' >&2; exit 1")
	if _, err := NewScanner(repo, frameReadFails, nil, 1, false, 0).ScanFolder(ctx, folder); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	rescanned, err := repo.GetByPath(ctx, moviePath)
	if err != nil {
		t.Fatalf("reading the rescanned file: %v", err)
	}
	if len(rescanned.ExternalSubtitles) == 0 {
		t.Fatal("the rescan did not rewrite the row, so it did not exercise the reprobe")
	}
	track := rescanned.VideoTracks[0]
	if !track.HDR10Plus || track.HDR10PlusUnchecked || track.VideoRangeType != "HDR10Plus" {
		t.Fatalf("rescan stored %+v, want the HDR10+ status the first scan read", track)
	}
}

// The HDR10+ backfill marks a file whose read decoded no frame so passes skip
// it for a day. A rescan reprobes the file and rewrites its track, which
// drops the mark, so the next pass reads it again (#3606).
func TestRescanClearsTheHDR10PlusNoFrameMarkDB(t *testing.T) {
	ctx := t.Context()
	pool := newScannerTestPool(t)

	root := t.TempDir()
	moviePath := filepath.Join(root, "Sicario (2015) {tmdb-273481}", "Sicario (2015) {tmdb-273481} [Bluray-2160p].mkv")
	writeTestFile(t, moviePath, "movie")

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', 'HDR10+ No Frame Test', true)
		RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folderID, root); err != nil {
		t.Fatalf("seed folder path: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1
			)`, folderID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}, Enabled: true}
	repo := NewFileRepository(pool)

	// ffprobe opens the file but decodes no frame, on every read.
	noFrame, _ := writeHDR10PlusFFprobe(t, hdr10PlusStreamsJSON, `printf '%s' '{"frames":[]}'`)
	scanner := NewScanner(repo, noFrame, nil, 1, false, 0)
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	scanned, err := repo.GetByPath(ctx, moviePath)
	if err != nil {
		t.Fatalf("reading the scanned file: %v", err)
	}
	noFrameMark := func() bool {
		t.Helper()
		var marked bool
		if err := pool.QueryRow(ctx, `SELECT video_tracks->0 ? '`+hdr10PlusNoFrameKey+`' FROM media_files WHERE id = $1`, scanned.ID).Scan(&marked); err != nil {
			t.Fatal(err)
		}
		return marked
	}

	backfiller := NewHDR10PlusBackfiller(repo, noFrame)
	backfiller.readFrame = func(ctx context.Context, ffprobePath, filePath, streamSpecifier string) ([]ffprobeSideData, error) {
		if filePath != moviePath {
			// Another test's row: leave it untouched.
			return nil, errors.New("not this test's file")
		}
		return probeVideoFrameSideData(ctx, ffprobePath, filePath, streamSpecifier)
	}
	if _, err := backfiller.Run(ctx, nil); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !noFrameMark() {
		t.Fatal("the backfill did not mark the file whose read decoded no frame")
	}

	writeTestFile(t, filepath.Join(filepath.Dir(moviePath), "Sicario (2015) {tmdb-273481} [Bluray-2160p].en.srt"), "1\n00:00:01,000 --> 00:00:02,000\nHi\n")
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	rescanned, err := repo.GetByPath(ctx, moviePath)
	if err != nil {
		t.Fatalf("reading the rescanned file: %v", err)
	}
	if len(rescanned.ExternalSubtitles) == 0 {
		t.Fatal("the rescan did not rewrite the row, so it did not exercise the reprobe")
	}
	if noFrameMark() {
		t.Fatal("the rescan kept the no-frame mark")
	}
	if !rescanned.VideoTracks[0].HDR10PlusUnchecked {
		t.Fatalf("rescan stored %+v, want the track still unchecked", rescanned.VideoTracks[0])
	}
}
