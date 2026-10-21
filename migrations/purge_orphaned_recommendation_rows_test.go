package migrations

import (
	"testing"
)

const purgeOrphanedRecommendationRowsMigration = "20261004003240_purge_orphaned_recommendation_rows"

// purgeFixtureSQL builds the tables the purge reads: account 1 keeps profile
// "live" and once had profile "gone"; account 2 has no profile in
// user_profiles, as on the SQLite user store. Every table holds a row for
// each of those profiles, and recommendation_cache also a global row.
const purgeFixtureSQL = `
CREATE TABLE users (id integer PRIMARY KEY);
CREATE TABLE user_profiles (user_id integer, id text, PRIMARY KEY (user_id, id));
CREATE TABLE server_settings (key text PRIMARY KEY, value text);
CREATE TABLE user_ratings (user_id integer, profile_id text, media_item_id text);
CREATE TABLE user_taste_profiles (user_id integer, profile_id text);
CREATE TABLE user_taste_clusters (user_id integer, profile_id text);
CREATE TABLE recommendation_cache (user_id integer, profile_id text);
INSERT INTO users VALUES (1), (2);
INSERT INTO user_profiles VALUES (1, 'live');
INSERT INTO user_ratings VALUES (1, 'live', 'm'), (1, 'gone', 'm'), (2, 'elsewhere', 'm');
INSERT INTO user_taste_profiles VALUES (1, 'live'), (1, 'gone'), (2, 'elsewhere');
INSERT INTO user_taste_clusters VALUES (1, 'live'), (1, 'gone'), (2, 'elsewhere');
INSERT INTO recommendation_cache VALUES (1, 'live'), (1, 'gone'), (2, 'elsewhere'), (NULL, '__global__');`

func TestPurgeOrphanedRecommendationRowsPostgres(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string // "" leaves the setting unset, which means postgres
		purged  bool
	}{
		{name: "postgres store, setting unset", purged: true},
		{name: "postgres store", backend: "postgres", purged: true},
		{name: "sqlite store", backend: "sqlite", purged: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, schema := adminMigrationFixture(t)
			migrationExec(t, tx, purgeFixtureSQL)
			if tc.backend != "" {
				migrationExec(t, tx, "INSERT INTO server_settings VALUES ('userdb.backend', '"+tc.backend+"')")
			}
			migrationExec(t, tx, adminMigrationSQL(t, purgeOrphanedRecommendationRowsMigration, schema, false))

			count := func(table, where string) int {
				t.Helper()
				var n int
				if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table+" WHERE "+where).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			for _, table := range []string{"user_ratings", "user_taste_profiles", "user_taste_clusters", "recommendation_cache"} {
				if got := count(table, "profile_id = 'live'"); got != 1 {
					t.Fatalf("%s: live profile rows = %d, want 1", table, got)
				}
				// An account with no profile in user_profiles is never
				// treated as orphaned: its profiles may live elsewhere.
				if got := count(table, "profile_id = 'elsewhere'"); got != 1 {
					t.Fatalf("%s: rows of an account without Postgres profiles = %d, want 1", table, got)
				}
				want := 1
				if tc.purged {
					want = 0
				}
				if got := count(table, "profile_id = 'gone'"); got != want {
					t.Fatalf("%s: deleted profile rows = %d, want %d", table, got, want)
				}
			}
			if got := count("recommendation_cache", "user_id IS NULL"); got != 1 {
				t.Fatalf("global rows = %d, want 1", got)
			}
		})
	}
}
