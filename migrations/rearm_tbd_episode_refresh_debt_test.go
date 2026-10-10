package migrations

import "testing"

func TestRearmTBDEpisodeRefreshDebtPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE episodes (
	content_id TEXT PRIMARY KEY, season_id TEXT, series_id TEXT NOT NULL,
	title TEXT, metadata_source TEXT, tmdb_id TEXT, tvdb_id TEXT, imdb_id TEXT, air_date DATE
);
CREATE TABLE metadata_refresh_debt (
	target_type TEXT NOT NULL, content_id TEXT NOT NULL, reason_mask BIGINT NOT NULL DEFAULT 0,
	next_refresh_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	PRIMARY KEY (target_type, content_id)
);
INSERT INTO episodes VALUES
	('old-tbd', 'season-a', 'series-a', 'TBD', 'provider', '1', '', '', CURRENT_DATE - 400),
	('recent-tba', 'season-b', 'series-b', 'tba', 'provider', '2', '', '', CURRENT_DATE - 10),
	('recent-soon', 'season-b', 'series-b', 'TBA', 'provider', '3', '', '', CURRENT_DATE - 10),
	('old-fallback', 'season-c', 'series-c', 'TBD', 'scanner_fallback', '4', '', '', CURRENT_DATE - 400),
	('undated', 'season-d', 'series-d', 'TBD', 'provider', '5', '', '', NULL);
INSERT INTO metadata_refresh_debt (target_type, content_id, reason_mask, next_refresh_at) VALUES
	('episode', 'old-tbd', 1, NOW() + INTERVAL '90 days'),
	('season', 'season-a', 1, NOW() + INTERVAL '90 days'),
	('item', 'series-a', 1, NOW() + INTERVAL '90 days'),
	('episode', 'recent-tba', 1, NOW() + INTERVAL '90 days'),
	('episode', 'recent-soon', 1, NOW() + INTERVAL '1 day'),
	('season', 'season-b', 1, NOW() + INTERVAL '90 days'),
	('item', 'series-b', 2, NOW() + INTERVAL '90 days'),
	('episode', 'old-fallback', 1, NOW() + INTERVAL '90 days'),
	('episode', 'undated', 1, NOW() + INTERVAL '90 days');`)

	migrationExec(t, tx, adminMigrationSQL(t, "20260928120000_rearm_tbd_episode_refresh_debt", schema, false))

	want := map[string]string{
		// Old real TBD rows wait an hour so a previous release cannot re-park them.
		"episode/old-tbd": "NOW() + INTERVAL '1 hour'",
		"season/season-a": "NOW() + INTERVAL '1 hour'",
		"item/series-a":   "NOW() + INTERVAL '1 hour'",
		// Recent rows come due when the 45-day window closes.
		"episode/recent-tba": "(CURRENT_DATE - 10) + INTERVAL '45 days 1 minute'",
		"season/season-b":    "(CURRENT_DATE - 10) + INTERVAL '45 days 1 minute'",
		// Rows already due sooner, without episode-incomplete debt, or for
		// episodes the corrected check still flags are left alone.
		"episode/recent-soon":  "NOW() + INTERVAL '1 day'",
		"item/series-b":        "NOW() + INTERVAL '90 days'",
		"episode/old-fallback": "NOW() + INTERVAL '90 days'",
		"episode/undated":      "NOW() + INTERVAL '90 days'",
	}
	for key, expr := range want {
		var matches bool
		if err := tx.QueryRow(t.Context(), `SELECT next_refresh_at = (`+expr+`)::timestamptz
			FROM metadata_refresh_debt WHERE target_type || '/' || content_id = $1`, key).Scan(&matches); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if !matches {
			t.Errorf("%s: next_refresh_at is not %s", key, expr)
		}
	}
}
