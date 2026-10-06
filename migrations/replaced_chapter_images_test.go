package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestReplacedChapterImagesMigrationPostgres checks that the deletion queue
// accepts one chapter thumbnail's key and nothing looser.
func TestReplacedChapterImagesMigrationPostgres(t *testing.T) {
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

	migrationExec(t, tx, `INSERT INTO blob_gc_queue (prefix, not_before) VALUES
		('chapter-images/12/0/w300.webp', now()), ('chapter-images/12/15/w640.webp', now()),
		('chapter-images/12/', now()), ('trickplay/12/9913/', now())`)
	for _, prefix := range []string{
		"chapter-images/12/0/original.webp", "chapter-images/12/00/w300.webp", "chapter-images/12/0/w0300.webp",
		"chapter-images/12/0/w.webp", "chapter-images/12/0/w300.jpg", "chapter-images/12/0/w300.webpx",
		"chapter-images/12/0/", "chapter-images/12/0/1/w300.webp", "chapter-images/012/0/w300.webp",
	} {
		requireMigrationSQLState(t, tx, `INSERT INTO blob_gc_queue (prefix, not_before) VALUES ('`+prefix+`', now())`, "23514")
	}
}
