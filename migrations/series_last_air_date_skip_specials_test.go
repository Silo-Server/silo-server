package migrations

import (
	"reflect"
	"testing"
)

// Issue #5393: last_air_date_at counted specials, so a special that aired
// after the finale moved a series' end date in lists past the date the item
// endpoint reports. The upgrade recomputes it from regular episodes: a series
// whose newest aired episode is a special falls back to its last regular one.
// A series with no aired regular episode keeps its newest special, and series
// without the problem, and future episodes, keep their values.
func TestSeriesLastAirDateSkipSpecialsMigrationPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE media_items (content_id text PRIMARY KEY, type text NOT NULL, last_air_date_at date);
CREATE TABLE episodes (content_id text PRIMARY KEY, series_id text NOT NULL, season_number integer NOT NULL, air_date date);
INSERT INTO media_items VALUES
    ('late-special', 'series', DATE '2020-02-16'),
    ('only-specials', 'series', DATE '2019-05-01'),
    ('regular', 'series', DATE '2012-02-01'),
    ('upcoming-special', 'series', DATE '2015-06-01'),
    ('undated', 'series', NULL),
    ('movie', 'movie', DATE '2001-01-01');
INSERT INTO episodes VALUES
    ('a1', 'late-special', 1, DATE '2008-01-20'), ('a2', 'late-special', 2, DATE '2013-09-29'),
    ('a0', 'late-special', 0, DATE '2020-02-16'),
    ('b0', 'only-specials', 0, DATE '2019-05-01'),
    ('c1', 'regular', 1, DATE '2012-02-01'), ('c0', 'regular', 0, DATE '2011-12-25'),
    ('d1', 'upcoming-special', 1, DATE '2015-06-01'), ('d0', 'upcoming-special', 0, CURRENT_DATE + 30),
    ('e1', 'undated', 1, NULL);`)

	migrationExec(t, tx, adminMigrationSQL(t, "20261126152958_series_last_air_date_skip_specials", schema, false))

	rows, err := tx.Query(t.Context(), `SELECT content_id, COALESCE(last_air_date_at::text, 'NULL') FROM media_items ORDER BY content_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, last string
		if err := rows.Scan(&id, &last); err != nil {
			t.Fatal(err)
		}
		got[id] = last
	}
	want := map[string]string{
		"late-special":     "2013-09-29",
		"only-specials":    "2019-05-01",
		"regular":          "2012-02-01",
		"upcoming-special": "2015-06-01",
		"undated":          "NULL",
		"movie":            "2001-01-01",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("last_air_date_at = %v, want %v", got, want)
	}
}
