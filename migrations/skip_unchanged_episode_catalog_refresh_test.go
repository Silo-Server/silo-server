package migrations

import (
	"strings"
	"testing"
)

// Exercise the refresh before and after the migration in an isolated schema.
// Transaction-local statistics count refreshes and entry writes, so the test
// sees work done inside triggers.
func TestSkipUnchangedEpisodeCatalogRefreshMigrationPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	for _, table := range []string{"media_items", "episodes", "episode_libraries", "media_files", "episode_catalog_entries"} {
		migrationExec(t, tx, "CREATE TABLE "+schema+"."+table+" (LIKE public."+table+" INCLUDING ALL)")
	}
	migrationExec(t, tx, "CREATE FUNCTION "+schema+".normalize_search_text(text) RETURNS text LANGUAGE plpgsql IMMUTABLE AS 'BEGIN RETURN public.normalize_search_text($1); END'")
	copyFunctions := func(signatures ...string) {
		t.Helper()
		for _, signature := range signatures {
			var definition string
			if err := tx.QueryRow(t.Context(), "SELECT pg_get_functiondef($1::regprocedure)", "public."+signature).Scan(&definition); err != nil {
				t.Fatal(err)
			}
			migrationExec(t, tx, strings.ReplaceAll(definition, "public.", schema+"."))
		}
	}
	const predecessor = "20260930130212_avoid_duplicate_episode_catalog_refresh"
	const migration = "20261008034854_skip_unchanged_episode_catalog_refresh"
	copyFunctions("episode_catalog_normalized_resolution(text)", "episode_catalog_resolution_rank(text)")
	migrationExec(t, tx, adminMigrationSQL(t, predecessor, schema, false))
	// SQL-language wrappers check their bodies, so they follow the refresh.
	copyFunctions("refresh_episode_catalog_entries_for_episode(text)", "refresh_episode_catalog_entries_for_series(text)")
	migrationExec(t, tx, `
CREATE TRIGGER search_fields BEFORE INSERT OR UPDATE ON episode_catalog_entries
FOR EACH ROW EXECUTE FUNCTION set_episode_catalog_entry_search_fields();
CREATE TRIGGER file_refresh AFTER INSERT OR UPDATE OR DELETE ON media_files
FOR EACH ROW EXECUTE FUNCTION episode_catalog_entries_media_files_trigger();
CREATE TRIGGER series_refresh AFTER UPDATE ON media_items
FOR EACH ROW EXECUTE FUNCTION episode_catalog_entries_series_trigger();
SET LOCAL track_functions='all';
-- A refresh that never leaves its retry loop fails instead of hanging.
SET LOCAL statement_timeout='30s';
INSERT INTO media_items(content_id,type,title,year) VALUES('series','series','Fixture Series',2020);
INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,overview,runtime)
VALUES('episode','series',1,1,'Pilot','A buried signal returns.',42);
INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES('episode',1);
INSERT INTO media_files(content_id,episode_id,media_folder_id,file_path,resolution)
VALUES('series','episode',1,'fixture.mkv','720p');`)
	refreshCalls := func() int64 {
		t.Helper()
		var calls int64
		if err := tx.QueryRow(t.Context(), `SELECT COALESCE(SUM(calls),0)::bigint
FROM pg_stat_xact_user_functions WHERE funcid=$1::regprocedure`, schema+".refresh_episode_catalog_entry(text,integer)").Scan(&calls); err != nil {
			t.Fatal(err)
		}
		return calls
	}
	entryWrites := func() int64 {
		t.Helper()
		var writes int64
		if err := tx.QueryRow(t.Context(), `SELECT n_tup_ins + n_tup_upd FROM pg_stat_xact_user_tables
WHERE relid=$1::regclass`, schema+".episode_catalog_entries").Scan(&writes); err != nil {
			t.Fatal(err)
		}
		return writes
	}
	work := func(stage, name, sql string, wantRefreshes, wantWrites int64) {
		t.Helper()
		refreshes, writes := refreshCalls(), entryWrites()
		migrationExec(t, tx, sql)
		if got := refreshCalls() - refreshes; got != wantRefreshes {
			t.Fatalf("%s: %s refreshed %d entries, want %d", stage, name, got, wantRefreshes)
		}
		if got := entryWrites() - writes; got != wantWrites {
			t.Fatalf("%s: %s wrote %d entry rows, want %d", stage, name, got, wantWrites)
		}
	}
	check := func(stage string, skips bool) {
		t.Helper()
		// Entries store no still columns, so a still edit needs no refresh.
		stillRefreshes, unchangedWrites := int64(1), int64(1)
		if skips {
			stillRefreshes, unchangedWrites = 0, 0
		}
		work(stage, "still edit", "UPDATE episodes SET still_path='/still/'||md5(random()::text)||'/original.jpg',still_thumbhash=md5(random()::text) WHERE content_id='episode'", stillRefreshes, stillRefreshes)
		work(stage, "unchanged refresh", "SELECT refresh_episode_catalog_entry('episode',1)", 1, unchangedWrites)
		// A series runtime does not reach an episode that has its own.
		work(stage, "masked series runtime", "UPDATE media_items SET runtime=COALESCE(runtime,0)+1 WHERE content_id='series'", 1, unchangedWrites)
		work(stage, "file edit", "UPDATE media_files SET resolution=CASE WHEN resolution='720p' THEN '1080p' ELSE '720p' END", 1, 1)
		work(stage, "series facet edit", "UPDATE media_items SET genres=CASE WHEN genres='{Drama}' THEN '{Comedy}' ELSE '{Drama}' END::text[] WHERE content_id='series'", 1, 1)
		work(stage, "missing entry", "DELETE FROM episode_catalog_entries; SELECT refresh_episode_catalog_entry('episode',1)", 1, 1)
		// With every value current, a refresh still repairs a missing document.
		migrationExec(t, tx, "ALTER TABLE episode_catalog_entries DISABLE TRIGGER search_fields; UPDATE episode_catalog_entries SET search_overview_vector=NULL; ALTER TABLE episode_catalog_entries ENABLE TRIGGER search_fields")
		work(stage, "missing document", "SELECT refresh_episode_catalog_entry('episode',1)", 1, 1)
		var matches bool
		if err := tx.QueryRow(t.Context(), `SELECT c.title=e.title AND c.runtime=e.runtime AND c.genres=s.genres
AND c.resolution_codes=ARRAY[public.episode_catalog_normalized_resolution(f.resolution)]
AND c.search_title_normalized=normalize_search_text(e.title)
AND c.search_overview_vector=to_tsvector('english',e.overview)
FROM episode_catalog_entries c JOIN episodes e ON e.content_id=c.episode_id
JOIN media_items s ON s.content_id=e.series_id JOIN media_files f ON f.episode_id=e.content_id`).Scan(&matches); err != nil || !matches {
			t.Fatalf("%s: entry differs from its sources: match=%v err=%v", stage, matches, err)
		}
	}
	check("predecessor", false)
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	check("up", true)
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	check("down", false)
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	check("reapply", true)
}
