package migrations

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

const (
	retireTraktMigration          = "20261016131356_retire_trakt"
	retireTraktWatchSyncMigration = "20261016134645_retire_trakt_watch_sync"
)

// The two migrations convert every Trakt-backed collection and home row,
// delete the Trakt connections, credentials and snapshots, disable the Trakt
// plugin, and leave everything else alone.
func TestRetireTraktMigrationPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE library_collections (
    id text PRIMARY KEY,
    collection_type text NOT NULL,
    source_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    source_url text NOT NULL DEFAULT '',
    sync_schedule text,
    next_sync_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT library_collections_collection_type_check
        CHECK (collection_type = ANY (ARRAY['manual', 'smart', 'mdblist', 'tmdb', 'trakt']))
);
CREATE TABLE user_personal_collections (
    id text PRIMARY KEY,
    collection_type text NOT NULL,
    source_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    source_url text NOT NULL DEFAULT '',
    sync_schedule text,
    next_sync_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_personal_collections_type_check
        CHECK (collection_type IN ('manual', 'smart', 'mdblist', 'tmdb', 'trakt', 'synced', 'playlist'))
);
CREATE TABLE page_sections (
    id text PRIMARY KEY,
    section_type text NOT NULL,
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE user_settings (user_id integer NOT NULL, key text NOT NULL, value text NOT NULL);
CREATE TABLE trending_discover_snapshots (source text NOT NULL, time_window text NOT NULL);
CREATE TABLE watch_provider_connections (id uuid PRIMARY KEY, provider text NOT NULL);
CREATE TABLE watch_provider_sync_runs (
    connection_id uuid NOT NULL REFERENCES watch_provider_connections(id) ON DELETE CASCADE
);
CREATE TABLE watch_provider_auth_sessions (provider text NOT NULL);
CREATE TABLE server_settings (key text PRIMARY KEY, value text NOT NULL);
CREATE TABLE plugin_installations (
    plugin_id text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO library_collections (id, collection_type, source_config, source_url, sync_schedule, next_sync_at) VALUES
    ('trakt-preset', 'trakt', '{"mode":"trakt_preset","provider":"trakt","preset":"trending"}', 'trakt://trending/movie', 'daily', now()),
    ('trakt-list', 'trakt', '{"mode":"trakt_list","provider":"trakt","url":"https://trakt.tv/users/u/lists/l"}', 'https://trakt.tv/users/u/lists/l', 'weekly', now()),
    ('tmdb', 'tmdb', '{"mode":"tmdb_preset","preset":"popular"}', 'tmdb://popular/movie', 'daily', now());
INSERT INTO user_personal_collections (id, collection_type, source_config, source_url, sync_schedule, next_sync_at) VALUES
    ('mine-trakt', 'trakt', '{"mode":"trakt_preset","provider":"trakt"}', 'trakt://popular/tv', 'daily', now()),
    ('mine-mdblist', 'mdblist', '{"mode":"mdblist_json"}', 'https://mdblist.com/lists/u/l', 'daily', now()),
    ('abs-playlist', 'playlist', '{}', '', NULL, NULL);
INSERT INTO page_sections (id, section_type, config, enabled) VALUES
    ('trakt-trending', 'trending_discover', '{"source":"trakt","window":"week"}', true),
    ('tmdb-trending', 'trending_discover', '{"source":"tmdb","window":"day"}', true),
    ('trakt-collection', 'collection', '{"library_collection_id":"trakt-preset","source_provider":"trakt"}', true);
INSERT INTO user_settings (user_id, key, value) VALUES
    (1, 'section_overrides:p1:home', '[{"ID":"own","SectionID":"","Hidden":false,"UserSectionType":"trending_discover","UserConfig":"{\"source\":\"trakt\",\"window\":\"day\"}","Config":""},{"ID":"linked","SectionID":"trakt-collection","Hidden":false,"Config":"{\"library_collection_id\":\"trakt-preset\",\"source_provider\":\"trakt\"}","UserConfig":""},{"ID":"keep","SectionID":"","Hidden":false,"UserSectionType":"trending_discover","UserConfig":"{\"source\":\"tmdb\",\"window\":\"day\"}","Config":""}]'),
    (1, 'section_overrides:p2:home', 'not json about trakt'),
    (1, 'ui.note', 'trakt');
INSERT INTO trending_discover_snapshots VALUES ('trakt', 'week'), ('tmdb', 'day');
INSERT INTO watch_provider_connections VALUES
    ('00000000-0000-4000-8000-000000000001', 'trakt'),
    ('00000000-0000-4000-8000-000000000002', 'simkl');
INSERT INTO watch_provider_sync_runs VALUES
    ('00000000-0000-4000-8000-000000000001'),
    ('00000000-0000-4000-8000-000000000002');
INSERT INTO watch_provider_auth_sessions VALUES ('trakt'), ('simkl');
INSERT INTO server_settings VALUES
    ('watchsync.trakt.client_id', 'id'),
    ('watchsync.trakt.client_secret', 'secret'),
    ('watchsync.plugin_migration.trakt', 'plugin'),
    ('watchsync.plugin_app_seeded.trakt', 'true'),
    ('watchsync.simkl.client_id', 'id'),
    ('watchsync.plugin_migration.simkl', 'plugin');
INSERT INTO plugin_installations (plugin_id) VALUES ('silo.watchprovider.trakt'), ('silo.watchprovider.simkl');`)

	values := func(query string) []string {
		t.Helper()
		rows, err := tx.Query(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			got = append(got, value)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	check := func(label, query string, want ...string) {
		t.Helper()
		if got := values(query); !slices.Equal(got, want) {
			t.Fatalf("%s = %q, want %q", label, got, want)
		}
	}
	verify := func() {
		t.Helper()
		check("library collections",
			`SELECT id || ' ' || collection_type || ' ' || source_config::text || ' ' || source_url || ' ' || (sync_schedule IS NULL)::text || ' ' || (next_sync_at IS NULL)::text FROM library_collections ORDER BY id`,
			`tmdb tmdb {"mode": "tmdb_preset", "preset": "popular"} tmdb://popular/movie false false`,
			`trakt-list manual {}  true true`,
			`trakt-preset manual {}  true true`)
		check("personal collections",
			`SELECT id || ' ' || collection_type || ' ' || source_config::text || ' ' || (sync_schedule IS NULL)::text FROM user_personal_collections ORDER BY id`,
			`abs-playlist playlist {} true`,
			`mine-mdblist mdblist {"mode": "mdblist_json"} false`,
			`mine-trakt manual {} true`)
		check("page sections",
			`SELECT id || ' ' || config::text || ' ' || enabled::text FROM page_sections ORDER BY id`,
			`tmdb-trending {"source": "tmdb", "window": "day"} true`,
			`trakt-collection {"library_collection_id": "trakt-preset"} true`,
			`trakt-trending {"source": "tmdb", "window": "week"} false`)
		check("unparsed and unrelated settings",
			`SELECT value FROM user_settings WHERE key <> 'section_overrides:p1:home' ORDER BY key`,
			"not json about trakt", "trakt")
		check("snapshots", `SELECT source || '|' || time_window FROM trending_discover_snapshots`, "tmdb|day")
		check("connections", `SELECT provider FROM watch_provider_connections`, "simkl")
		check("sync runs", `SELECT connection_id::text FROM watch_provider_sync_runs`, "00000000-0000-4000-8000-000000000002")
		check("auth sessions", `SELECT provider FROM watch_provider_auth_sessions`, "simkl")
		check("server settings", `SELECT key FROM server_settings ORDER BY key`, "watchsync.plugin_migration.simkl", "watchsync.simkl.client_id")
		check("plugins", `SELECT plugin_id || ' ' || enabled::text FROM plugin_installations ORDER BY plugin_id`,
			"silo.watchprovider.simkl true", "silo.watchprovider.trakt false")

		var stored string
		if err := tx.QueryRow(t.Context(), `SELECT value FROM user_settings WHERE key = 'section_overrides:p1:home'`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		var overrides []map[string]any
		if err := json.Unmarshal([]byte(stored), &overrides); err != nil {
			t.Fatalf("overrides %q: %v", stored, err)
		}
		config := func(raw any) map[string]any {
			t.Helper()
			var v map[string]any
			if err := json.Unmarshal([]byte(raw.(string)), &v); err != nil {
				t.Fatalf("config %v: %v", raw, err)
			}
			return v
		}
		if len(overrides) != 3 {
			t.Fatalf("overrides = %v", overrides)
		}
		if own := overrides[0]; own["ID"] != "own" || own["Hidden"] != true || !reflect.DeepEqual(config(own["UserConfig"]), map[string]any{"source": "tmdb", "window": "week"}) {
			t.Fatalf("own trending override = %v, want hidden TMDB weekly", own)
		}
		if linked := overrides[1]; linked["ID"] != "linked" || linked["Hidden"] != false || !reflect.DeepEqual(config(linked["Config"]), map[string]any{"library_collection_id": "trakt-preset"}) {
			t.Fatalf("linked collection override = %v, want the Trakt marker dropped", linked)
		}
		if keep := overrides[2]; keep["ID"] != "keep" || keep["Hidden"] != false || keep["UserConfig"] != `{"source":"tmdb","window":"day"}` {
			t.Fatalf("TMDB override = %v, want unchanged", keep)
		}
	}

	migrate := func(down bool) {
		t.Helper()
		if down {
			migrationExec(t, tx, adminMigrationSQL(t, retireTraktWatchSyncMigration, schema, true))
			migrationExec(t, tx, adminMigrationSQL(t, retireTraktMigration, schema, true))
			return
		}
		migrationExec(t, tx, adminMigrationSQL(t, retireTraktMigration, schema, false))
		migrationExec(t, tx, adminMigrationSQL(t, retireTraktWatchSyncMigration, schema, false))
	}

	migrate(false)
	verify()
	requireMigrationSQLState(t, tx, `INSERT INTO library_collections (id, collection_type) VALUES ('new', 'trakt')`, "23514")
	requireMigrationSQLState(t, tx, `INSERT INTO user_personal_collections (id, collection_type) VALUES ('new', 'trakt')`, "23514")
	migrationExec(t, tx, `INSERT INTO user_personal_collections (id, collection_type) VALUES ('new-playlist', 'playlist'); DELETE FROM user_personal_collections WHERE id = 'new-playlist'`)

	// The rollback only widens the constraints again, and re-running the
	// migrations changes nothing.
	migrate(true)
	migrate(false)
	verify()
}
