package migrations

import "testing"

// The access-token lifetime moves from the old 1h seed to the 8h default. A
// value an administrator chose, a refresh lifetime under two days, and a
// missing row keep the access lifetime as it is; the refresh row is never
// touched.
func TestAccessTokenExpiryMigrationPostgres(t *testing.T) {
	const migration = "20261029170809_access_token_expiry_8h"
	for _, tc := range []struct {
		name    string
		stored  string // "" leaves the row out
		refresh string // "" leaves the row out
		want    string
	}{
		{"old seed", "1h", "30d", "8h"},
		{"old seed, refresh two days in hours", "1h", "48h", "8h"},
		{"old seed, no refresh row", "1h", "", "8h"},
		{"refresh under two days", "1h", "47h", "1h"},
		{"refresh one day", "1h", "1d", "1h"},
		{"refresh in another format", "1h", "1h30m", "1h"},
		{"admin value", "2h", "30d", "2h"},
		{"no row", "", "30d", "no row"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, schema := adminMigrationFixture(t)
			migrationExec(t, tx, `CREATE TABLE server_settings (key text PRIMARY KEY, value text NOT NULL);`)
			for key, value := range map[string]string{"auth.access_token_expiry": tc.stored, "auth.refresh_token_expiry": tc.refresh} {
				if value == "" {
					continue
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, key, value); err != nil {
					t.Fatal(err)
				}
			}
			migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
			var access, refresh string
			if err := tx.QueryRow(t.Context(), `SELECT
    COALESCE((SELECT value FROM server_settings WHERE key = 'auth.access_token_expiry'), 'no row'),
    COALESCE((SELECT value FROM server_settings WHERE key = 'auth.refresh_token_expiry'), '')`).Scan(&access, &refresh); err != nil {
				t.Fatal(err)
			}
			if access != tc.want || refresh != tc.refresh {
				t.Errorf("access_token_expiry = %q, refresh_token_expiry = %q; want %q and %q", access, refresh, tc.want, tc.refresh)
			}
		})
	}
}
