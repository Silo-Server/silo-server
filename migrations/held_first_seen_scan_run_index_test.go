package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestHeldFirstSeenScanRunIndexRetryUsesConcurrentCleanup(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20261008023737_index_media_files_held_episode_first_seen_scan_run.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(migrationBytes)
	if !strings.HasPrefix(migration, "-- +goose NO TRANSACTION") {
		t.Fatal("concurrent index builds require NO TRANSACTION")
	}
	up := strings.Join(strings.Fields(strings.SplitN(migration, "-- +goose Down", 2)[0]), " ")
	const name = "idx_media_files_held_episode_first_seen_scan_run_id"
	drop := "DROP INDEX CONCURRENTLY IF EXISTS public." + name + ";"
	create := "CREATE INDEX CONCURRENTLY " + name + " ON public.media_files (held_episode_first_seen_scan_run_id) WHERE held_episode_first_seen_scan_run_id IS NOT NULL;"
	dropAt, createAt := strings.Index(up, drop), strings.Index(up, create)
	if dropAt < 0 || createAt < 0 || dropAt > createAt {
		t.Fatalf("migration must clean up a failed build concurrently before %q", create)
	}
}

// TestScanRunForeignKeysHaveLeadingIndexesDB guards library deletion: it
// cascades to every scan run of the library, and each deleted run makes
// PostgreSQL find the rows that still reference it. A foreign key without an
// index whose first column is the referencing column turns each of those
// lookups into a scan of the whole referencing table.
func TestScanRunForeignKeysHaveLeadingIndexesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	var hasScanRuns bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.scan_runs') IS NOT NULL`).Scan(&hasScanRuns); err != nil {
		t.Fatal(err)
	}
	if !hasScanRuns {
		t.Skip("test database has not applied the schema")
	}

	rows, err := conn.Query(ctx, `
		SELECT c.conrelid::regclass::text, a.attname
		FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		WHERE c.contype = 'f'
		  AND c.confrelid = 'public.scan_runs'::regclass
		  AND NOT EXISTS (
			SELECT 1 FROM pg_index i
			WHERE i.indrelid = c.conrelid
			  AND i.indkey[0] = c.conkey[1]
			  AND i.indisvalid
		  )
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		missing = append(missing, table+"."+column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("foreign keys to scan_runs without a leading index: %s", strings.Join(missing, ", "))
	}
}
