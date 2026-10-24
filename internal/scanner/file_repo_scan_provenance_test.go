package scanner

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanbatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFileRepositoryPreservesFirstSeenScanProvenance(t *testing.T) {
	ctx := context.Background()
	pool := newScannerTestPool(t)

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

// TestBulkLinkEpisodesAdvancesSeriesAvailableAirDateDB pins that a file
// reaching its episode through the bulk link moves the series' "Latest
// Episode Air Date" sort key, which counts only episodes with a file
// (issue #2741). An air-date correction that holds the series row while the
// link runs must not lose to the link's older view of the episode dates.
func TestBulkLinkEpisodesAdvancesSeriesAvailableAirDateDB(t *testing.T) {
	ctx := context.Background()
	pool := newScannerTestPool(t)

	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("bulk-link-air-date-series-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, seriesID).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'Bulk Link Air Date', 'matched', '{}'::text[])
	`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, air_date)
		VALUES ($1 || '-e1', $1, 1, 1, 'Episode One', CURRENT_DATE - 7),
		       ($1 || '-e2', $1, 1, 2, 'Episode Two', CURRENT_DATE - 1)
	`, seriesID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}

	repo := NewFileRepository(pool)
	for episode := 1; episode <= 2; episode++ {
		if _, err := repo.Upsert(ctx, models.MediaFile{
			ContentID:     seriesID,
			MediaFolderID: folderID,
			FilePath:      fmt.Sprintf("/tmp/bulk-link-air-date-%d-s01e%02d.mkv", suffix, episode),
			FileSize:      1024,
			SeasonNumber:  1,
			EpisodeNumber: episode,
		}); err != nil {
			t.Fatalf("upsert episode %d file: %v", episode, err)
		}
	}

	// A metadata correction moves episode two back a month and holds the
	// series row, as catalog's air-date recompute does, until the link is
	// waiting on it.
	correction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin correction: %v", err)
	}
	defer correction.Rollback(ctx) //nolint:errcheck // no-op after Commit
	var correctionPID int
	if err := correction.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&correctionPID); err != nil {
		t.Fatalf("correction pid: %v", err)
	}
	if _, err := correction.Exec(ctx, `UPDATE episodes SET air_date = CURRENT_DATE - 30 WHERE content_id = $1 || '-e2'`, seriesID); err != nil {
		t.Fatalf("correct episode two air date: %v", err)
	}
	if err := catalog.RecomputeSeriesEpisodeMembershipDenorms(ctx, correction, []string{seriesID}); err != nil {
		t.Fatalf("correction recompute: %v", err)
	}

	type linkResult struct {
		linked int
		err    error
	}
	done := make(chan linkResult, 1)
	go func() {
		linked, err := repo.BulkLinkEpisodesBySeries(ctx, seriesID)
		done <- linkResult{linked, err}
	}()
	waitForBlockedBy(t, pool, correctionPID)
	if err := correction.Commit(ctx); err != nil {
		t.Fatalf("commit correction: %v", err)
	}
	if res := <-done; res.err != nil || res.linked != 2 {
		t.Fatalf("bulk link = %d, err %v", res.linked, res.err)
	}

	var available, want string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(latest_available_episode_air_date_at::text, 'NULL'), (CURRENT_DATE - 7)::text
		FROM media_items WHERE content_id = $1
	`, seriesID).Scan(&available, &want); err != nil {
		t.Fatalf("read series latest available episode air date: %v", err)
	}
	if available != want {
		t.Fatalf("series latest_available_episode_air_date_at = %s, want %s", available, want)
	}
}

// waitForBlockedBy returns once some backend waits on a lock held by the
// backend holderPID.
func waitForBlockedBy(t *testing.T, pool *pgxpool.Pool, holderPID int) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var blocked bool
		if err := pool.QueryRow(waitCtx, `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND $1::int = ANY(pg_blocking_pids(pid)))
		`, holderPID).Scan(&blocked); err != nil {
			t.Fatalf("waiting for a backend to block on pid %d: %v", holderPID, err)
		}
		if blocked {
			return
		}
	}
}
