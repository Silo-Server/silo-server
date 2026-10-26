package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestMarkerContributionClaimsByTargetMigrationPostgres replays
// 20260925144545 over legacy claim rows laid out the way a live install had
// them. The migration moves a claim between rows that share a payload while
// the payload index still exists, so it has to release the losing claim
// before it grants the winning one. The index statements run without
// CONCURRENTLY so the whole migration fits the fixture's transaction.
func TestMarkerContributionClaimsByTargetMigrationPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE media_items (content_id text PRIMARY KEY, type text NOT NULL, tmdb_id text, imdb_id text, tvdb_id text);
CREATE TABLE episodes (content_id text PRIMARY KEY, series_id text NOT NULL REFERENCES media_items(content_id), season_number integer, episode_number integer);
CREATE TABLE media_files (id integer PRIMARY KEY, content_id text, episode_id text REFERENCES episodes(content_id));
INSERT INTO media_items VALUES ('series-a', 'series', '37866', '', ''), ('series-b', 'series', '99999', '', '');
INSERT INTO episodes VALUES ('series-a-1-11', 'series-a', 1, 11), ('series-a-1-5', 'series-a', 1, 5), ('series-b-2-3', 'series-b', 2, 3);
INSERT INTO media_files VALUES (1, 'series-a', 'series-a-1-11'), (2, 'series-a', 'series-a-1-11'), (3, 'series-a', 'series-a-1-5'), (4, 'series-b', 'series-b-2-3'), (5, 'series-a', 'series-a-1-11');`)
	migrationExec(t, tx, adminMigrationSQL(t, "182_marker_contributions", schema, false))
	migrationExec(t, tx, withoutConcurrently(adminMigrationSQL(t, "20260807140637_settle_marker_contribution_conflicts", schema, false)))

	// Rows 1 and 2: two files of one episode submitted the same intro. The
	// provider refused the second as a conflict and that row holds the
	// payload claim; the pending row it duplicates was inserted first, so the
	// re-ranking reaches it first. Rows 3 and 4: the claim holder's file was
	// rematched to another series since, so its payload no longer recomputes
	// and it keeps a NULL key. Row 5: an older pending submission of the
	// first episode with different times, which the newer pending row
	// outranks.
	episode11 := markerContributionPayloadHash("intro", 10010, 90007, 1352000, "37866", 1, 11)
	episode11Older := markerContributionPayloadHash("intro", 9990, 89990, 1352000, "37866", 1, 11)
	episode5 := markerContributionPayloadHash("intro", 0, 79996, 1357000, "37866", 1, 5)
	migrationExec(t, tx, fmt.Sprintf(`
