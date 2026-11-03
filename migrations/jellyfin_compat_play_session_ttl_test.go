package migrations

import "testing"

// The Jellyfin play-session lifetime moves from the old 6h seed to 24h. A value
// an administrator chose, another setting holding 6h, and a missing row stay as
// they are.
func TestJellyfinCompatPlaySessionTTLMigrationPostgres(t *testing.T) {
	const migration = "20261022130254_jellyfin_compat_play_session_ttl_24h"
	for _, tc := range []struct {
		name   string
		stored string // "" leaves the row out
		want   string
	}{
		{"old seed", "6h", "24h"},
		{"admin value", "12h", "12h"},
		{"no row", "", "no row"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, schema := adminMigrationFixture(t)
			migrationExec(t, tx, `CREATE TABLE server_settings (key text PRIMARY KEY, value text NOT NULL);
INSERT INTO server_settings (key, value) VALUES ('jellyfin_compat.session_ttl', '6h');`)
			if tc.stored != "" {
				if _, err := tx.Exec(t.Context(), `INSERT INTO server_settings (key, value) VALUES ('jellyfin_compat.playback_session_ttl', $1)`, tc.stored); err != nil {
					t.Fatal(err)
				}
			}
			migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
			var ttl, other string
			if err := tx.QueryRow(t.Context(), `SELECT
    COALESCE((SELECT value FROM server_settings WHERE key = 'jellyfin_compat.playback_session_ttl'), 'no row'),
    (SELECT value FROM server_settings WHERE key = 'jellyfin_compat.session_ttl')`).Scan(&ttl, &other); err != nil {
				t.Fatal(err)
			}
			if ttl != tc.want || other != "6h" {
				t.Errorf("playback_session_ttl = %q, session_ttl = %q; want %q and 6h", ttl, other, tc.want)
			}
		})
	}
}
