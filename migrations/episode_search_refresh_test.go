package migrations

import (
	"strings"
	"testing"
)

// Exercise both versions with the actual catalog refresh functions in an
// isolated schema. Rollback leaves the deployment's triggers untouched.
func TestEpisodeSearchRefreshMigrationPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	for _, table := range []string{"media_items", "episodes", "episode_libraries", "media_files", "episode_catalog_entries"} {
		migrationExec(t, tx, "CREATE TABLE "+schema+"."+table+" (LIKE public."+table+" INCLUDING ALL)")
	}
	// PL/pgSQL keeps the wrapper from being inlined, so its call count shows
	// when a refresh rebuilds search documents.
	migrationExec(t, tx, "CREATE FUNCTION "+schema+".normalize_search_text(text) RETURNS text LANGUAGE plpgsql IMMUTABLE AS 'BEGIN RETURN public.normalize_search_text($1); END'")
	for _, signature := range []string{
		"episode_catalog_normalized_resolution(text)", "episode_catalog_resolution_rank(text)",
		"refresh_episode_catalog_entry(text,integer)", "refresh_episode_catalog_entries_for_episode(text)",
		"refresh_episode_catalog_entries_for_series(text)",
	} {
		var definition string
		if err := tx.QueryRow(t.Context(), "SELECT pg_get_functiondef($1::regprocedure)", "public."+signature).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		migrationExec(t, tx, strings.ReplaceAll(definition, "public.", schema+"."))
	}
	const migration = "20260930130212_avoid_duplicate_episode_catalog_refresh"
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	migrationExec(t, tx, `
CREATE TRIGGER search_fields BEFORE INSERT OR UPDATE ON episode_catalog_entries
FOR EACH ROW EXECUTE FUNCTION set_episode_catalog_entry_search_fields();
CREATE TRIGGER file_refresh AFTER INSERT OR UPDATE OR DELETE ON media_files
FOR EACH ROW EXECUTE FUNCTION episode_catalog_entries_media_files_trigger();
CREATE TRIGGER series_refresh AFTER UPDATE ON media_items
FOR EACH ROW EXECUTE FUNCTION episode_catalog_entries_series_trigger();
SET LOCAL track_functions='all';
INSERT INTO media_items(content_id,type,title,year) VALUES('series','series','Fixture Series',2020);
INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,overview)
VALUES('episode','series',1,1,'Pilot','A buried signal returns.');
INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES('episode',1);
INSERT INTO media_files(content_id,episode_id,media_folder_id,file_path,resolution)
VALUES('series','episode',1,'fixture.mkv','720p');`)
	functionCalls := func(signature string) int64 {
		t.Helper()
		var calls int64
		if err := tx.QueryRow(t.Context(), `SELECT COALESCE(SUM(calls),0)::bigint
FROM pg_stat_xact_user_functions WHERE funcid=$1::regprocedure`, schema+"."+signature).Scan(&calls); err != nil {
			t.Fatal(err)
		}
		return calls
	}
	refreshCalls := func() int64 { return functionCalls("refresh_episode_catalog_entry(text,integer)") }
	documentBuilds := func() int64 { return functionCalls("normalize_search_text(text)") }
	check := func(stage string, doubleRefresh bool) {
		t.Helper()
		before := refreshCalls()
		migrationExec(t, tx, "UPDATE episodes SET title=title || ' revised',overview=overview || ' revised' WHERE content_id='episode'")
		want := int64(1)
		if doubleRefresh {
			want = 2
		}
		if got := refreshCalls() - before; got != want {
			t.Fatalf("%s combined metadata edit refreshed %d entries, want %d", stage, got, want)
		}
		// The predecessor upserts every refresh, so its BEFORE INSERT trigger
		// rebuilds both documents before ON CONFLICT discards them. Existing
		// entries must now keep their documents through file and facet edits.
		before = refreshCalls()
		builds := documentBuilds()
		migrationExec(t, tx, "UPDATE media_files SET resolution=CASE WHEN resolution='720p' THEN '1080p' ELSE '720p' END")
		if got := refreshCalls() - before; got != want {
			t.Fatalf("%s file edit refreshed %d entries, want %d", stage, got, want)
		}
		if got := documentBuilds() - builds; (got > 0) != doubleRefresh {
			t.Fatalf("%s file edit built search documents %d times", stage, got)
		}
		builds = documentBuilds()
		migrationExec(t, tx, "UPDATE media_items SET genres=CASE WHEN genres='{Drama}' THEN '{Comedy}' ELSE '{Drama}' END::text[] WHERE content_id='series'")
		if got := documentBuilds() - builds; (got > 0) != doubleRefresh {
			t.Fatalf("%s series facet edit built search documents %d times", stage, got)
		}
		var matches bool
		if err := tx.QueryRow(t.Context(), `SELECT search_title_normalized=normalize_search_text(e.title)
AND search_title_vector=setweight(to_tsvector('simple',normalize_search_text(e.title)),'A')
AND search_overview_vector=to_tsvector('english',e.overview)
FROM episode_catalog_entries c JOIN episodes e ON e.content_id=c.episode_id`).Scan(&matches); err != nil || !matches {
			t.Fatalf("%s documents differ: match=%v err=%v", stage, matches, err)
		}
	}
	check("predecessor", true)
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	check("up", false)
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	check("down", true)
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	check("reapply", false)
}
