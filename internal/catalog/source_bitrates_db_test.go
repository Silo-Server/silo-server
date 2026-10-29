package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
)

// TestSourceBitratePercentileDB: the speed test's target reads the 99.9th
// percentile of the present video files a viewer can play, so one file with a
// nonsense bitrate does not set it, and missing files, audio-only files, cover
// art and files playback would refuse do not count: a library outside the
// viewer's access, an item also linked to a disabled library, and a title over
// the profile's maturity ceiling. A file counts at the bitrate playback plans it
// at.
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
	var played, disabled, empty, shared, rated, tracked int
	folders := []*int{&played, &disabled, &empty, &shared, &rated, &tracked}
	for _, id := range folders {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE file_path LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_item_libraries WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int{played, disabled, empty, shared, rated, tracked})
	})
	// item adds a movie linked to the given libraries, with an optional
	// minimum age, and count files of bitrate kbps in folder.
	item := func(name string, age *int, libraries []int, folder, count, kbps int) string {
		id := prefix + "-" + name
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, content_rating_age) VALUES ($1, 'movie', $1, 'released', '{}', $2)`, id, age); err != nil {
			t.Fatal(err)
		}
		for _, library := range libraries {
			if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video)
			SELECT $1, $2, $1 || '-' || n || '.mkv', $3, 'hevc' FROM generate_series(1, $4) n`, id, folder, kbps, count); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// 999 ordinary files and one that reports 9 Gbps.
	movie := item("movie", nil, []int{played}, played, 999, 20000)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video) VALUES ($1, $2, $1 || '-outlier.mkv', 9000000, 'hevc')`, []any{movie, played}},
		// Files that are not playable video in this library.
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video, missing_since) VALUES ($1, $2, $1 || '-missing.mkv', 90000000, 'hevc', now())`, []any{movie, played}},
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video) VALUES ($1, $2, $1 || '-audio.m4b', 900000, '')`, []any{movie, played}},
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video) VALUES ($1, $2, $1 || '-cover.mp3', 900000, 'mjpeg')`, []any{movie, played}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	// A library the viewer cannot play.
	item("disabled", nil, []int{disabled}, disabled, 50, 80000)
	// In a shared library, one title is also linked to the disabled library.
	item("shared", nil, []int{shared}, shared, 50, 10000)
	item("dual", nil, []int{shared, disabled}, shared, 50, 40000)
	// In another, one title is rated for adults.
	r := 17
	item("unrated", nil, []int{rated}, rated, 50, 30000)
	item("adult", &r, []int{rated}, rated, 50, 90000)
	// Playback plans a file at its first video track's bitrate when it has one
	// and reads a legacy bps figure as kbps: 50 files with no container bitrate
	// count at their track's 63000, 50 files of 70000 whose track is 31600 count
	// at 31600, and a legacy 63,000,000 counts at 63000.
	streams := item("streams", nil, []int{tracked}, tracked, 0, 0)
	for _, statement := range []string{
		`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video, video_tracks)
			SELECT $1, $2, $1 || '-' || n || '.mp4', CASE WHEN n <= 50 THEN 0 ELSE 70000 END, 'hevc',
			       jsonb_build_array(jsonb_build_object('codec', 'hevc', 'bitrate', CASE WHEN n <= 50 THEN 63000 ELSE 31600 END))
			FROM generate_series(1, 100) n`,
		`INSERT INTO media_files (content_id, media_folder_id, file_path, bitrate, codec_video) VALUES ($1, $2, $1 || '-legacy.mkv', 63000000, 'hevc')`,
	} {
		if _, err := pool.Exec(ctx, statement, streams, tracked); err != nil {
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
		{"shared library", AccessFilter{AllowedLibraryIDs: []int{shared}}, 40000},
		{"title also in a disabled library", AccessFilter{AllowedLibraryIDs: []int{shared, disabled}, DisabledLibraryIDs: []int{disabled}}, 10000},
		{"rated library", AccessFilter{AllowedLibraryIDs: []int{rated}}, 90000},
		{"title over the ceiling", AccessFilter{AllowedLibraryIDs: []int{rated}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13", AllowUnratedContent: true}}, 30000},
		{"video track bitrates", AccessFilter{AllowedLibraryIDs: []int{tracked}}, 63000},
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
