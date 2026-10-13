package migrations

import (
	"fmt"
	"testing"
)

const chapterThumbnailsServedVariantMigration = "20260929234755_chapter_thumbnails_point_at_served_variant"

// The migration points every legacy chapter thumbnail original at the w300
// object beside it, keeps chapter order and every other field, and leaves
// paths it does not recognize alone. Its trigger applies the same rewrite to
// later writes, which is what fences writers still on an earlier build.
func TestChapterThumbnailsServedVariantMigrationRewritesOnlyLegacyOriginalsPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE media_files (id integer PRIMARY KEY, chapters jsonb, note text);
INSERT INTO media_files (id, chapters) VALUES
    (1, '[{"index": 0, "title": "A", "thumbnail_path": "chapter-images/1/0/original.webp", "thumbnail_thumbhash": "h0"},
          {"index": 1, "title": "B", "thumbnail_path": "chapter-images/1/1/original.webp", "thumbnail_thumbhash": "h1"},
          {"index": 2, "title": "C", "thumbnail_path": "", "thumbnail_last_error": "extract failed"}]'),
    (2, '[{"index": 0, "thumbnail_path": "chapter-images/2/0/w300.webp"}]'),
    (3, '[{"index": 0, "thumbnail_path": "other/3/0/original.webp"},
          {"index": 1, "thumbnail_path": "chapter-images/3/1/original.jpg"}]'),
    (4, '[]'),
    (5, NULL),
    (6, '{"thumbnail_path": "chapter-images/6/0/original.webp"}'),
    (7, '["chapter-images/7/0/original.webp", 3]');`)

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
		6: `{"thumbnail_path": "chapter-images/6/0/original.webp"}`,
		7: `["chapter-images/7/0/original.webp", 3]`,
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

	// A writer still on an earlier build saves original paths again, by
	// update and by insert. Updates that leave chapters alone are untouched.
	legacy := `[{"index": 0, "thumbnail_path": "chapter-images/%d/0/original.webp"}]`
	migrationExec(t, tx, `
UPDATE media_files SET chapters = '[{"index": 0, "thumbnail_path": "chapter-images/2/0/original.webp"}]' WHERE id = 2;
INSERT INTO media_files (id, chapters) VALUES (8, '[{"index": 0, "thumbnail_path": "chapter-images/8/0/original.webp"}]');
UPDATE media_files SET note = 'touched' WHERE id = 3;`)
	want[8] = `[{"index": 0, "thumbnail_path": "chapter-images/8/0/w300.webp"}]`
	check("legacy writes")

	// Down removes the trigger and keeps the rewritten paths.
	migrationExec(t, tx, adminMigrationSQL(t, chapterThumbnailsServedVariantMigration, schema, true))
	check("down")
	migrationExec(t, tx, `UPDATE media_files SET chapters = '`+fmt.Sprintf(legacy, 2)+`' WHERE id = 2`)
	if got, w := chapters(2), fmt.Sprintf(legacy, 2); got != w {
		t.Fatalf("after down: file 2 chapters = %s, want the write kept as %s", got, w)
	}

	// Up again re-creates the trigger and rewrites the row once more.
	migrationExec(t, tx, adminMigrationSQL(t, chapterThumbnailsServedVariantMigration, schema, false))
	check("re-run")
}
