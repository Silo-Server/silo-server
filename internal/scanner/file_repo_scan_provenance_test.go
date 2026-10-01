package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanbatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFileRepositoryPreservesFirstSeenScanProvenance(t *testing.T) {
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
	seriesID := fmt.Sprintf("scan-provenance-series-%d", suffix)
	episodeOneID := fmt.Sprintf("scan-provenance-episode-one-%d", suffix)
	episodeTwoID := fmt.Sprintf("scan-provenance-episode-two-%d", suffix)
	firstRunID := fmt.Sprintf("scan-provenance-first-%d", suffix)
	secondRunID := fmt.Sprintf("scan-provenance-second-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, seriesID).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'Scan Provenance', 'matched', '{}'::text[])
	`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, seriesID, folderID); err != nil {
		t.Fatalf("seed series membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $3, 1, 1, 'Episode One'), ($2, $3, 1, 2, 'Episode Two')
	`, episodeOneID, episodeTwoID, seriesID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO scan_runs (id, media_folder_id, mode, status)
		VALUES ($1, $3, 'library', 'completed'), ($2, $3, 'library', 'completed')
	`, firstRunID, secondRunID, folderID); err != nil {
		t.Fatalf("seed scan runs: %v", err)
	}

	repo := NewFileRepository(pool)
	filePath := fmt.Sprintf("/tmp/scan-provenance-%d-s01e01.mkv", suffix)
	file, err := repo.Upsert(scanbatch.WithRunID(ctx, firstRunID), models.MediaFile{
		ContentID:     seriesID,
		MediaFolderID: folderID,
		FilePath:      filePath,
		FileSize:      1024,
		SeasonNumber:  1,
		EpisodeNumber: 1,
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if file.FirstSeenScanRunID != firstRunID {
		t.Fatalf("first upsert provenance = %q, want %q", file.FirstSeenScanRunID, firstRunID)
	}
	if err := repo.UpdateEpisodeLink(ctx, file.ID, episodeOneID, 1, 1); err != nil {
		t.Fatalf("delayed episode link: %v", err)
	}
	assertEpisodeProvenance(t, ctx, pool, episodeOneID, folderID, firstRunID)

	rescanned, err := repo.Upsert(scanbatch.WithRunID(ctx, secondRunID), models.MediaFile{
		ContentID:     seriesID,
		EpisodeID:     episodeOneID,
		MediaFolderID: folderID,
		FilePath:      filePath,
		FileSize:      2048,
		SeasonNumber:  1,
		EpisodeNumber: 1,
	})
	if err != nil {
		t.Fatalf("rescan upsert: %v", err)
	}
	if rescanned.FirstSeenScanRunID != firstRunID {
		t.Fatalf("rescan overwrote provenance: got %q, want %q", rescanned.FirstSeenScanRunID, firstRunID)
	}

	version, err := repo.Upsert(scanbatch.WithRunID(ctx, secondRunID), models.MediaFile{
		ContentID:     seriesID,
		EpisodeID:     episodeOneID,
		MediaFolderID: folderID,
		FilePath:      fmt.Sprintf("/tmp/scan-provenance-%d-s01e01-4k.mkv", suffix),
		FileSize:      4096,
		SeasonNumber:  1,
		EpisodeNumber: 1,
	})
	if err != nil {
		t.Fatalf("additional version upsert: %v", err)
	}
	if err := repo.UpdateEpisodeLink(ctx, version.ID, episodeOneID, 1, 1); err != nil {
		t.Fatalf("additional version link: %v", err)
	}
	assertEpisodeProvenance(t, ctx, pool, episodeOneID, folderID, firstRunID)

	bulkFile, err := repo.Upsert(scanbatch.WithRunID(ctx, secondRunID), models.MediaFile{
		ContentID:     seriesID,
		MediaFolderID: folderID,
		FilePath:      fmt.Sprintf("/tmp/scan-provenance-%d-s01e02.mkv", suffix),
		FileSize:      1024,
		SeasonNumber:  1,
		EpisodeNumber: 2,
	})
	if err != nil {
		t.Fatalf("bulk-link candidate upsert: %v", err)
	}
	if bulkFile.FirstSeenScanRunID != secondRunID {
		t.Fatalf("bulk-link candidate provenance = %q, want %q", bulkFile.FirstSeenScanRunID, secondRunID)
	}
	linked, err := repo.BulkLinkEpisodesBySeries(ctx, seriesID)
	if err != nil || linked < 1 {
		t.Fatalf("bulk link = %d, err %v", linked, err)
	}
	assertEpisodeProvenance(t, ctx, pool, episodeTwoID, folderID, secondRunID)
}

func assertEpisodeProvenance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, episodeID string, folderID int, want string) {
	t.Helper()
	var got *string
	if err := pool.QueryRow(ctx, `
		SELECT first_seen_scan_run_id FROM episode_libraries
		WHERE episode_id = $1 AND media_folder_id = $2
	`, episodeID, folderID).Scan(&got); err != nil {
		t.Fatalf("read episode provenance: %v", err)
	}
	if got == nil || *got != want {
		t.Fatalf("episode %s provenance = %v, want %q", episodeID, got, want)
	}
}
