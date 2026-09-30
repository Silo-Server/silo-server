package migrations

import "testing"

const chapterThumbnailsServedVariantMigration = "20260929234755_chapter_thumbnails_point_at_served_variant"

// The migration points every legacy chapter thumbnail original at the w300
// object beside it, keeps chapter order and every other field, and leaves
// paths it does not recognize alone.
func TestChapterThumbnailsServedVariantMigrationRewritesOnlyLegacyOriginalsPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE media_files (id integer PRIMARY KEY, chapters jsonb);
INSERT INTO media_files (id, chapters) VALUES
    (1, '[{"index": 0, "title": "A", "thumbnail_path": "chapter-images/1/0/original.webp", "thumbnail_thumbhash": "h0"},
          {"index": 1, "title": "B", "thumbnail_path": "chapter-images/1/1/original.webp", "thumbnail_thumbhash": "h1"},
          {"index": 2, "title": "C", "thumbnail_path": "", "thumbnail_last_error": "extract failed"}]'),
    (2, '[{"index": 0, "thumbnail_path": "chapter-images/2/0/w300.webp"}]'),
    (3, '[{"index": 0, "thumbnail_path": "other/3/0/original.webp"},
          {"index": 1, "thumbnail_path": "chapter-images/3/1/original.jpg"}]'),
    (4, '[]'),
    (5, NULL);`)

	chapters := func(id int) string {
		t.Helper()
		var got *string
		if err := tx.QueryRow(t.Context(), `SELECT chapters::text FROM media_files WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == nil {
			return "NULL"
		}
		return *got
	}
	want := map[int]string{
		1: `[{"index": 0, "title": "A", "thumbnail_path": "chapter-images/1/0/w300.webp", "thumbnail_thumbhash": "h0"}, ` +
			`{"index": 1, "title": "B", "thumbnail_path": "chapter-images/1/1/w300.webp", "thumbnail_thumbhash": "h1"}, ` +
			`{"index": 2, "title": "C", "thumbnail_path": "", "thumbnail_last_error": "extract failed"}]`,
		2: `[{"index": 0, "thumbnail_path": "chapter-images/2/0/w300.webp"}]`,
		3: `[{"index": 0, "thumbnail_path": "other/3/0/original.webp"}, {"index": 1, "thumbnail_path": "chapter-images/3/1/original.jpg"}]`,
		4: `[]`,
		5: `NULL`,
	}
	check := func(stage string) {
		t.Helper()
		for id, w := range want {
			if got := chapters(id); got != w {
				t.Fatalf("%s: file %d chapters = %s, want %s", stage, id, got, w)
			}
		}
	}

	migrationExec(t, tx, adminMigrationSQL(t, chapterThumbnailsServedVariantMigration, schema, false))
	check("up")

	// The rollback is a no-op, and re-running the migration changes nothing.
	migrationExec(t, tx, adminMigrationSQL(t, chapterThumbnailsServedVariantMigration, schema, true))
	migrationExec(t, tx, adminMigrationSQL(t, chapterThumbnailsServedVariantMigration, schema, false))
	check("re-run")
}
