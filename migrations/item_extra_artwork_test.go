package migrations

import (
	"os"
	"strings"
	"testing"
)

const itemExtraArtworkMigration = "20261011223116_add_item_extra_artwork"
const itemExtraArtworkIndexMigration = "20261011223117_index_item_extra_artwork_paths"

// The migration adds the banner, thumb, clear art and disc slots and the
// extra-backdrop table, queues displaced copies of both for the revision GC,
// and lets cache jobs target them. Down puts every piece back.
func TestItemExtraArtworkMigrationUpAndDownPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	// The pieces of the migrated schema this migration changes, as they are
	// before it runs.
	migrationExec(t, tx, `
CREATE TABLE media_items (content_id text PRIMARY KEY, poster_path text NOT NULL DEFAULT '', backdrop_path text NOT NULL DEFAULT '', logo_path text NOT NULL DEFAULT '');
CREATE TABLE artwork_revision_gc_candidates (LIKE public.artwork_revision_gc_candidates INCLUDING ALL);
CREATE TABLE metadata_image_cache_jobs (LIKE public.metadata_image_cache_jobs INCLUDING ALL);
ALTER TABLE metadata_image_cache_jobs
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_target_check,
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_image_type_check,
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_shape_check;`)
	migrationExec(t, tx, revisionGCFunctionSQL(t, schema))
	// Down restores the constraints and triggers that predate the migration.
	migrationExec(t, tx, adminMigrationSQL(t, itemExtraArtworkMigration, schema, true))
	migrationExec(t, tx, `INSERT INTO media_items (content_id) VALUES ('m1')`)

	jobSQL := func(targetType, targetID, imageType, language string) string {
		return `INSERT INTO metadata_image_cache_jobs (target_type, target_content_id, target_language, series_id, source_path, provider_id, provider_content_id, content_type, image_type, status)
VALUES ('` + targetType + `', '` + targetID + `', '` + language + `', 'm1', 'file:///m/x.jpg', 'local', 'm1', 'movies', '` + imageType + `', 'queued')`
	}
	queued := func(path, imageType string) bool {
		t.Helper()
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM artwork_revision_gc_candidates WHERE original_path = $1 AND image_type = $2`, path, imageType).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	// Before: the jobs table refuses the new types.
	requireMigrationSQLState(t, tx, jobSQL("item", "m1", "banner", ""), "23514")
	requireMigrationSQLState(t, tx, jobSQL("item_backdrop", "7", "backdrop", ""), "23514")

	migrationExec(t, tx, adminMigrationSQL(t, itemExtraArtworkMigration, schema, false))

	var banner, thumbhash string
	if err := tx.QueryRow(t.Context(), `SELECT banner_path, thumb_thumbhash FROM media_items WHERE content_id = 'm1'`).Scan(&banner, &thumbhash); err != nil || banner != "" || thumbhash != "" {
		t.Fatalf("existing row after up: banner=%q thumbhash=%q err=%v", banner, thumbhash, err)
	}
	migrationExec(t, tx, jobSQL("item", "m1", "banner", ""))
	migrationExec(t, tx, jobSQL("item", "m1", "disc", ""))
	migrationExec(t, tx, jobSQL("item_backdrop", "7", "backdrop", ""))
	requireMigrationSQLState(t, tx, jobSQL("item_localization", "m1", "banner", "fr"), "23514")
	requireMigrationSQLState(t, tx, jobSQL("item_backdrop", "8", "poster", ""), "23514")

	// The widened checks are added NOT VALID, so the transaction that holds
	// media_items does not scan the queue; the next migration validates them.
	validatedChecks := func() int {
		t.Helper()
		var n int
		if err := tx.QueryRow(t.Context(), `
SELECT count(*) FROM pg_constraint
WHERE conrelid = 'metadata_image_cache_jobs'::regclass AND convalidated
  AND conname IN ('metadata_image_cache_jobs_target_check', 'metadata_image_cache_jobs_image_type_check', 'metadata_image_cache_jobs_shape_check')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := validatedChecks(); n != 0 {
		t.Fatalf("%d widened checks validated by the first migration, want 0", n)
	}
	for _, statement := range strings.Split(adminMigrationSQL(t, itemExtraArtworkIndexMigration, schema, false), "\n") {
		if strings.Contains(statement, "VALIDATE CONSTRAINT") {
			migrationExec(t, tx, statement)
		}
	}
	if n := validatedChecks(); n != 3 {
		t.Fatalf("%d widened checks validated after the index migration, want 3", n)
	}

	migrationExec(t, tx, `
UPDATE media_items SET banner_path = 'local/movies/m1/aa/banner/original.r1.webp', disc_path = 'local/movies/m1/bb/disc/original.r1.webp' WHERE content_id = 'm1';
UPDATE media_items SET banner_path = '' WHERE content_id = 'm1';
INSERT INTO media_item_backdrops (content_id, ordinal, path) VALUES
    ('m1', 1, 'local/movies/m1/cc-1/backdrop/original.r1.webp'),
    ('m1', 2, 'local/movies/m1/dd-2/backdrop/original.r1.webp');
UPDATE media_item_backdrops SET path = '' WHERE ordinal = 1;`)
	if !queued("local/movies/m1/aa/banner/original.r1.webp", "banner") || !queued("local/movies/m1/cc-1/backdrop/original.r1.webp", "backdrop") {
		t.Fatal("displaced banner and backdrop copies were not queued for the revision GC")
	}
	requireMigrationSQLState(t, tx, `INSERT INTO media_item_backdrops (content_id, ordinal) VALUES ('m1', 0)`, "23514")
	requireMigrationSQLState(t, tx, `INSERT INTO media_item_backdrops (content_id, ordinal) VALUES ('m1', 2)`, "23505")
	requireMigrationSQLState(t, tx, `INSERT INTO media_item_backdrops (content_id, ordinal) VALUES ('missing', 1)`, "23503")

	// A content ID rename carries the backdrops; deleting the item removes
	// them and queues every cached copy it held.
	migrationExec(t, tx, `UPDATE media_items SET content_id = 'm2' WHERE content_id = 'm1'`)
	var owner string
	if err := tx.QueryRow(t.Context(), `SELECT DISTINCT content_id FROM media_item_backdrops`).Scan(&owner); err != nil || owner != "m2" {
		t.Fatalf("backdrop owner after rename = %q, %v", owner, err)
	}
	migrationExec(t, tx, `DELETE FROM media_items WHERE content_id = 'm2'`)
	var left int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM media_item_backdrops`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("backdrops after deleting the item = %d, %v", left, err)
	}
	if !queued("local/movies/m1/bb/disc/original.r1.webp", "disc") || !queued("local/movies/m1/dd-2/backdrop/original.r1.webp", "backdrop") {
		t.Fatal("deleting the item did not queue its disc art and backdrops")
	}

	migrationExec(t, tx, `INSERT INTO media_items (content_id, poster_path) VALUES ('m3', 'local/movies/m3/ee/poster/original.r1.webp')`)
	migrationExec(t, tx, adminMigrationSQL(t, itemExtraArtworkMigration, schema, true))

	var columns, tables, newJobs int
	if err := tx.QueryRow(t.Context(), `
SELECT
    (SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'media_items'
        AND column_name ~ '^(banner|thumb|clearart|disc)_'),
    (SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'media_item_backdrops'),
    (SELECT count(*) FROM metadata_image_cache_jobs WHERE target_type = 'item_backdrop' OR image_type <> 'poster')`, schema).Scan(&columns, &tables, &newJobs); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || tables != 0 || newJobs != 0 {
		t.Fatalf("after down: %d extra columns, %d backdrop tables, %d new-type jobs", columns, tables, newJobs)
	}
	requireMigrationSQLState(t, tx, jobSQL("item", "m1", "banner", ""), "23514")
	migrationExec(t, tx, jobSQL("item", "m1", "logo", ""))
	// The poster, backdrop and logo trigger is back.
	migrationExec(t, tx, `UPDATE media_items SET poster_path = '' WHERE content_id = 'm3'`)
	if !queued("local/movies/m3/ee/poster/original.r1.webp", "poster") {
		t.Fatal("poster trigger missing after down")
	}

	// Up again on the rolled-back schema.
	migrationExec(t, tx, adminMigrationSQL(t, itemExtraArtworkMigration, schema, false))
}

// revisionGCFunctionSQL creates queue_displaced_artwork_revision in schema,
// writing to that schema's candidate table, from the migration that owns it.
func revisionGCFunctionSQL(t *testing.T, schema string) string {
	t.Helper()
	data, err := os.ReadFile("sql/20260714120826_artwork_revision_gc.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(data), "-- +goose StatementBegin")
	body, _, ok2 := strings.Cut(rest, "-- +goose StatementEnd")
	if !ok || !ok2 {
		t.Fatal("artwork revision GC migration lost its function block")
	}
	return strings.ReplaceAll(body, "public.", schema+".")
}
