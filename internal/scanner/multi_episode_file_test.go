package scanner

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A multi-episode file links to its first episode only. Every episode its
// range spans must be listed in the library and resolve to the file, and lose
// its membership with the file (issue #739).
func TestMultiEpisodeFileServesSpannedEpisodesDB(t *testing.T) {
	ctx := context.Background()
	pool := newScannerTestPool(t)

	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("multi-episode-series-%d", suffix)
	episodeIDs := []string{
		fmt.Sprintf("multi-episode-e1-%d", suffix),
		fmt.Sprintf("multi-episode-e2-%d", suffix),
		fmt.Sprintf("multi-episode-e3-%d", suffix),
	}
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
		VALUES ($1, 'series', 'Multi Episode', 'matched', '{}'::text[])
	`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $4, 1, 1, 'One'), ($2, $4, 1, 2, 'Two'), ($3, $4, 1, 3, 'Three')
	`, episodeIDs[0], episodeIDs[1], episodeIDs[2], seriesID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}

	repo := NewFileRepository(pool)
	file, err := repo.Upsert(ctx, models.MediaFile{
		ContentID:         seriesID,
		MediaFolderID:     folderID,
		FilePath:          fmt.Sprintf("/tmp/multi-episode-%d/Season 01/Show - S01E01-E02.mkv", suffix),
		FileSize:          1024,
		SeasonNumber:      1,
		EpisodeNumber:     1,
		PresentationKind:  "multi_episode",
		MultiEpisodeStart: 1,
		MultiEpisodeEnd:   2,
	})
	if err != nil {
		t.Fatalf("upsert file: %v", err)
	}
	if err := repo.UpdateEpisodeLink(ctx, file.ID, episodeIDs[0], 1, 1); err != nil {
		t.Fatalf("link file: %v", err)
	}
	assertEpisodeMemberships(t, ctx, pool, folderID, episodeIDs[:2])

	spanned, err := repo.GetByEpisodeID(ctx, episodeIDs[1])
	if err != nil {
		t.Fatalf("files of the spanned episode: %v", err)
	}
	if len(spanned) != 1 || spanned[0].ID != file.ID {
		t.Fatalf("spanned episode files = %v, want file %d", spanned, file.ID)
	}
	grouped, err := repo.ListByEpisodeIDs(ctx, episodeIDs)
	if err != nil {
		t.Fatalf("files by episodes: %v", err)
	}
	if len(grouped[episodeIDs[0]]) != 1 || len(grouped[episodeIDs[1]]) != 1 || len(grouped[episodeIDs[2]]) != 0 {
		t.Fatalf("files by episode = %v, want the file under the first two episodes only", grouped)
	}

	// A folder reconcile restores a lost membership of a spanned episode.
	if _, err := pool.Exec(ctx, `DELETE FROM episode_libraries WHERE episode_id = $1`, episodeIDs[1]); err != nil {
		t.Fatalf("drop spanned membership: %v", err)
	}
	memberships := catalog.NewEpisodeLibraryRepository(pool)
	if _, err := memberships.ReconcileFolderMembership(ctx, folderID); err != nil {
		t.Fatalf("reconcile folder: %v", err)
	}
	assertEpisodeMemberships(t, ctx, pool, folderID, episodeIDs[:2])

	if _, err := pool.Exec(ctx, `UPDATE media_files SET missing_since = NOW() WHERE id = $1`, file.ID); err != nil {
		t.Fatalf("mark file missing: %v", err)
	}
	// A scoped scan names the file's own episode; the spanned one goes too.
	if _, err := memberships.RemoveStaleEpisodeMemberships(ctx, folderID, episodeIDs[:1]); err != nil {
		t.Fatalf("remove stale memberships: %v", err)
	}
	assertEpisodeMemberships(t, ctx, pool, folderID, nil)
}

func assertEpisodeMemberships(t *testing.T, ctx context.Context, pool *pgxpool.Pool, folderID int, want []string) {
	t.Helper()
	var got []string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(array_agg(episode_id ORDER BY episode_id), ARRAY[]::text[])
		FROM episode_libraries
		WHERE media_folder_id = $1
	`, folderID).Scan(&got); err != nil {
		t.Fatalf("load episode memberships: %v", err)
	}
	want = slices.Sorted(slices.Values(want))
	if !slices.Equal(got, want) {
		t.Fatalf("episode memberships = %v, want %v", got, want)
	}
}
