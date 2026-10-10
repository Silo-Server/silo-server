package scanner

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestListMissingChapterThumbnailsFollowsTheWidth lists a file whose
// thumbnail was made at another width, unless that chapter is waiting out a
// failure.
func TestListMissingChapterThumbnailsFollowsTheWidth(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled, chapter_thumbnails_enabled) VALUES ('movies', $1, true, true) RETURNING id`,
		fmt.Sprintf("chapter width %d", time.Now().UnixNano()),
	).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	later := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	insert := func(name, chapter string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, chapters) VALUES ($1, $2, 1, $3::jsonb) RETURNING id`,
			folderID, "/chapter-width/"+name+".mkv",
			`[{"index":0,"start_seconds":0,"end_seconds":60,"source":"embedded",`+chapter+`}]`,
		).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	at300 := insert("at300", `"thumbnail_path":"chapter-images/1/0/w300.webp"`)
	at320 := insert("at320", `"thumbnail_path":"chapter-images/2/0/w320.webp"`)
	waiting := insert("waiting", `"thumbnail_path":"chapter-images/3/0/w300.webp","thumbnail_retry_after":"`+later+`"`)
	missing := insert("missing", `"thumbnail_path":""`)

	repo := NewFileRepository(pool)
	listed := func(suffix string) []int {
		t.Helper()
		files, err := repo.ListMissingChapterThumbnails(ctx, 100000, suffix)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int
		for _, f := range files {
			if f.ID == at300 || f.ID == at320 || f.ID == waiting {
				ids = append(ids, f.ID)
			}
		}
		slices.Sort(ids)
		return ids
	}
	if got := listed("/w300.webp"); !slices.Equal(got, []int{at320}) {
		t.Fatalf("at 300 px listed %v, want only %d", got, at320)
	}
	if got := listed("/w320.webp"); !slices.Equal(got, []int{at300}) {
		t.Fatalf("at 320 px listed %v, want only %d", got, at300)
	}
	files, pending, err := repo.ListChapterThumbnailsAtOtherWidths(ctx, 100000, "/w320.webp", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	var own []int
	for _, file := range files {
		if file.MediaFolderID == folderID {
			own = append(own, file.ID)
		}
	}
	if !slices.Equal(own, []int{at300, missing}) {
		t.Fatalf("width backfill listed %v, want %d and missing file %d", own, at300, missing)
	}
	if pending.IsZero() {
		t.Fatal("width backfill forgot images waiting out a failure")
	}
	files, pending, err = repo.ListChapterThumbnailsAtOtherWidths(ctx, 100000, "/w320.webp", at300, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.ID == at300 {
			t.Fatalf("cursor repeated file %d", at300)
		}
	}
	if pending.IsZero() {
		t.Fatal("last page did not keep the backfill pending")
	}
}

// TestChapterWidthScanCanSkipHDRFiles leaves out HDR and Dolby Vision files
// when asked, as the width backfill does while the HDR policy is disabled.
func TestChapterWidthScanCanSkipHDRFiles(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled, chapter_thumbnails_enabled) VALUES ('movies', $1, true, true) RETURNING id`,
		fmt.Sprintf("chapter width hdr %d", time.Now().UnixNano()),
	).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	insert := func(name string, hdr bool, tracks string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, hdr, video_tracks, chapters)
			VALUES ($1, $2, 1, $3, $4::jsonb, '[{"index":0,"start_seconds":0,"end_seconds":60,"source":"embedded","thumbnail_path":"chapter-images/1/0/w300.webp"}]'::jsonb)
			RETURNING id`, folderID, "/chapter-width-hdr/"+name+".mkv", hdr, tracks).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sdr := insert("sdr", false, `[{"codec":"h264"}]`)
	hdr := insert("hdr", true, `[{"codec":"hevc"}]`)
	dovi := insert("dovi", false, `[{"codec":"hevc","dolby_vision":"dvhe.08"}]`)

	repo := NewFileRepository(pool)
	listed := func(skipHDR bool) []int {
		t.Helper()
		files, _, err := repo.ListChapterThumbnailsAtOtherWidths(ctx, 100000, "/w320.webp", 0, skipHDR)
		if err != nil {
			t.Fatal(err)
		}
		var own []int
		for _, file := range files {
			if file.MediaFolderID == folderID {
				own = append(own, file.ID)
			}
		}
		return own
	}
	if got := listed(false); !slices.Equal(got, []int{sdr, hdr, dovi}) {
		t.Fatalf("listed %v, want every file", got)
	}
	if got := listed(true); !slices.Equal(got, []int{sdr}) {
		t.Fatalf("listed %v with HDR skipped, want only %d", got, sdr)
	}
}
