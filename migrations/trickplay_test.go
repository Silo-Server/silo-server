package migrations

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestTrickplayMigrationPostgres checks, against the migrated schema in a
// rolled-back transaction, that the deletion queue accepts trickplay
// revision prefixes and nothing looser, and that a trickplay row's state and
// manifest stay consistent.
func TestTrickplayMigrationPostgres(t *testing.T) {
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

	migrationExec(t, tx, `INSERT INTO blob_gc_queue (prefix, not_before) VALUES ('trickplay/12/9913/', now()), ('chapter-images/12/', now())`)
	for _, prefix := range []string{"trickplay/12/", "trickplay/12/0/", "trickplay/012/5/", "trickplay/12/5/0.5.jpg", "trickplay/"} {
		requireMigrationSQLState(t, tx, `INSERT INTO blob_gc_queue (prefix, not_before) VALUES ('`+prefix+`', now())`, "23514")
	}

	var folder, file int
	if err := tx.QueryRow(t.Context(), `INSERT INTO media_folders (type, name) VALUES ('movies', 'trickplay migration test') RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err := tx.QueryRow(t.Context(), `SELECT trickplay_enabled FROM media_folders WHERE id = $1`, folder).Scan(&enabled); err != nil || enabled {
		t.Fatalf("trickplay must default to off: %t %v", enabled, err)
	}
	if err := tx.QueryRow(t.Context(), `INSERT INTO media_files (media_folder_id, file_path) VALUES ($1, '/trickplay/migration.mkv') RETURNING id`, folder).Scan(&file); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO media_file_trickplay (media_file_id, recipe_version, state) VALUES ($1, 1, 'running')`,
		`INSERT INTO media_file_trickplay (media_file_id, recipe_version, state) VALUES ($1, 1, 'done')`,
		`INSERT INTO media_file_trickplay (media_file_id, recipe_version, work_revision) VALUES ($1, 1, 5)`,
		`INSERT INTO media_file_trickplay (media_file_id, recipe_version, revision) VALUES ($1, 1, 5)`,
	} {
		requireMigrationSQLState(t, tx, fmtFileID(sql, file), "23514")
	}
}

func fmtFileID(sql string, file int) string {
	return strings.ReplaceAll(sql, "$1", strconv.Itoa(file))
}
