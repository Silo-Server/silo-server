package migrations

import (
	"reflect"
	"testing"
)

// Turning local metadata on by default enables the built-in NFO rows of
// existing chains, including ones the startup sync appended disabled and last,
// moves them to the front, and keeps the other providers' order. Rolling back
// puts each row's priority and on/off state back as they were, including an
// NFO row an admin had turned on, keeps a field an admin edited after the
// upgrade, turns off NFO rows added since, gives per-level rows copied from a
// legacy chain after the upgrade that legacy row's state, and renumbers each
// chain so a restored priority never ties with one an admin set.
func TestLocalMetadataDefaultOnMigrationPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE plugin_installations (id bigint PRIMARY KEY, plugin_id text NOT NULL, kind text NOT NULL);
CREATE TABLE plugin_capabilities (plugin_installation_id bigint NOT NULL, capability_type text NOT NULL, capability_id text NOT NULL, metadata jsonb NOT NULL);
CREATE TABLE library_provider_chains (
    media_folder_id integer NOT NULL, plugin_installation_id bigint NOT NULL, capability_id text NOT NULL,
    capability_type text NOT NULL DEFAULT 'metadata_provider.v1', content_level text NOT NULL,
    priority integer NOT NULL, enabled boolean NOT NULL,
    PRIMARY KEY (media_folder_id, plugin_installation_id, capability_id, content_level));
INSERT INTO plugin_installations VALUES (1, 'silo.builtin', 'builtin'), (2, 'silo.tmdb', 'plugin'), (3, 'silo.tvdb', 'plugin'), (3000000000, 'silo.big', 'plugin');
INSERT INTO plugin_capabilities VALUES
    (1, 'metadata_provider.v1', 'nfo', '{"display_name":"NFO Files","default_priority":{"movie":1,"series":1,"season":1,"episode":1},"default_enabled":false}'),
    (2, 'metadata_provider.v1', 'tmdb', '{"metadata":{"default_priority":{"movie":1}}}');
