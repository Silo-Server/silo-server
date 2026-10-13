package chapterthumbs

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobgc"
	"github.com/Silo-Server/silo-server/internal/blobstore"
)

func TestImagesGroup(t *testing.T) {
	tests := map[string]string{
		"chapter-images/42/0/w300.webp":         "chapter-images/42/",
		"chapter-images/42/":                    "chapter-images/42/",
		"chapter-images/7/12/original.webp":     "chapter-images/7/",
		"chapter-images/042/0/w300.webp":        "",
		"chapter-images/0/0/w300.webp":          "",
		"chapter-images/-3/0/w300.webp":         "",
		"chapter-images/abc/0/w300.webp":        "",
		"chapter-images/42":                     "",
		"chapter-images/99999999999999999999/0": "",
		"trickplay/42/1/0.1.jpg":                "",
	}
	for key, want := range tests {
		got, ok := imagesGroup(key)
		if got != want || ok != (want != "") {
			t.Errorf("imagesGroup(%q) = %q, %t; want %q", key, got, ok, want)
		}
	}
}

// TestBlobNamespaceLiveDB checks that a prefix is live exactly while its
// media file's row exists.
func TestBlobNamespaceLiveDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	var folderID, fileID int
	if err := tx.QueryRow(t.Context(), `INSERT INTO public.media_folders (type, name) VALUES ('movies', 'blobgc live test') RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(t.Context(), `INSERT INTO public.media_files (media_folder_id, file_path) VALUES ($1, '/blobgc/live-test.mkv') RETURNING id`, folderID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM public.media_folders WHERE id = $1`, folderID)
	})
	livePrefix := chapterImagesPrefix + strconv.Itoa(fileID) + "/"
	deadPrefix := chapterImagesPrefix + "2147483648/"
	live, err := BlobNamespace().Live(t.Context(), pool, []string{livePrefix, deadPrefix})
	if err != nil {
		t.Fatal(err)
	}
	if !live[livePrefix] || live[deadPrefix] {
		t.Fatalf("live %v, want only %s", live, livePrefix)
	}
	deadKey := deadPrefix + "0/w300.webp"
	live, err = ImageBlobNamespace().Live(t.Context(), pool, []string{deadKey})
	if err != nil || live[deadKey] {
		t.Fatalf("high-ID image liveness: %v, %v", live, err)
	}
}

// TestReplacedImagesAreCollectedDB retires a chapter's 300 px image after a
// 320 px one replaced it: nothing is deleted during the grace, then only the
// image no chapter references goes, and a key taken off the queue before a
// write is never deleted.
func TestReplacedImagesAreCollectedDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var folderID, fileID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO public.media_folders (type, name) VALUES ('movies', 'replaced images test') RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM public.media_folders WHERE id = $1`, folderID)
	})
	if err := pool.QueryRow(t.Context(), `INSERT INTO public.media_files (media_folder_id, file_path) VALUES ($1, '/blobgc/replaced.mkv') RETURNING id`, folderID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	key := func(chapter, width int) string {
		return chapterThumbnailKey(fileID, chapter, width)
	}
	chapters := fmt.Sprintf(`[{"index":0,"start_seconds":0,"end_seconds":60,"source":"embedded","thumbnail_path":%q},{"index":1,"start_seconds":60,"end_seconds":120,"source":"embedded","thumbnail_path":%q}]`,
		key(0, 320), key(1, 320))
	if _, err := pool.Exec(t.Context(), `UPDATE public.media_files SET chapters = $2::jsonb WHERE id = $1`, fileID, chapters); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM public.blob_gc_queue WHERE prefix LIKE $1`, chapterImagesPrefix+strconv.Itoa(fileID)+"/%")
	})

	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{key(0, 300), key(0, 320), key(1, 300), key(1, 320)} {
		if err := store.Put(t.Context(), k, []byte("webp")); err != nil {
			t.Fatal(err)
		}
	}
	queue := blobgc.NewQueue(pool)
	// Chapter 0's replacement retired the 300 px image; chapter 1's width
	// change was undone before its write, which took the key off the queue.
	if err := queue.Schedule(t.Context(), []string{key(0, 300), key(0, 320), key(1, 320)}, displacedImageGrace); err != nil {
		t.Fatal(err)
	}
	if err := queue.Cancel(t.Context(), []string{key(1, 320)}); err != nil {
		t.Fatal(err)
	}
	collector := blobgc.NewCollector(pool, store, BlobNamespace(), ImageBlobNamespace())
	// The database is shared, so this reads only this file's rows and
	// objects rather than the collector's counts.
	pattern := chapterImagesPrefix + strconv.Itoa(fileID) + "/%"
	queued := func() []string {
		t.Helper()
		if _, err := collector.Collect(t.Context(), 100); err != nil {
			t.Fatal(err)
		}
		rows, err := pool.Query(t.Context(), `SELECT prefix FROM public.blob_gc_queue WHERE prefix LIKE $1 ORDER BY prefix`, pattern)
		if err != nil {
			t.Fatal(err)
		}
		prefixes, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return prefixes
	}
	stored := func() []string {
		t.Helper()
		var left []string
		for _, k := range []string{key(0, 300), key(0, 320), key(1, 300), key(1, 320)} {
			if objects, _, err := store.List(t.Context(), k, "", 1); err != nil {
				t.Fatal(err)
			} else if len(objects) > 0 {
				left = append(left, k)
			}
		}
		return left
	}
	if got := queued(); !slices.Equal(got, []string{key(0, 300), key(0, 320)}) || len(stored()) != 4 {
		t.Fatalf("collected during the grace: queued %v, stored %v", got, stored())
	}

	if _, err := pool.Exec(t.Context(), `UPDATE public.blob_gc_queue SET not_before = now() - interval '1 second' WHERE prefix LIKE $1`, pattern); err != nil {
		t.Fatal(err)
	}
	if got := queued(); len(got) != 0 {
		t.Fatalf("still queued: %v", got)
	}
	// The referenced 320 px image was kept; chapter 1's 300 px image was
	// never queued, so only its file's deletion removes it.
	if got, want := stored(), []string{key(0, 320), key(1, 300), key(1, 320)}; !slices.Equal(got, want) {
		t.Fatalf("stored %v, want %v", got, want)
	}
}
