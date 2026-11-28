package migrations

import (
	"reflect"
	"testing"
)

// Profile 10 tracks scanned before #4487 store DOVI. The backfill gives each
// the range type a fresh scan stores for its base layer, and leaves every
// other track as it is (#5244).
func TestProfile10RangeTypeBackfillPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE media_files (id bigint PRIMARY KEY, video_tracks jsonb);
INSERT INTO media_files VALUES
    (1, '[{"dv_profile":10,"dv_bl_compat_id":1,"video_range_type":"DOVI"}]'),
    (2, '[{"dv_profile":10,"dv_bl_compat_id":1,"hdr10_plus":true,"video_range_type":"DOVI"}]'),
    (3, '[{"dv_profile":10,"dv_bl_compat_id":2,"video_range_type":"DOVI"}]'),
    (4, '[{"dv_profile":10,"dv_bl_compat_id":4,"video_range_type":"DOVI"}]'),
    (5, '[{"dv_profile":10,"video_range_type":"DOVI"}]'),
    (6, '[{"dv_profile":10,"dv_bl_compat_id":6,"video_range_type":"DOVI"}]'),
    (7, '[{"dv_profile":8,"dv_bl_compat_id":1,"video_range_type":"DOVI"}]'),
    (8, '[{"dv_profile":10,"dv_bl_compat_id":2,"video_range_type":"DOVI","codec":"av1"},{"codec":"mjpeg","video_range_type":"SDR"}]'),
    (9, NULL);`)

	migrationExec(t, tx, adminMigrationSQL(t, "20261127143000_profile10_range_type_backfill", schema, false))

	rows, err := tx.Query(t.Context(), `
SELECT id, COALESCE((SELECT jsonb_agg(track ->> 'video_range_type' ORDER BY position)
                     FROM jsonb_array_elements(video_tracks) WITH ORDINALITY AS t(track, position))::text, '')
FROM media_files ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int64]string{}
	for rows.Next() {
		var id int64
		var ranges string
		if err := rows.Scan(&id, &ranges); err != nil {
			t.Fatal(err)
		}
		got[id] = ranges
	}
	want := map[int64]string{
		1: `["DOVIWithHDR10"]`,
		2: `["DOVIWithHDR10Plus"]`,
		3: `["DOVIWithSDR"]`,
		4: `["DOVIWithHLG"]`,
		5: `["DOVI"]`,
		6: `["DOVI"]`,
		7: `["DOVI"]`,
		8: `["DOVIWithSDR", "SDR"]`,
		9: ``,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("range types = %v, want %v", got, want)
	}
}
