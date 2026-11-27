package scanner

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A show in another order than its files: TMDB's S1E21 is the episode the
// TVDB-named file S02E01.mkv holds (file numbers 2×1), while TMDB's own S2E1
// is a different episode whose file name would be S17E01. The bulk link reads
// the file's numbers in the file order, not the episodes' shown numbers.
func TestBulkLinkEpisodesByFileNumbersDB(t *testing.T) {
	ctx := context.Background()
	pool := newScannerTestPool(t)

	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("file-numbers-series-%d", suffix)
	named := fmt.Sprintf("file-numbers-s1e21-%d", suffix)
	shown := fmt.Sprintf("file-numbers-s2e1-%d", suffix)
	plain := fmt.Sprintf("file-numbers-s1e1-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`,
		seriesID).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'File Numbers', 'matched', '{}'::text[])`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, file_season_number, file_episode_number, title)
		VALUES ($1, $4, 1, 21, 2, 1, 'Named S02E01'),
		       ($2, $4, 2, 1, 17, 1, 'Shown S2E1'),
		       ($3, $4, 1, 1, NULL, NULL, 'Pilot')`, named, shown, plain, seriesID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}

	repo := NewFileRepository(pool)
	files := map[string]int{}
	for _, f := range []struct {
		name            string
		season, episode int
	}{{"S02E01", 2, 1}, {"S01E01", 1, 1}} {
		file, err := repo.Upsert(ctx, models.MediaFile{
			ContentID: seriesID, MediaFolderID: folderID, FileSize: 1,
			FilePath:     fmt.Sprintf("/tmp/file-numbers-%d-%s.mkv", suffix, f.name),
			SeasonNumber: f.season, EpisodeNumber: f.episode,
		})
		if err != nil {
			t.Fatalf("seed file %s: %v", f.name, err)
		}
		files[f.name] = file.ID
	}

	if linked, err := repo.BulkLinkEpisodesBySeries(ctx, seriesID); err != nil || linked != 2 {
		t.Fatalf("bulk link = %d, %v; want 2", linked, err)
	}
	for name, want := range map[string]string{"S02E01": named, "S01E01": plain} {
		var got string
		var season, episode int
		if err := pool.QueryRow(ctx, `SELECT episode_id, season_number, episode_number FROM media_files WHERE id = $1`,
			files[name]).Scan(&got, &season, &episode); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s linked to %s, want %s", name, got, want)
		}
		if fmt.Sprintf("S%02dE%02d", season, episode) != name {
			t.Fatalf("%s numbers became S%02dE%02d; a link keeps the file's own numbers", name, season, episode)
		}
	}
}
