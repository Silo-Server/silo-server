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
