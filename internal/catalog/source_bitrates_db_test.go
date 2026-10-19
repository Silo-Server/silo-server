package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSourceBitratePercentileDB: the speed test's target reads the 99.9th
// percentile of the present video files a viewer can play, so one file with a
// nonsense bitrate does not set it, and missing files, audio-only files, cover
// art and libraries outside the viewer's access do not count.
func TestSourceBitratePercentileDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := fmt.Sprintf("bitrates-%d", time.Now().UnixNano())
	var played, disabled, empty int
	for _, id := range []*int{&played, &disabled, &empty} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE file_path LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int{played, disabled, empty})
	})
	movie := prefix + "-movie"
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'released', '{}')`, []any{movie}},
		// 999 ordinary files and one that reports 11.7 Gbps.
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video)
			SELECT $1, $2, $3 || '-' || n || '.mkv', 20000, 'hevc' FROM generate_series(1, 999) n`, []any{movie, played, prefix}},
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video) VALUES ($1, $2, $3 || '-outlier.mkv', 11700000, 'hevc')`, []any{movie, played, prefix}},
		// Files that are not playable video in this library.
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video, missing_since) VALUES ($1, $2, $3 || '-missing.mkv', 90000000, 'hevc', now())`, []any{movie, played, prefix}},
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video) VALUES ($1, $2, $3 || '-audio.m4b', 900000, '')`, []any{movie, played, prefix}},
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video) VALUES ($1, $2, $3 || '-cover.mp3', 900000, 'mjpeg')`, []any{movie, played, prefix}},
		// A library the viewer cannot play.
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video)
			SELECT $1, $2, $3 || '-disabled-' || n || '.mkv', 80000, 'hevc' FROM generate_series(1, 50) n`, []any{movie, disabled, prefix}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	source := &SourceBitrates{Pool: pool}
	for _, tc := range []struct {
		name   string
		filter AccessFilter
		want   int
	}{
		{"played library", AccessFilter{AllowedLibraryIDs: []int{played}}, 20000},
		{"disabled library excluded", AccessFilter{AllowedLibraryIDs: []int{played, disabled}, DisabledLibraryIDs: []int{disabled}}, 20000},
		{"disabled library alone", AccessFilter{AllowedLibraryIDs: []int{disabled}}, 80000},
		{"no files", AccessFilter{AllowedLibraryIDs: []int{empty}}, 0},
		{"no libraries", AccessFilter{AllowedLibraryIDs: []int{}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := source.Percentile(ctx, tc.filter, 0.999)
			if err != nil || got != tc.want {
				t.Fatalf("percentile = %d (%v), want %d", got, err, tc.want)
			}
		})
	}
}
