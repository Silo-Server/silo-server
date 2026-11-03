package migrations

import "testing"

// A server set up before the wizard saved its sharing choice keeps sharing by
// default and keeps a saved choice. A database with no account yet stays
// unset unless it stores false.
func TestMarkersShareByDefaultExistingInstallsMigrationPostgres(t *testing.T) {
	const migration = "20261026133552_markers_share_by_default_existing_installs"
	for _, tc := range []struct {
		name     string
		accounts bool
		stored   string // "" leaves the row out
		want     string
	}{
		{"set up, no choice", true, "", "true"},
		{"set up, sharing off", true, "false", "false"},
		{"new server", false, "", "no row"},
		{"new server, sharing off", false, "false", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, schema := adminMigrationFixture(t)
			migrationExec(t, tx, `CREATE TABLE server_settings (key text PRIMARY KEY, value text NOT NULL);
CREATE TABLE users (id bigint PRIMARY KEY);`)
			if tc.accounts {
				migrationExec(t, tx, `INSERT INTO users (id) VALUES (1)`)
			}
			if tc.stored != "" {
				if _, err := tx.Exec(t.Context(), `INSERT INTO server_settings (key, value) VALUES ('markers.share_by_default', $1)`, tc.stored); err != nil {
					t.Fatal(err)
				}
			}
			migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
			var got string
			if err := tx.QueryRow(t.Context(), `SELECT
    COALESCE((SELECT value FROM server_settings WHERE key = 'markers.share_by_default'), 'no row')`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("markers.share_by_default = %q, want %q", got, tc.want)
			}
		})
	}
}