INSERT INTO marker_contributions (id, media_file_id, provider, segment_kind, source, submitted_start_ms, submitted_end_ms, video_duration_ms, content_hash, submission_id, status, http_status, submitted_at, updated_at, claim_active) VALUES
 ('00000000-0000-0000-0000-000000000001', 1, 'plugin:6:introdb', 'intro', 'scanner', 10010, 90007, 1352000, '%[1]s', 'submission-1', 'pending', NULL, '2026-07-09 04:15:28+00', '2026-07-09 04:15:28+00', false),
 ('00000000-0000-0000-0000-000000000002', 2, 'plugin:6:introdb', 'intro', 'scanner', 10010, 90007, 1352000, '%[1]s', NULL, 'conflict', 409, '2026-07-09 04:15:29+00', '2026-08-07 15:30:26+00', true),
 ('00000000-0000-0000-0000-000000000003', 3, 'plugin:6:introdb', 'intro', 'scanner', 0, 79996, 1357000, '%[2]s', 'submission-3', 'pending', NULL, '2026-07-09 04:15:20+00', '2026-07-09 04:15:20+00', false),
 ('00000000-0000-0000-0000-000000000004', 4, 'plugin:6:introdb', 'intro', 'scanner', 0, 79996, 1357000, '%[2]s', 'submission-4', 'pending', NULL, '2026-07-09 04:15:22+00', '2026-07-09 04:15:22+00', true),
 ('00000000-0000-0000-0000-000000000005', 5, 'plugin:6:introdb', 'intro', 'scanner', 9990, 89990, 1352000, '%[3]s', 'submission-5', 'pending', NULL, '2026-06-01 00:00:00+00', '2026-06-01 00:00:00+00', true)`,
		episode11, episode5, episode11Older))

	const migration = "20260925144545_key_marker_contribution_claims_by_target"
	wantClaims := []string{
		"00000000-0000-0000-0000-000000000001 true",
		"00000000-0000-0000-0000-000000000002 false",
		"00000000-0000-0000-0000-000000000003 true",
		"00000000-0000-0000-0000-000000000004 false",
		"00000000-0000-0000-0000-000000000005 false",
	}
	wantKeys := []string{
		"00000000-0000-0000-0000-000000000001 episode|tmdb:37866|1|11",
		"00000000-0000-0000-0000-000000000002 episode|tmdb:37866|1|11",
		"00000000-0000-0000-0000-000000000003 episode|tmdb:37866|1|5",
		"00000000-0000-0000-0000-000000000004 NULL",
		"00000000-0000-0000-0000-000000000005 episode|tmdb:37866|1|11",
	}
	// Goose re-runs a failed NO TRANSACTION migration from the top, so the
	// second pass sees the first pass's rows.
	for _, pass := range []string{"migration", "retry"} {
		migrationExec(t, tx, withoutConcurrently(adminMigrationSQL(t, migration, schema, false)))
		if got := markerContributionRows(t, tx, "claim_active::text"); !slices.Equal(got, wantClaims) {
			t.Fatalf("claims after %s:\n%s\nwant:\n%s", pass, strings.Join(got, "\n"), strings.Join(wantClaims, "\n"))
		}
		if got := markerContributionRows(t, tx, "COALESCE(target_key, 'NULL')"); !slices.Equal(got, wantKeys) {
			t.Fatalf("target keys after %s:\n%s\nwant:\n%s", pass, strings.Join(got, "\n"), strings.Join(wantKeys, "\n"))
		}
		indexes := markerContributionIndexes(t, tx, schema)
		if !slices.Contains(indexes, "marker_contributions_provider_target_active_uidx") || slices.Contains(indexes, "marker_contributions_provider_hash_active_uidx") {
			t.Fatalf("indexes after %s: %v", pass, indexes)
		}
	}

	migrationExec(t, tx, withoutConcurrently(adminMigrationSQL(t, migration, schema, true)))
	if got := markerContributionRows(t, tx, "claim_active::text"); !slices.Equal(got, wantClaims) {
		t.Fatalf("claims after rollback:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantClaims, "\n"))
	}
	indexes := markerContributionIndexes(t, tx, schema)
	if slices.Contains(indexes, "marker_contributions_provider_target_active_uidx") || !slices.Contains(indexes, "marker_contributions_provider_hash_active_uidx") {
		t.Fatalf("indexes after rollback: %v", indexes)
	}
	var keyed bool
	if err := tx.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'marker_contributions' AND column_name = 'target_key')", schema).Scan(&keyed); err != nil {
		t.Fatal(err)
	}
	if keyed {
		t.Fatal("rollback kept the target_key column")
	}
}

func withoutConcurrently(sql string) string {
	return strings.ReplaceAll(sql, " CONCURRENTLY", "")
}

// markerContributionPayloadHash recomputes an episode contribution's
// content_hash the way the migration's backfill does.
func markerContributionPayloadHash(segmentKind string, startMs, endMs, durationMs int64, tmdbID string, season, episode int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		segmentKind,
		strconv.FormatInt(startMs, 10),
		strconv.FormatInt(endMs, 10),
		strconv.FormatInt(durationMs, 10),
		"episode", tmdbID, "", "",
		strconv.Itoa(season),
		strconv.Itoa(episode),
	}, "|")))
	return hex.EncodeToString(sum[:])[:32]
}

// markerContributionRows lists every contribution as its id followed by the
// given column expression, in id order.
func markerContributionRows(t *testing.T, tx pgx.Tx, column string) []string {
	t.Helper()
	rows, err := tx.Query(t.Context(), "SELECT id::text || ' ' || "+column+" FROM marker_contributions ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	values, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func markerContributionIndexes(t *testing.T, tx pgx.Tx, schema string) []string {
	t.Helper()
	rows, err := tx.Query(t.Context(), "SELECT indexname FROM pg_indexes WHERE schemaname = $1 AND tablename = 'marker_contributions' ORDER BY indexname", schema)
	if err != nil {
		t.Fatal(err)
	}
	indexes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return indexes
}
