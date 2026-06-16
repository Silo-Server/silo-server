package sections

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// The recently-added/released section cards for a manga SERIES must show the
// cover of the latest-added volume/chapter (greatest created_at) instead of
// the AniList series cover. The override is implemented as a manga-gated
// (type='manga') CASE that pulls the newest linked chapter's poster, falling
// back to the series' own poster. Non-manga rows must keep mi.poster_path
// exactly.
func assertMangaPosterOverride(t *testing.T, label, query string) {
	t.Helper()
	for _, frag := range []string{
		"CASE WHEN mi.type = 'manga'",
		"manga_chapters mc ON mc.chapter_content_id = c.content_id",
		"mc.series_content_id = mi.content_id",
		"ORDER BY c.created_at DESC",
		"AS poster_path",
		"AS poster_thumbhash",
		// Poster columns default to '' (not NULL), so the override must NULLIF
		// each operand or a cover-less latest chapter blanks the series card.
		"COALESCE(NULLIF(",
		"NULLIF(mi.poster_path, '')",
		"NULLIF(mi.poster_thumbhash, '')",
	} {
		if !strings.Contains(query, frag) {
			t.Fatalf("%s query missing manga poster-override fragment %q:\n%s", label, frag, query)
		}
	}
}

func TestRecentlyAddedQueriesUseLatestMangaVolumePoster(t *testing.T) {
	t.Parallel()

	generic, _ := buildRecentlyAddedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{"filter_library_ids":[1,2]}`),
	}, nil, nil, catalog.AccessFilter{})
	assertMangaPosterOverride(t, "recently-added generic", generic)

	single, _ := buildRecentlyAddedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{"filter_library_id":1}`),
	}, nil, []int{1, 2}, catalog.AccessFilter{})
	assertMangaPosterOverride(t, "recently-added single-library", single)
}

func TestRecentlyReleasedQueryUsesLatestMangaVolumePoster(t *testing.T) {
	t.Parallel()

	query, _ := buildRecentlyReleasedQuery(ResolvedSection{
		ItemLimit: 12,
		Config:    json.RawMessage(`{}`),
	}, nil, nil, catalog.AccessFilter{})
	assertMangaPosterOverride(t, "recently-released", query)
}
