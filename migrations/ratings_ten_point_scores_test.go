package migrations

import "testing"

const ratingsTenPointScoresMigration = "20261116115433_ratings_ten_point_scores"

// Personal ratings and the values watch provider sync last agreed on move
// from whole stars to scores from 1 to 10: up doubles both and widens their
// checks, and down rounds back to stars, half up.
func TestRatingsTenPointScoresMigrationUpAndDownPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE user_ratings (LIKE public.user_ratings INCLUDING ALL);
CREATE TABLE watch_provider_rating_items (LIKE public.watch_provider_rating_items INCLUDING ALL);
CREATE TABLE watch_provider_connections (id int, sync_cursors jsonb NOT NULL DEFAULT '{}'::jsonb);`)
	// Down restores the whole-star tables that predate the migration.
	migrationExec(t, tx, adminMigrationSQL(t, ratingsTenPointScoresMigration, schema, true))
	migrationExec(t, tx, `
INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating, rated_at) VALUES
    (1, 'p1', 'movie:a', 1, '2026-01-01'), (1, 'p1', 'movie:b', 4, '2026-01-02'), (1, 'p1', 'movie:c', 5, '2026-01-03');
INSERT INTO watch_provider_rating_items (connection_id, media_item_id, kind, synced_rating) VALUES
    ('00000000-0000-0000-0000-000000000001', 'movie:b', 'movie', 4);
INSERT INTO watch_provider_connections (id, sync_cursors) VALUES
    (1, '{"plugin.remote.ratings": "r", "watchsync.ratings.import": "i", "plugin.remote.watched": "w"}'), (2, '{}');`)
	requireMigrationSQLState(t, tx, `UPDATE user_ratings SET rating = 6 WHERE media_item_id = 'movie:a'`, "23514")

	ratings := func() string {
		t.Helper()
		var got string
		if err := tx.QueryRow(t.Context(), `
SELECT string_agg(media_item_id || '=' || rating, ' ' ORDER BY media_item_id)
    || ' synced:' || (SELECT string_agg(synced_rating::text, ' ') FROM watch_provider_rating_items)
FROM user_ratings`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	migrationExec(t, tx, adminMigrationSQL(t, ratingsTenPointScoresMigration, schema, false))
	if got, want := ratings(), "movie:a=2 movie:b=8 movie:c=10 synced:8"; got != want {
		t.Fatalf("after up: %s, want %s", got, want)
	}
	// Rating cursors are cleared, so the next sync reads every provider
	// rating and imports the ones that were rounded into a star.
	var cursors string
	if err := tx.QueryRow(t.Context(), `SELECT string_agg(sync_cursors::text, ' ' ORDER BY id) FROM watch_provider_connections`).Scan(&cursors); err != nil {
		t.Fatal(err)
	}
	if want := `{"plugin.remote.watched": "w"} {}`; cursors != want {
		t.Fatalf("cursors after up: %s, want %s", cursors, want)
	}
	migrationExec(t, tx, `UPDATE user_ratings SET rating = 7 WHERE media_item_id = 'movie:b'`)
	requireMigrationSQLState(t, tx, `UPDATE user_ratings SET rating = 11 WHERE media_item_id = 'movie:a'`, "23514")
	requireMigrationSQLState(t, tx, `UPDATE watch_provider_rating_items SET synced_rating = 0`, "23514")

	// Back to stars, a half star rounding up: 7 is 4 stars.
	migrationExec(t, tx, adminMigrationSQL(t, ratingsTenPointScoresMigration, schema, true))
	if got, want := ratings(), "movie:a=1 movie:b=4 movie:c=5 synced:4"; got != want {
		t.Fatalf("after down: %s, want %s", got, want)
	}
}
