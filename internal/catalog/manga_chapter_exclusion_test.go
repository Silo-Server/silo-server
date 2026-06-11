package catalog

import (
	"strings"
	"testing"
)

// mangaChapterExclusionFor returns the predicate that excludes manga chapter
// ebook rows (type='ebook' rows linked via manga_chapters) from a catalog
// listing query keyed on the given media_items alias. Manga chapters are
// internal sub-units of a type='manga' series and must never surface as
// standalone catalog items on browse / section / search surfaces.
//
// TestMangaChapterExclusionPredicate_AllListingBuilders pins the exact text so
// the three independent listing builders (buildBrowsePlan,
// QueryExecutor.buildPreviewPagePlan, ItemRepository.buildSearchSQL) all carry
// the same index-backed exclusion (manga_chapters.chapter_content_id is the PK).
func mangaChapterExclusionPredicate(alias string) string {
	return "NOT EXISTS (SELECT 1 FROM manga_chapters mc WHERE mc.chapter_content_id = " + alias + ".content_id)"
}

func TestMangaChapterExclusion_BrowsePlan(t *testing.T) {
	repo := &BrowseRepository{}
	plan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{Type: "ebook"})
	if err != nil || earlyEmpty {
		t.Fatalf("buildBrowsePlan err=%v earlyEmpty=%v", err, earlyEmpty)
	}
	if !strings.Contains(plan.whereClause, mangaChapterExclusionPredicate("mi")) {
		t.Fatalf("browse plan WHERE missing manga-chapter exclusion.\ngot: %s", plan.whereClause)
	}
}

func TestMangaChapterExclusion_PreviewPageSQL(t *testing.T) {
	sql, _, err := (&QueryExecutor{}).buildPreviewPageSQL(
		QueryDefinition{MediaScope: "ebook"},
		AccessFilter{},
		20, 0, true,
	)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL error: %v", err)
	}
	if !strings.Contains(sql, mangaChapterExclusionPredicate("mi")) {
		t.Fatalf("preview-page SQL missing manga-chapter exclusion.\ngot: %s", sql)
	}
}

func TestMangaChapterExclusion_SearchSQL(t *testing.T) {
	repo := &ItemRepository{}
	sql, _, _ := repo.buildSearchSQL("naruto", []string{"ebook"}, 20, 0, AccessFilter{})
	if !strings.Contains(sql, mangaChapterExclusionPredicate("mi")) {
		t.Fatalf("search SQL missing manga-chapter exclusion.\ngot: %s", sql)
	}
}
