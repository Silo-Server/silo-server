package migrations

import (
	"context"
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
	conn := migratedContentIDFamilyConn(t)
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

// TestContentIDFamilyReferencesUseCCollationPostgres guards the content ID
// collation (docs/architecture/deterministic-content-id.md): every column with
// a foreign key to media_items, seasons or episodes must have the collation of
// the column it references, COLLATE "C". Across two collations a join on
// content_id cannot use the referencing table's index, so a NOT EXISTS against
// it filters a scan of the whole table for each outer row. A new table that
// references the family must declare the collation itself.
func TestContentIDFamilyReferencesUseCCollationPostgres(t *testing.T) {
	conn := migratedContentIDFamilyConn(t)
	rows, err := conn.Query(t.Context(), `SELECT DISTINCT con.conrelid::regclass::text || '.' || a.attname
FROM pg_constraint con
CROSS JOIN LATERAL unnest(con.conkey, con.confkey) AS k(attnum, refnum)
JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
JOIN pg_attribute r ON r.attrelid = con.confrelid AND r.attnum = k.refnum
WHERE con.contype = 'f'
  AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass)
  AND a.attcollation <> r.attcollation
ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(wrong) > 0 {
		t.Fatalf("content_id-family references without COLLATE \"C\": %s", strings.Join(wrong, ", "))
	}
}

// migratedContentIDFamilyConn connects to the fully migrated database the DB
// pins job provides.
func migratedContentIDFamilyConn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	var migrated bool
	if err := conn.QueryRow(t.Context(), "SELECT to_regclass('public.media_items') IS NOT NULL").Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Skip("database is not migrated")
	}
	return conn
}