-- 10: an upgraded movie library, NFO appended disabled and last.
-- 11: an upgraded series level with two remote providers.
-- 12: a library created after NFO existed, NFO first but disabled.
-- 13: an admin turned NFO on, still last.
-- 15: two remote providers, NFO off and last.
-- 19: an installation ID above the integer range.
-- 16, 17, 18: legacy unlevelled chains, NFO on (16) and off (17, 18), not yet
--             copied to per-level rows; 18 has two remote providers.
INSERT INTO library_provider_chains (media_folder_id, plugin_installation_id, capability_id, content_level, priority, enabled) VALUES
    (18, 2, 'tmdb', '', 0, true), (18, 3, 'tvdb', '', 1, true), (18, 1, 'nfo', '', 2, false),
    (19, 3000000000, 'big', 'movie', 0, true), (19, 1, 'nfo', 'movie', 1, false),
    (16, 2, 'tmdb', '', 0, true), (16, 1, 'nfo', '', 1, true),
    (17, 2, 'tmdb', '', 0, true), (17, 1, 'nfo', '', 1, false),
    (15, 2, 'tmdb', 'movie', 0, true), (15, 3, 'tvdb', 'movie', 1, true), (15, 1, 'nfo', 'movie', 2, false),
    (10, 2, 'tmdb', 'movie', 0, true), (10, 1, 'nfo', 'movie', 1, false),
    (11, 3, 'tvdb', 'series', 0, true), (11, 2, 'tmdb', 'series', 1, false), (11, 1, 'nfo', 'series', 2, false),
    (12, 1, 'nfo', 'movie', 0, false), (12, 2, 'tmdb', 'movie', 1, true),
    (13, 2, 'tmdb', 'movie', 0, true), (13, 1, 'nfo', 'movie', 1, true);`)

	type row struct {
		folder     int
		capability string
		priority   int
		enabled    bool
	}
	chains := func() []row {
		t.Helper()
		rows, err := tx.Query(t.Context(), `SELECT media_folder_id, capability_id, priority, enabled FROM library_provider_chains ORDER BY media_folder_id, content_level, priority, capability_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.folder, &r.capability, &r.priority, &r.enabled); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	defaultEnabled := func() bool {
		t.Helper()
		var enabled bool
		if err := tx.QueryRow(t.Context(), `SELECT (metadata->>'default_enabled')::boolean FROM plugin_capabilities WHERE capability_id = 'nfo'`).Scan(&enabled); err != nil {
			t.Fatal(err)
		}
		return enabled
	}

	const migration = "20261011201709_local_metadata_default_on"
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	if !defaultEnabled() {
		t.Error("nfo default_enabled not turned on")
	}
	want := []row{
		{10, "nfo", 0, true}, {10, "tmdb", 1, true},
		{11, "nfo", 0, true}, {11, "tvdb", 1, true}, {11, "tmdb", 2, false},
		{12, "nfo", 0, true}, {12, "tmdb", 1, true},
		{13, "nfo", 0, true}, {13, "tmdb", 1, true},
		{15, "nfo", 0, true}, {15, "tmdb", 1, true}, {15, "tvdb", 2, true},
		{16, "nfo", 0, true}, {16, "tmdb", 1, true},
		{17, "nfo", 0, true}, {17, "tmdb", 1, true},
		{18, "nfo", 0, true}, {18, "tmdb", 1, true}, {18, "tvdb", 2, true},
		{19, "nfo", 0, true}, {19, "big", 1, true},
	}
	if got := chains(); !reflect.DeepEqual(got, want) {
		t.Fatalf("chains after Up =\n%+v\nwant\n%+v", got, want)
	}

	// After the upgrade: a new library seeds NFO on, an admin turns on TMDB
	// in library 11, another turns NFO off in library 13 and swaps the remote
	// providers in library 15, the startup sync copies the legacy chains of
	// 16, 17 and 18 to per-level rows, and an admin swaps 18's copied remote
	// providers.
	migrationExec(t, tx, `INSERT INTO library_provider_chains (media_folder_id, plugin_installation_id, capability_id, content_level, priority, enabled) VALUES
    (14, 1, 'nfo', 'movie', 0, true), (14, 2, 'tmdb', 'movie', 1, true);
UPDATE library_provider_chains SET enabled = true WHERE media_folder_id = 11 AND capability_id = 'tmdb';
UPDATE library_provider_chains SET enabled = false WHERE media_folder_id = 13 AND capability_id = 'nfo';
UPDATE library_provider_chains SET priority = 3 - priority WHERE media_folder_id = 15 AND capability_id IN ('tmdb', 'tvdb');
INSERT INTO library_provider_chains (media_folder_id, plugin_installation_id, capability_id, content_level, priority, enabled)
SELECT media_folder_id, plugin_installation_id, capability_id, 'movie', priority, enabled
FROM library_provider_chains WHERE media_folder_id IN (16, 17, 18) AND content_level = '';
UPDATE library_provider_chains SET priority = 3 - priority WHERE media_folder_id = 18 AND content_level = 'movie' AND capability_id IN ('tmdb', 'tvdb');`)

	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	if defaultEnabled() {
		t.Error("nfo default_enabled not turned off on rollback")
	}
	want = []row{
		{10, "tmdb", 0, true}, {10, "nfo", 1, false},
		{11, "tvdb", 0, true}, {11, "tmdb", 1, true}, {11, "nfo", 2, false},
		{12, "nfo", 0, false}, {12, "tmdb", 1, true},
		{13, "tmdb", 0, true}, {13, "nfo", 1, false},
		{14, "nfo", 0, false}, {14, "tmdb", 1, true},
		// The admin's swap of the remote providers stays; NFO goes back last.
		{15, "tvdb", 0, true}, {15, "tmdb", 1, true}, {15, "nfo", 2, false},
		// Legacy rows and the per-level copies made after the upgrade both
		// return to the legacy chain's state, NFO on in 16 and off in 17.
		{16, "tmdb", 0, true}, {16, "nfo", 1, true}, {16, "tmdb", 0, true}, {16, "nfo", 1, true},
		{17, "tmdb", 0, true}, {17, "nfo", 1, false}, {17, "tmdb", 0, true}, {17, "nfo", 1, false},
		// The admin's swap of the copied remote rows stays, and the restored
		// NFO row goes behind them, not ahead on its installation ID.
		{18, "tmdb", 0, true}, {18, "tvdb", 1, true}, {18, "nfo", 2, false},
		{18, "tvdb", 0, true}, {18, "tmdb", 1, true}, {18, "nfo", 2, false},
		{19, "big", 0, true}, {19, "nfo", 1, false},
	}
	if got := chains(); !reflect.DeepEqual(got, want) {
		t.Fatalf("chains after Down =\n%+v\nwant\n%+v", got, want)
	}
}
