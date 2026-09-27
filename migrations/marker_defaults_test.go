package migrations

import "testing"

func TestMarkerDefaultsMigrationPreservesConfiguredServersPostgres(t *testing.T) {
	for _, tt := range []struct {
		name      string
		mode      string
		hasUser   bool
		completed bool
		want      string
	}{
		{"fresh database", "online", false, false, "both"},
		{"existing online", "online", true, true, "online"},
		{"setup account created", "online", true, false, "online"},
		{"completed without account", "online", false, true, "online"},
		{"existing local", "local", true, true, "local"},
		{"existing off", "off", true, true, "off"},
		{"existing both", "both", true, true, "both"},
		{"unconfigured explicit local", "local", false, false, "local"},
		{"unconfigured explicit off", "off", false, false, "off"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tx, schema := adminMigrationFixture(t)
			migrationExec(t, tx, `
CREATE TABLE users (id integer PRIMARY KEY);
CREATE TABLE server_settings (key text PRIMARY KEY, value text NOT NULL);`)
			if _, err := tx.Exec(t.Context(), "INSERT INTO server_settings VALUES ('markers.mode', $1)", tt.mode); err != nil {
				t.Fatal(err)
			}
			if tt.hasUser {
				migrationExec(t, tx, "INSERT INTO users VALUES (1)")
			}
			if tt.completed {
				migrationExec(t, tx, "INSERT INTO server_settings VALUES ('setup.completed', 'true')")
			}
			const migration = "20260921234440_default_markers_online_with_local_fallback"
			migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
			var got string
			if err := tx.QueryRow(t.Context(), "SELECT value FROM server_settings WHERE key = 'markers.mode'").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("markers.mode = %q, want %q", got, tt.want)
			}
			migrationExec(t, tx, "UPDATE server_settings SET value = 'off' WHERE key = 'markers.mode'")
			migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
			if err := tx.QueryRow(t.Context(), "SELECT value FROM server_settings WHERE key = 'markers.mode'").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != "off" {
				t.Fatalf("rollback overwrote customized markers.mode: %q", got)
			}
		})
	}
}
