package migrations

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestContentIDFamilyFKsCascadeOnUpdatePostgres guards the online re-id
// (silo_rename_content_id): every foreign key referencing media_items, seasons
// or episodes must carry ON UPDATE CASCADE, or renaming an item with rows in
// that table fails with a foreign key violation (#933). A new table that
// references the family must declare the cascade itself. Runs against a fully
// migrated database, as the DB pins job provides.
func TestContentIDFamilyFKsCascadeOnUpdatePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(t.Context()) }()
	var migrated bool
	if err := conn.QueryRow(t.Context(), "SELECT to_regclass('public.media_items') IS NOT NULL").Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Skip("database is not migrated")
	}
	rows, err := conn.Query(t.Context(), `SELECT con.conrelid::regclass::text || '.' || con.conname
FROM pg_constraint con
WHERE con.contype = 'f'
  AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass)
  AND con.confupdtype <> 'c'
ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("content_id-family foreign keys without ON UPDATE CASCADE: %s", strings.Join(missing, ", "))
	}
}
