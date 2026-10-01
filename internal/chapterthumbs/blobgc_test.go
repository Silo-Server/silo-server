package chapterthumbs

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
	live, err := BlobNamespace(pool).Live(t.Context(), []string{livePrefix, deadPrefix})
	if err != nil {
		t.Fatal(err)
	}
	if !live[livePrefix] || live[deadPrefix] {
		t.Fatalf("live %v, want only %s", live, livePrefix)
	}
}
