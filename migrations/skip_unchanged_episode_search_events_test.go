package migrations

import (
	"slices"
	"testing"
)

// Run the episode search capture triggers before and after the migration in an
// isolated schema, listing the outbox rows each episode write queues.
func TestSkipUnchangedEpisodeSearchEventsMigrationPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	for _, table := range []string{
		"server_settings", "catalog_search_index_events", "media_items", "episodes",
		"episode_libraries", "media_item_libraries",
	} {
		migrationExec(t, tx, "CREATE TABLE "+schema+"."+table+" (LIKE public."+table+" INCLUDING ALL)")
	}
	const migration = "20261123125535_skip_unchanged_episode_search_events"
	migrationExec(t, tx, adminMigrationSQL(t, "20260713233100_episode_search_outbox_triggers", schema, false))
	migrationExec(t, tx, `
INSERT INTO server_settings(key,value) VALUES('catalog.search.provider','meilisearch');
INSERT INTO media_items(content_id,type,title,year) VALUES('series','series','Fixture Series',2020);
INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,overview,tvdb_id) VALUES
	('e1','series',1,1,'Pilot','A buried signal returns.','101'),
	('e2','series',1,2,'Second','The signal answers.','102');`)

	// The metadata bulk write's shape: an upsert that sets every column from
	// the incoming row and stamps updated_at. NOW() would repeat the insert's
	// timestamp inside this one transaction, so the stamp uses the wall clock.
	firstID, firstTitle := "e1", "Pilot"
	upsert := func(title string) string {
		return `
INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,overview,tvdb_id) VALUES
	('` + firstID + `','series',1,1,'` + title + `','A buried signal returns.','101'),
	('e2','series',1,2,'Second','The signal answers.','102')
ON CONFLICT (series_id,season_number,episode_number) DO UPDATE SET
	title = EXCLUDED.title, overview = EXCLUDED.overview,
	tvdb_id = COALESCE(NULLIF(EXCLUDED.tvdb_id,''), episodes.tvdb_id), updated_at = clock_timestamp()`
	}
	queued := func(stage, name, sql string, want ...string) {
		t.Helper()
		var last int64
		if err := tx.QueryRow(t.Context(), "SELECT COALESCE(MAX(id),0) FROM catalog_search_index_events").Scan(&last); err != nil {
			t.Fatal(err)
		}
		migrationExec(t, tx, sql)
		var got []string
		if err := tx.QueryRow(t.Context(), `SELECT COALESCE(array_agg(action || ':' || content_id
ORDER BY action, content_id), '{}') FROM catalog_search_index_events WHERE id > $1`, last).Scan(&got); err != nil {
			t.Fatal(err)
		}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: %s queued %v, want %v", stage, name, got, want)
		}
	}
	check := func(stage string, skips bool) {
		t.Helper()
		both := []string{"upsert:" + firstID, "upsert:e2"}
		var unchanged []string
		if !skips {
			unchanged = both
		}
		// The stale-ID clear writes an episode's own ID back unchanged.
		queued(stage, "unchanged update", "UPDATE episodes SET tvdb_id = tvdb_id WHERE series_id = 'series'", unchanged...)
		queued(stage, "unchanged upsert", upsert(firstTitle), unchanged...)
		changed := both
		if skips {
			changed = both[:1]
		}
		firstTitle = stage + " Pilot"
		queued(stage, "upsert changing one episode", upsert(firstTitle), changed...)
		// A changed content ID removes the old document and adds the new one.
		renamed := "e1-" + stage
		queued(stage, "content ID change", "UPDATE episodes SET content_id = '"+renamed+"' WHERE content_id = '"+firstID+"'",
			"delete:"+firstID, "upsert:"+renamed)
		firstID = renamed
	}
	check("before", false)
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, false))
	// A static trigger statement keeps the plan from its first call on the
	// connection. After a one-row first call, that plan compares every row
	// of a large rewrite with every other one, which takes most of a minute
	// here; planned per call, it takes well under a second.
	queued("after", "unchanged one-row update", "UPDATE episodes SET updated_at = clock_timestamp() WHERE content_id = 'e2'")
	migrationExec(t, tx, `
INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,overview)
SELECT 'bulk-' || g, 'bulk-series', 1, g, 'Episode ' || g, repeat('An overview of typical length. ', 20) FROM generate_series(1, 8000) g`)
	migrationExec(t, tx, "SET LOCAL statement_timeout = '10s'")
	queued("after", "unchanged rewrite of a large series",
		"UPDATE episodes SET updated_at = clock_timestamp() WHERE series_id = 'bulk-series'")
	migrationExec(t, tx, "SET LOCAL statement_timeout = 0")
	check("after", true)
	migrationExec(t, tx, adminMigrationSQL(t, migration, schema, true))
	check("rolled back", false)
}
