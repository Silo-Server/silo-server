package migrations

import (
	"strings"
	"testing"
)

func TestEbookEnrichmentReconcileCursorPersistsAcrossRestartsAndUsesCoveringIndex(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260719173000_ebook_enrichment_reconcile_cursor.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := strings.Join(strings.Fields(string(migrationBytes)), " ")
	for _, fragment := range []string{
		"-- +goose NO TRANSACTION",
		"CREATE TABLE IF NOT EXISTS ebook_enrichment_reconcile_cursors",
		"folder_id integer PRIMARY KEY REFERENCES media_folders(id) ON DELETE CASCADE",
		"after_first_seen_at timestamptz",
		"after_content_id text",
		"CHECK ((after_first_seen_at IS NULL) = (after_content_id IS NULL))",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_item_libraries_folder_enrichment_cursor",
		"ON public.media_item_libraries (media_folder_id, first_seen_at DESC, content_id DESC)",
		"DROP INDEX CONCURRENTLY IF EXISTS idx_item_libraries_folder_enrichment_cursor",
		"DROP TABLE IF EXISTS ebook_enrichment_reconcile_cursors",
	} {
		if !strings.Contains(migration, fragment) {
			t.Fatalf("cursor migration missing %q", fragment)
		}
	}
}
