package catalog

import (
	"strings"
	"testing"
)

// TestMangaCountColumns pins the browse-card manga count contract: two
// index-backed correlated subqueries over manga_chapters, scoped to the series
// content ID and aliased so the scan paths can read them positionally. The
// card chip reads "X Volumes · X Chapters", so manga_volume_count must count
// DISTINCT volume tokens (many chapter rows can share one volume) and
// manga_chapter_count must count only loose rows without a volume token.
func TestMangaCountColumns(t *testing.T) {
	cols := mangaCountColumns("mi")

	for _, want := range []string{
		"FROM manga_chapters mc",
		"mc.series_content_id = mi.content_id",
		"count(DISTINCT mc.volume)",
		"mc.volume IS NOT NULL AND mc.volume <> ''",
		"AS manga_volume_count",
		"(mc.volume IS NULL OR mc.volume = '')",
		"AS manga_chapter_count",
	} {
		if !strings.Contains(cols, want) {
			t.Fatalf("manga count columns missing %q\ngot: %s", want, cols)
		}
	}

	// Both counts must be present (two correlated subqueries).
	if got := strings.Count(cols, "FROM manga_chapters mc"); got != 2 {
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
