package scanner

import (
	"context"
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
