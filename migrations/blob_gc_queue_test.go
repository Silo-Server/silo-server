package migrations

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestBlobGCQueueTriggerPostgres deletes media files directly and through a
// library's cascade, against the migrated schema in a transaction that is
// rolled back. Only files whose chapters record a thumbnail are queued, a
// day out, and the queue refuses prefixes outside the derived namespaces.
func TestBlobGCQueueTriggerPostgres(t *testing.T) {
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

	var folder int
	if err := tx.QueryRow(t.Context(), `INSERT INTO media_folders (type, name) VALUES ('movies', 'blob gc trigger test') RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	insert := func(path, chapters string) int {
		t.Helper()
		var id int
		if err := tx.QueryRow(t.Context(), `INSERT INTO media_files (media_folder_id, file_path, chapters) VALUES ($1, $2, $3::jsonb) RETURNING id`,
			folder, path, chapters).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	withThumbnail := insert("/gc/a.mkv", `[{"index":0,"title":"One","start_seconds":0,"end_seconds":60,"source":"embedded","thumbnail_path":"chapter-images/1/0/w300.webp"},{"index":1,"title":"Two","start_seconds":60,"end_seconds":120,"source":"embedded"}]`)
	withoutThumbnail := insert("/gc/b.mkv", `[{"index":0,"title":"One","start_seconds":0,"end_seconds":60,"source":"embedded"}]`)
	withoutChapters := insert("/gc/c.mkv", `null`)
	cascaded := insert("/gc/d.mkv", `[{"index":0,"title":"One","start_seconds":0,"end_seconds":60,"source":"embedded","thumbnail_path":"chapter-images/4/0/w300.webp"}]`)

	queued := func() map[string]time.Duration {
		t.Helper()
		rows, err := tx.Query(t.Context(), `SELECT prefix, not_before - now() FROM blob_gc_queue WHERE prefix = ANY($1)`,
			[]string{prefixOf(withThumbnail), prefixOf(withoutThumbnail), prefixOf(withoutChapters), prefixOf(cascaded)})
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]time.Duration{}
		for rows.Next() {
			var prefix string
			var wait time.Duration
			if err := rows.Scan(&prefix, &wait); err != nil {
				t.Fatal(err)
			}
			result[prefix] = wait
		}
		return result
	}

	if _, err := tx.Exec(t.Context(), `DELETE FROM media_files WHERE id = ANY($1)`, []int{withThumbnail, withoutThumbnail, withoutChapters}); err != nil {
		t.Fatal(err)
	}
	got := queued()
	if len(got) != 1 || got[prefixOf(withThumbnail)] != 24*time.Hour {
		t.Fatalf("queued %v, want only %s a day out", got, prefixOf(withThumbnail))
	}
	if _, err := tx.Exec(t.Context(), `DELETE FROM media_folders WHERE id = $1`, folder); err != nil {
		t.Fatal(err)
	}
	if got := queued(); len(got) != 2 || got[prefixOf(cascaded)] != 24*time.Hour {
		t.Fatalf("after the library cascade queued %v, want %s too", got, prefixOf(cascaded))
	}

	for _, prefix := range []string{"chapter-images/0/", "chapter-images/12", "chapter-images/12/0/", "local/12/", "", "chapter-images/012/"} {
		requireMigrationSQLState(t, tx, `INSERT INTO blob_gc_queue (prefix, not_before) VALUES ('`+prefix+`', now())`, "23514")
	}
}

func prefixOf(id int) string {
	return "chapter-images/" + strconv.Itoa(id) + "/"
}
