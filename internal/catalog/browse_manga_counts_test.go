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

// TestBrowseScopeMayContainManga pins the gating that lets browse skip the two
// manga count subqueries when the scope cannot return manga rows. An empty type
// filter (all types) or one that includes "manga" keeps them; any other
// explicit type filter rules manga out.
func TestBrowseScopeMayContainManga(t *testing.T) {
	cases := []struct {
		typeFilter string
		want       bool
	}{
		{"", true},
		{"manga", true},
		{"movie,manga", true},
		{" manga ", true},
		{"movie", false},
		{"movie,series,episode", false},
		{"ebook", false},
	}
	for _, c := range cases {
		if got := browseScopeMayContainManga(BrowseFilters{Type: c.typeFilter}); got != c.want {
			t.Fatalf("browseScopeMayContainManga(%q) = %v, want %v", c.typeFilter, got, c.want)
		}
	}
}

// nullMangaCountColumns must keep the exact column names and order of
// mangaCountColumns so the shared scan path is unchanged when the subqueries
// are skipped.
func TestNullMangaCountColumnsMatchScanContract(t *testing.T) {
	null := nullMangaCountColumns()
	for _, want := range []string{"AS manga_chapter_count", "AS manga_volume_count"} {
		if !strings.Contains(null, want) {
			t.Fatalf("null manga count columns missing %q\ngot: %s", want, null)
		}
	}
	if strings.Contains(null, "FROM manga_chapters") {
		t.Fatalf("null manga count columns must not run subqueries\ngot: %s", null)
	}
	if a, b := strings.Index(null, "manga_chapter_count"), strings.Index(null, "manga_volume_count"); a > b {
		t.Fatalf("column order must match mangaCountColumns (chapter then volume)\ngot: %s", null)
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
