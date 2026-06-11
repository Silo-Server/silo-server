package catalog

import (
	"strings"
	"testing"
)

// TestMangaCountColumns pins the browse-card manga count contract: two
// index-backed correlated subqueries over manga_chapters, scoped to the series
// content ID and aliased so scanBrowseItems can read them positionally. The
// volume count must filter out NULL/empty volume tokens so the frontend can
// pick a volume-based vs chapter-based chip.
func TestMangaCountColumns(t *testing.T) {
	cols := mangaCountColumns("mi")

	for _, want := range []string{
		"FROM manga_chapters mc",
		"mc.series_content_id = mi.content_id",
		"AS manga_chapter_count",
		"mc.volume IS NOT NULL AND mc.volume <> ''",
		"AS manga_volume_count",
	} {
		if !strings.Contains(cols, want) {
			t.Fatalf("manga count columns missing %q\ngot: %s", want, cols)
		}
	}

	// Both counts must be present (two SELECT count(*) subqueries).
	if got := strings.Count(cols, "SELECT count(*) FROM manga_chapters mc"); got != 2 {
		t.Fatalf("expected 2 manga count subqueries, got %d\ngot: %s", got, cols)
	}
}

// The library page browses through the catalog query preview path
// (previewQuerySource -> QueryExecutor.PreviewPage), not BrowseRepository, so
// the preview-page SELECT must carry the same manga count columns or manga
// cards in /library/{id}?tab=library render without the Vols/Ch chip.
func TestPreviewPageSQLIncludesMangaCounts(t *testing.T) {
	sql, _, err := (&QueryExecutor{}).buildPreviewPageSQL(
		QueryDefinition{MediaScope: "manga"},
		AccessFilter{},
		20, 0, true,
	)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL error: %v", err)
	}
	for _, want := range []string{"AS manga_chapter_count", "AS manga_volume_count"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("preview-page SQL missing %q\ngot: %s", want, sql)
		}
	}
}
