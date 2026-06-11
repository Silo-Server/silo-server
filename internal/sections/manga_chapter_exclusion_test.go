package sections

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// mangaChapterExclusionSQL is the predicate that library-listing section
// builders must carry so manga CHAPTER rows (type='ebook' linked into a manga
// series) never surface as standalone cards.
const mangaChapterExclusionSQL = "NOT EXISTS (SELECT 1 FROM manga_chapters mc WHERE mc.chapter_content_id = mi.content_id)"

func TestRecentlyAddedQueriesExcludeMangaChapters(t *testing.T) {
	t.Parallel()

	// Generic multi-library path.
	generic, _ := buildRecentlyAddedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{"filter_library_ids":[1,2],"filter_type":"movie"}`),
	}, nil, nil, catalog.AccessFilter{})
	if !strings.Contains(generic, mangaChapterExclusionSQL) {
		t.Fatalf("recently-added generic query missing manga-chapter exclusion:\n%s", generic)
	}

	// Single-library fast path.
	single, _ := buildRecentlyAddedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{"filter_library_id":1,"filter_type":"movie"}`),
	}, nil, []int{1, 2}, catalog.AccessFilter{})
	if !strings.Contains(single, mangaChapterExclusionSQL) {
		t.Fatalf("recently-added single-library query missing manga-chapter exclusion:\n%s", single)
	}
}

func TestRecentlyReleasedQueryExcludesMangaChapters(t *testing.T) {
	t.Parallel()

	query, _ := buildRecentlyReleasedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{}`),
	}, nil, nil, catalog.AccessFilter{})
	if !strings.Contains(query, mangaChapterExclusionSQL) {
		t.Fatalf("recently-released query missing manga-chapter exclusion:\n%s", query)
	}
}

func TestRandomQueryExcludesMangaChapters(t *testing.T) {
	t.Parallel()

	query, _, _ := buildRandomQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{}`),
	}, nil, nil, catalog.AccessFilter{})
	if !strings.Contains(query, mangaChapterExclusionSQL) {
		t.Fatalf("random query missing manga-chapter exclusion:\n%s", query)
	}
}
