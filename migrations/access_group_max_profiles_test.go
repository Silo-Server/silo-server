package migrations

import "testing"

func TestAccessGroupMaxProfilesMigrationPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	// The users column as 093 left it, and the group columns the revision
	// trigger reads.
	migrationExec(t, tx, `
CREATE TABLE server_settings (key text PRIMARY KEY, value text);
CREATE TABLE users (id integer PRIMARY KEY, role text NOT NULL, access_group_id bigint);
CREATE TABLE access_groups (
    id bigint PRIMARY KEY, name text NOT NULL, description text NOT NULL DEFAULT '', library_ids integer[],
    max_playback_quality text NOT NULL DEFAULT '', download_allowed boolean NOT NULL DEFAULT true,
    download_transcode_allowed boolean NOT NULL DEFAULT true, transcode_allowed boolean NOT NULL DEFAULT true,
    audio_transcode_allowed boolean NOT NULL DEFAULT true, max_streams integer NOT NULL DEFAULT 0,
    max_transcodes integer NOT NULL DEFAULT 0, max_remote_stream_bitrate_kbps integer NOT NULL DEFAULT 0,
    max_local_stream_bitrate_kbps integer NOT NULL DEFAULT 0, allowed_permissions text[],
    requests_allowed boolean NOT NULL DEFAULT true, is_default boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());`)
	for _, name := range []string{"056_user_max_profiles", "093_user_max_profiles_floor", "20260906000909_add_access_group_configuration_revisions"} {
		migrationExec(t, tx, adminMigrationSQL(t, name, schema, false))
	}
	migrationExec(t, tx, `
INSERT INTO access_groups (id, name) VALUES (1, 'Household'), (2, 'Guests');
INSERT INTO users (id, role, access_group_id) VALUES (1, 'user', 1), (2, 'user', 2), (4, 'admin', 2), (5, 'user', NULL);
INSERT INTO users (id, role, access_group_id, max_profiles) VALUES (3, 'user', 1, 2), (6, 'admin', 2, 8);`)

	const migration = "20261002210227_access_group_max_profiles"
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))

	// A stored 5 was the default and now inherits; other values stay overrides.
	limits := func() string {
		t.Helper()
		var got string
		if err := tx.QueryRow(t.Context(), `SELECT string_agg(id || '=' || COALESCE(max_profiles::text, 'inherit'), ' ' ORDER BY id) FROM users`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got, want := limits(), "1=inherit 2=inherit 3=2 4=inherit 5=inherit 6=8"; got != want {
		t.Fatalf("user limits after Up = %s, want %s", got, want)
	}
	var groups string
	if err := tx.QueryRow(t.Context(), `SELECT string_agg(id || '=' || max_profiles, ' ' ORDER BY id) FROM access_groups`).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if groups != "1=5 2=5" {
		t.Fatalf("group limits after Up = %s, want 1=5 2=5", groups)
	}
	migrationExec(t, tx, `INSERT INTO users (id, role) VALUES (7, 'user')`)
	requireMigrationSQLState(t, tx, `UPDATE users SET max_profiles = 0 WHERE id = 7`, "23514")
	requireMigrationSQLState(t, tx, `UPDATE access_groups SET max_profiles = 0 WHERE id = 1`, "23514")

	// The revision trigger sees the new column.
	revision := func() int64 {
		t.Helper()
		var got int64
		if err := tx.QueryRow(t.Context(), `SELECT configuration_revision FROM access_groups WHERE id = 2`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := revision()
	migrationExec(t, tx, `UPDATE access_groups SET max_profiles = 3 WHERE id = 2`)
	if revision() == before {
		t.Fatal("changing a group's profile limit kept its configuration revision")
	}

	// Down stores the limit each account resolved to: its group's for a
	// grouped account, 5 for an ungrouped one or an admin.
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	if got, want := limits(), "1=5 2=3 3=2 4=5 5=5 6=8 7=5"; got != want {
		t.Fatalf("user limits after Down = %s, want %s", got, want)
	}
	requireMigrationSQLState(t, tx, `INSERT INTO users (id, role, max_profiles) VALUES (8, 'user', NULL)`, "23502")
	before = revision()
	migrationExec(t, tx, `UPDATE access_groups SET description = 'changed' WHERE id = 2`)
	if revision() == before {
		t.Fatal("the restored revision trigger stopped advancing on a change")
	}
}
