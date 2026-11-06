package migrations

import (
	"reflect"
	"testing"
)

// Before this migration, local detection never restored markers that a
// rematch or a touched file cleared, and nothing records which files lost
// them. The upgrade stamps every file missing a range local detection places,
// an episode's intro or credits and a movie's credits, so the next scheduled
// run looks at them again. A movie with credits, an episode with both ranges,
// and extras stay settled.
func TestTrackMarkerInvalidationBackfillPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE media_files (id bigint PRIMARY KEY, episode_id text, extra_id text,
    intro_start double precision, credits_start double precision);
CREATE TABLE media_intro_fingerprints (media_file_id bigint PRIMARY KEY);
INSERT INTO media_files VALUES
    (1, 'e1', NULL, 20, 1300), (2, 'e2', NULL, NULL, 1300), (3, 'e3', NULL, 20, NULL),
    (4, NULL, NULL, NULL, 1300), (5, NULL, NULL, NULL, NULL), (6, NULL, 'x1', NULL, NULL);`)

	migrationExec(t, tx, adminMigrationSQL(t, "20261025203537_track_marker_invalidation", schema, false))

	rows, err := tx.Query(t.Context(), `SELECT id FROM media_files WHERE markers_invalidated_at IS NOT NULL ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stamped []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		stamped = append(stamped, id)
	}
	if want := []int64{2, 3, 5}; !reflect.DeepEqual(stamped, want) {
		t.Fatalf("stamped files = %v, want %v", stamped, want)
	}
}
