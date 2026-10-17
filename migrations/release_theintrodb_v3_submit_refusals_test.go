package migrations

import (
	"slices"
	"strings"
	"testing"
)

// TestReleaseTheIntroDBV3SubmitRefusalsPostgres checks that only TheIntroDB
// refusals recorded with HTTP 404, the answer of its retired v3 submit route,
// are released, and that other refusals and outcomes keep their claims.
func TestReleaseTheIntroDBV3SubmitRefusalsPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE media_items (content_id text PRIMARY KEY, type text NOT NULL, tmdb_id text, imdb_id text, tvdb_id text);
CREATE TABLE episodes (content_id text PRIMARY KEY, series_id text NOT NULL REFERENCES media_items(content_id), season_number integer, episode_number integer);
CREATE TABLE media_files (id integer PRIMARY KEY, content_id text, episode_id text REFERENCES episodes(content_id));
INSERT INTO media_items VALUES ('series-a', 'series', '1399', '', '');
INSERT INTO episodes VALUES ('series-a-1-1', 'series-a', 1, 1);
INSERT INTO media_files VALUES (1, 'series-a', 'series-a-1-1');`)
	migrationExec(t, tx, adminMigrationSQL(t, "182_marker_contributions", schema, false))
	migrationExec(t, tx, withoutConcurrently(adminMigrationSQL(t, "20260807140637_settle_marker_contribution_conflicts", schema, false)))
	migrationExec(t, tx, withoutConcurrently(adminMigrationSQL(t, "20260925144545_key_marker_contribution_claims_by_target", schema, false)))

	// One file, one claim per provider and segment: v3 route refusals from the
	// plugin and the legacy built-in provider, a real 400 refusal, another
	// provider's 404, and outcomes that are not refusals.
	migrationExec(t, tx, `
INSERT INTO marker_contributions (id, media_file_id, provider, segment_kind, source, content_hash, status, http_status, claim_active, target_key) VALUES
 ('00000000-0000-0000-0000-000000000001', 1, 'plugin:4:introdb', 'intro', 'scanner', 'hash-1', 'invalid', 404, true, 'episode|tmdb:1399|1|1'),
 ('00000000-0000-0000-0000-000000000002', 1, 'introdb', 'intro', 'scanner', 'hash-2', 'invalid', 404, true, 'episode|tmdb:1399|1|1'),
 ('00000000-0000-0000-0000-000000000003', 1, 'plugin:4:introdb', 'credits', 'scanner', 'hash-3', 'invalid', 400, true, 'episode|tmdb:1399|1|1'),
 ('00000000-0000-0000-0000-000000000004', 1, 'plugin:9:markers', 'intro', 'scanner', 'hash-4', 'invalid', 404, true, 'episode|tmdb:1399|1|1'),
 ('00000000-0000-0000-0000-000000000005', 1, 'plugin:4:introdb', 'recap', 'manual', 'hash-5', 'conflict', 409, true, 'episode|tmdb:1399|1|1'),
 ('00000000-0000-0000-0000-000000000006', 1, 'plugin:4:introdb', 'preview', 'manual', 'hash-6', 'pending', NULL, true, 'episode|tmdb:1399|1|1')`)

	migrationExec(t, tx, adminMigrationSQL(t, "20261017200720_release_theintrodb_v3_submit_refusals", schema, false))

	want := []string{
		"00000000-0000-0000-0000-000000000001 error false",
		"00000000-0000-0000-0000-000000000002 error false",
		"00000000-0000-0000-0000-000000000003 invalid true",
		"00000000-0000-0000-0000-000000000004 invalid true",
		"00000000-0000-0000-0000-000000000005 conflict true",
		"00000000-0000-0000-0000-000000000006 pending true",
	}
	if got := markerContributionRows(t, tx, "status || ' ' || claim_active::text"); !slices.Equal(got, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
