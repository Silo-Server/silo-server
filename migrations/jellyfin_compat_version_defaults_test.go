package migrations

import "testing"

const jellyfinCompatNewInstallDefaultsMigration = "20260923181531_jellyfin_compat_12_1_new_install_defaults"

func jellyfinCompatDefaultsFixture(t *testing.T, hasUser, completed bool, settings map[string]string) (func(string) string, func(bool)) {
	t.Helper()
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE users (id integer PRIMARY KEY);
CREATE TABLE server_settings (key text PRIMARY KEY, value text NOT NULL);`)
	for key, value := range settings {
		if _, err := tx.Exec(t.Context(), "INSERT INTO server_settings VALUES ($1, $2)", key, value); err != nil {
			t.Fatal(err)
		}
	}
	if hasUser {
		migrationExec(t, tx, "INSERT INTO users VALUES (1)")
	}
	if completed {
		migrationExec(t, tx, "INSERT INTO server_settings VALUES ('setup.completed', 'true')")
	}
	read := func(key string) string {
		t.Helper()
		var got string
		if err := tx.QueryRow(t.Context(), "SELECT COALESCE((SELECT value FROM server_settings WHERE key = $1), '')", key).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	run := func(down bool) {
		t.Helper()
		migrationExec(t, tx, adminMigrationSQL(t, jellyfinCompatNewInstallDefaultsMigration, schema, down))
	}
	return read, run
}

// The Jellyfin 12.1 emulated-version default reaches only databases that have
// not been set up; configured servers keep their version for the admin to
// change.
func TestJellyfinCompatEmulatedVersionMigrationPreservesConfiguredServersPostgres(t *testing.T) {
	for _, tt := range []struct {
		name      string
		version   string
		hasUser   bool
		completed bool
		want      string
	}{
		{"fresh database", "10.12.0", false, false, "12.1.0"},
		{"existing seeded default", "10.12.0", true, true, "10.12.0"},
		{"setup account created", "10.12.0", true, false, "10.12.0"},
		{"completed without account", "10.12.0", false, true, "10.12.0"},
		{"unconfigured explicit version", "10.11.8", false, false, "10.11.8"},
		{"existing explicit version", "10.11.8", true, true, "10.11.8"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			read, run := jellyfinCompatDefaultsFixture(t, tt.hasUser, tt.completed, map[string]string{"jellyfin_compat.emulated_server_version": tt.version})
			run(false)
			if got := read("jellyfin_compat.emulated_server_version"); got != tt.want {
				t.Fatalf("emulated_server_version = %q, want %q", got, tt.want)
			}
			run(true)
			if got := read("jellyfin_compat.emulated_server_version"); got != tt.want {
				t.Fatalf("rollback changed emulated_server_version: %q", got)
			}
		})
	}
}

// Configured servers without a stored Jellyfin Web version are pinned to the
// previous 10.11.6 default; fresh databases follow the new 12.1 default and
// explicit choices are left to the admin.
func TestJellyfinCompatWebVersionMigrationPinsConfiguredServersPostgres(t *testing.T) {
	for _, tt := range []struct {
		name      string
		stored    string
		emptyRow  bool
		hasUser   bool
		completed bool
		want      string
	}{
		{"fresh database", "", false, false, false, ""},
		{"configured without stored version", "", false, true, true, "10.11.6"},
		{"configured with an empty stored version", "", true, true, true, "10.11.6"},
		{"configured with a tab-only stored version", "\t", false, true, true, "10.11.6"},
		{"fresh with an empty stored version", "", true, false, false, " "},
		{"setup account created", "", false, true, false, "10.11.6"},
		{"completed without account", "", false, false, true, "10.11.6"},
		{"configured explicit version", "12.1", false, true, true, "12.1"},
		{"fresh explicit version", "10.11.8", false, false, false, "10.11.8"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			settings := map[string]string{}
			if tt.stored != "" {
				settings["jellyfin_compat.web_version"] = tt.stored
			}
			if tt.emptyRow {
				settings["jellyfin_compat.web_version"] = " "
			}
			read, run := jellyfinCompatDefaultsFixture(t, tt.hasUser, tt.completed, settings)
			run(false)
			if got := read("jellyfin_compat.web_version"); got != tt.want {
				t.Fatalf("web_version = %q, want %q", got, tt.want)
			}
		})
	}
}
