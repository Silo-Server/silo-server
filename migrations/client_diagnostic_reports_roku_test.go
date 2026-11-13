package migrations

import (
	"strings"
	"testing"
)

// The platform constraint accepts roku after the migration, keeps every stored
// report, still rejects macos, and stays wide on rollback.
func TestClientDiagnosticReportsRokuPlatformPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `CREATE TABLE users (id integer PRIMARY KEY); INSERT INTO users VALUES (1);`)
	data, err := FS.ReadFile("sql/20260720084550_client_diagnostic_reports.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(data), "-- +goose Down")
	migrationExec(t, tx, up)

	insert := func(id, platform string) string {
		return `INSERT INTO client_diagnostic_reports (id, short_id, user_id, state, captured_at, report_type, platform, app_version, manifest)
VALUES ('` + id + `', 'SILO-` + id[len(id)-12:] + `', 1, 'ready', now(), 'manual', '` + platform + `', '1.0.0', '{}')`
	}
	migrationExec(t, tx, insert("00000000-0000-0000-0000-000000000001", "android-tv"))
	requireMigrationSQLState(t, tx, insert("00000000-0000-0000-0000-000000000002", "roku"), "23514")

	const migration = "20261031021616_allow_roku_client_diagnostic_reports"
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	migrationExec(t, tx, insert("00000000-0000-0000-0000-000000000002", "roku"))
	requireMigrationSQLState(t, tx, insert("00000000-0000-0000-0000-000000000003", "macos"), "23514")

	var platforms string
	if err := tx.QueryRow(t.Context(), `SELECT string_agg(platform, ',' ORDER BY id) FROM client_diagnostic_reports`).Scan(&platforms); err != nil {
		t.Fatal(err)
	}
	if platforms != "android-tv,roku" {
		t.Fatalf("platforms = %q, want android-tv,roku", platforms)
	}

	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	migrationExec(t, tx, insert("00000000-0000-0000-0000-000000000004", "roku"))
}
