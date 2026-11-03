package intromarkers

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedSilenceBackfillFixture creates an intro-enabled series library with three
// episode files carrying a scanner chapter:v1 intro marker.
func seedSilenceBackfillFixture(t *testing.T, pool *pgxpool.Pool) []int {
	t.Helper()
	ctx := t.Context()
	prefix := fmt.Sprintf("silence-backfill-%d-", time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled, intro_detection_enabled) VALUES ('series', $1, true, true) RETURNING id`, prefix).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	seriesID := prefix + "series"
	seasonID := prefix + "season"
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, stmt := range []string{
			`DELETE FROM media_files WHERE media_folder_id = $1`,
			`DELETE FROM media_folders WHERE id = $1`,
		} {
			if _, err := pool.Exec(cleanup, stmt, folderID); err != nil {
				t.Errorf("clean silence backfill fixture: %v", err)
			}
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID); err != nil {
			t.Errorf("clean silence backfill fixture: %v", err)
		}
	})
	exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Silence backfill')`, seriesID)
	exec(`INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1, $2, 1)`, seasonID, seriesID)

	chapters := `[{"index":0,"title":"Opening","start_seconds":60,"end_seconds":120},{"index":1,"title":"Part 1","start_seconds":120,"end_seconds":1400}]`
	fileIDs := make([]int, 0, 3)
	for episode := 1; episode <= 3; episode++ {
		episodeID := fmt.Sprintf("%sepisode-%d", prefix, episode)
		exec(`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number) VALUES ($1, $2, $3, 1, $4)`, episodeID, seriesID, seasonID, episode)
		// media_files.id is bigint; the last file sits past the int32 range so the
		// attempts table has to hold it.
		id := any(nil)
		if episode == 3 {
			id = int64(math.MaxInt32) + time.Now().UnixNano()%1_000_000_000 + 1
		}
		var fileID int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (
			    id, media_folder_id, file_path, episode_id, season_number, episode_number,
			    file_hash, file_size, duration, chapters,
			    intro_start, intro_end, intro_markers_source, intro_markers_confidence,
			    intro_markers_algorithm, intro_markers_detected_at
			) VALUES (COALESCE($7::bigint, nextval(pg_get_serial_sequence('media_files', 'id'))),
			        $1, $2, $3, 1, $4, $5, 1000000, 1500, $6::jsonb,
			        60, 120, 'scanner', 0.95, 'chapter:v1', '2026-08-08T03:30:00Z')
			RETURNING id`,
			folderID, fmt.Sprintf("/%s/e%d.mkv", prefix, episode), episodeID, episode,
			fmt.Sprintf("%shash-%d", prefix, episode), chapters, id,
		).Scan(&fileID); err != nil {
			t.Fatal(err)
		}
		fileIDs = append(fileIDs, fileID)
	}
	return fileIDs
}
