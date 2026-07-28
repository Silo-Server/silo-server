package scanner

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFirstDurationsByIDs(t *testing.T) {
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
	movieID := fmt.Sprintf("duration-movie-%d", suffix)
	seriesID := fmt.Sprintf("duration-series-%d", suffix)
	episodeOneID := fmt.Sprintf("duration-episode-one-%d", suffix)
	episodeTwoID := fmt.Sprintf("duration-episode-two-%d", suffix)

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('mixed', 'Duration Lookup Test', true)
		RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movieID, seriesID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES
			($1, 'movie', 'Duration Movie', 'matched', '{}'::text[]),
			($2, 'series', 'Duration Series', 'matched', '{}'::text[])
	`, movieID, seriesID); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES
			($1, $3, 1, 1, 'Episode One'),
			($2, $3, 1, 2, 'Episode Two')
	`, episodeOneID, episodeTwoID, seriesID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}

	fileNumber := 0
	seedFile := func(contentID, episodeID string, duration int, missing bool) {
		t.Helper()
		fileNumber++
		var episodeValue any
		if episodeID != "" {
			episodeValue = episodeID
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (
				content_id, episode_id, media_folder_id, file_path, file_size,
				duration, missing_since
			) VALUES ($1, $2, $3, $4, 1024, $5,
				CASE WHEN $6 THEN NOW() ELSE NULL END)
		`, contentID, episodeValue, folderID,
			fmt.Sprintf("/tmp/duration-lookup-%d-%d.mkv", suffix, fileNumber),
			duration, missing,
		); err != nil {
			t.Fatalf("seed file %d: %v", fileNumber, err)
		}
	}

	// Content lookup: skip missing and zero-duration files, then choose the
	// first positive live file by id.
	seedFile(movieID, "", 900, true)
	seedFile(movieID, "", 0, false)
	seedFile(movieID, "", 120, false)
	seedFile(movieID, "", 240, false)
	// Episode files share the parent series content_id and must never make the
	// series appear to have a directly-backed duration. The episode lookup
	// independently follows the same live/positive/first-id rule.
	seedFile(seriesID, episodeOneID, 800, true)
	seedFile(seriesID, episodeOneID, 0, false)
	seedFile(seriesID, episodeOneID, 300, false)
	seedFile(seriesID, episodeOneID, 600, false)
	seedFile(seriesID, episodeTwoID, 0, false)

	repo := NewFileRepository(pool)
	contentDurations, err := repo.FirstDurationsByContentIDs(ctx, []string{movieID, seriesID})
	if err != nil {
		t.Fatalf("FirstDurationsByContentIDs: %v", err)
	}
	if want := map[string]int{movieID: 120}; !reflect.DeepEqual(contentDurations, want) {
		t.Fatalf("content durations = %v, want %v", contentDurations, want)
	}

	episodeDurations, err := repo.FirstDurationsByEpisodeIDs(ctx, []string{episodeOneID, episodeTwoID})
	if err != nil {
		t.Fatalf("FirstDurationsByEpisodeIDs: %v", err)
	}
	if want := map[string]int{episodeOneID: 300}; !reflect.DeepEqual(episodeDurations, want) {
		t.Fatalf("episode durations = %v, want %v", episodeDurations, want)
	}
}
