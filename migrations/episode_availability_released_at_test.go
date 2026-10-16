package migrations

import "testing"

func TestEpisodeAvailabilityReleasedAtBackfillPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE episode_availability (
	library_id INTEGER NOT NULL, episode_id TEXT NOT NULL, available_at TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (library_id, episode_id)
);
CREATE TABLE notification_library_seed_state (library_id INTEGER PRIMARY KEY, seeded_at TIMESTAMPTZ NOT NULL);
CREATE TABLE episode_libraries (
	episode_id TEXT NOT NULL, media_folder_id INTEGER NOT NULL, first_seen_at TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (episode_id, media_folder_id)
);
INSERT INTO notification_library_seed_state VALUES (1, NOW() - INTERVAL '100 days');
INSERT INTO episode_availability VALUES
	(1, 'back-catalog', NOW() - INTERVAL '101 days'),
	(1, 'new', NOW() - INTERVAL '5 days'),
	(1, 'late-match', NOW() - INTERVAL '5 days'),
	(1, 'no-membership', NOW() - INTERVAL '5 days'),
	(1, 'restored', NOW() - INTERVAL '5 days'),
	(2, 'unseeded', NOW() - INTERVAL '5 days');
INSERT INTO episode_libraries VALUES
	('back-catalog', 1, NOW() - INTERVAL '101 days'),
	('new', 1, NOW() - INTERVAL '6 days'),
	('late-match', 1, NOW() - INTERVAL '60 days'),
	('restored', 1, NOW() - INTERVAL '1 day'),
	('unseeded', 2, NOW() - INTERVAL '5 days');`)

	migrationExec(t, tx, adminMigrationSQL(t, "20261016012917_add_episode_availability_released_at", schema, false))

	want := map[string]bool{"back-catalog": false, "new": true, "late-match": false, "no-membership": false, "restored": false, "unseeded": false}
	for id, released := range want {
		var got bool
		if err := tx.QueryRow(t.Context(),
			`SELECT released_at IS NOT DISTINCT FROM available_at AND released_at IS NOT NULL FROM episode_availability WHERE episode_id = $1`,
			id).Scan(&got); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got != released {
			t.Errorf("%s: released = %v, want %v", id, got, released)
		}
	}

	migrationExec(t, tx, adminMigrationSQL(t, "20261016012917_add_episode_availability_released_at", schema, true))
}
