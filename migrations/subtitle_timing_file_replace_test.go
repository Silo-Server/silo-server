package migrations

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestSubtitleTimingFileReplacePostgres checks, in a rolled-back transaction,
// that replacing a media file removes its subtitles' timing corrections and
// sync history, and that an unknown hash or size, or any other update, keeps
// them.
func TestSubtitleTimingFileReplacePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	var folder, file, other int
	if err := tx.QueryRow(t.Context(), `INSERT INTO media_folders (type, name) VALUES ('movies', 'subtitle timing replace test') RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		path string
		id   *int
	}{{"/replace/movie.mkv", &file}, {"/replace/other.mkv", &other}} {
		if err := tx.QueryRow(t.Context(), `INSERT INTO media_files (media_folder_id, file_path, file_hash, file_size)
			VALUES ($1, $2, 'aaaaaaaaaaaaaaaa', 1000) RETURNING id`, folder, target.path).Scan(target.id); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(fileID int) int {
		t.Helper()
		var subtitleID int
		if err := tx.QueryRow(t.Context(), `INSERT INTO downloaded_subtitles (media_file_id, provider, language, format, s3_key, timing_offset_ms, timing_scale)
			VALUES ($1, 'upload', 'en', 'srt', $2, -1200, 1.04271) RETURNING id`, fileID, fmt.Sprintf("subtitles/replace-%d", fileID)).Scan(&subtitleID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO subtitle_sync_jobs (subtitle_id, media_file_id, trigger, base_revision, status)
			VALUES ($1, $2, 'manual', 1, 'synced')`, subtitleID, fileID); err != nil {
			t.Fatal(err)
		}
		return subtitleID
	}
	subtitle, otherSubtitle := seed(file), seed(other)
	kept := func(subtitleID int, want bool) {
		t.Helper()
		var offset, jobs int
		if err := tx.QueryRow(t.Context(), `SELECT timing_offset_ms, (SELECT count(*) FROM subtitle_sync_jobs WHERE subtitle_id = $1)
			FROM downloaded_subtitles WHERE id = $1`, subtitleID).Scan(&offset, &jobs); err != nil {
			t.Fatal(err)
		}
		if got := offset == -1200 && jobs == 1; got != want {
			t.Fatalf("subtitle %d: offset %d, jobs %d; want kept=%t", subtitleID, offset, jobs, want)
		}
		if !want && (offset != 0 || jobs != 0) {
			t.Fatalf("subtitle %d partly reset: offset %d, jobs %d", subtitleID, offset, jobs)
		}
	}

	for _, update := range []string{
		`UPDATE media_files SET file_modified_at = now() WHERE id = $1`,
		`UPDATE media_files SET file_hash = '' WHERE id = $1`,
		`UPDATE media_files SET file_hash = 'aaaaaaaaaaaaaaaa' WHERE id = $1`,
		`UPDATE media_files SET file_size = NULL WHERE id = $1`,
		`UPDATE media_files SET file_size = 1000 WHERE id = $1`,
		`UPDATE media_files SET file_hash = 'aaaaaaaaaaaaaaaa', file_size = 1000 WHERE id = $1`,
	} {
		migrationExec(t, tx, fmtFileID(update, file))
		kept(subtitle, true)
	}

	migrationExec(t, tx, fmtFileID(`UPDATE media_files SET file_hash = 'bbbbbbbbbbbbbbbb' WHERE id = $1`, file))
	kept(subtitle, false)
	kept(otherSubtitle, true)

	migrationExec(t, tx, fmtFileID(`UPDATE media_files SET file_size = 2000 WHERE id = $1`, other))
	kept(otherSubtitle, false)
}
