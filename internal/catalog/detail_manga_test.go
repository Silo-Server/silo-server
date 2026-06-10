package catalog

import (
	"context"
	"strings"
	"testing"
)

// TestMangaChaptersQueryOrdering pins the manga chapter listing contract: join
// manga_chapters to media_items on the chapter content ID, scope to the series,
// and order by chapter_index (NULLs last) then sort_title. A wrong ORDER BY
// would surface chapters out of reading order in the series detail.
func TestMangaChaptersQueryOrdering(t *testing.T) {
	q := strings.Join(strings.Fields(mangaChaptersQuery), " ")

	for _, want := range []string{
		"FROM manga_chapters mc",
		"JOIN media_items m ON m.content_id = mc.chapter_content_id",
		"WHERE mc.series_content_id = $1",
		"ORDER BY mc.chapter_index NULLS LAST, m.sort_title",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("manga chapters query missing %q\nquery: %s", want, q)
		}
	}
}

// TestFetchMangaChaptersNilSafe asserts the helper never returns nil (the JSON
// payload must always carry an array) and tolerates an unconfigured pool.
func TestFetchMangaChaptersNilSafe(t *testing.T) {
	var s *DetailService
	if got := s.fetchMangaChapters(context.Background(), "series-1"); got == nil {
		t.Fatal("nil receiver should yield an empty slice, not nil")
	}

	s = &DetailService{}
	got := s.fetchMangaChapters(context.Background(), "series-1")
	if got == nil {
		t.Fatal("unconfigured pool should yield an empty slice, not nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected no chapters without a pool, got %d", len(got))
	}
}
