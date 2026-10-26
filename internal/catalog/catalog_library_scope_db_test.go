package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestCatalogQueryRequestedLibraryOutsideAccessDB pins the catalog query
// source (GET /api/v1|v2/catalog?library_id=) to the viewer's library access:
// asking for a library outside the allowed set, or one that does not exist,
// must return nothing rather than fall back to an unscoped catalog. Both the
// offset and the cursor pager are checked, with their totals, for item and
// episode scopes.
func TestCatalogQueryRequestedLibraryOutsideAccessDB(t *testing.T) {
	episodeRepo, seriesID, _ := seedCompletionEpisodes(t, 4)
	pool := episodeRepo.pool
	ctx := t.Context()

	var seriesLibrary int
	var episodeID string
	if err := pool.QueryRow(ctx, `SELECT el.media_folder_id, e.content_id FROM episode_libraries el JOIN episodes e ON e.content_id = el.episode_id
		WHERE e.series_id = $1 ORDER BY el.media_folder_id, e.episode_number LIMIT 1`, seriesID).Scan(&seriesLibrary, &episodeID); err != nil {
		t.Fatal(err)
	}
	batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, seriesID, seriesLibrary)

	prefix := fmt.Sprintf("libscope-%d", time.Now().UnixNano())
	var allowedLibrary, hiddenLibrary int
	for _, id := range []*int{&allowedLibrary, &hiddenLibrary} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	allowedMovie, hiddenMovie := prefix+"-allowed", prefix+"-hidden"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{allowedMovie, hiddenMovie})
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int{allowedLibrary, hiddenLibrary})
	})
	for _, movie := range []struct {
		id      string
		library int
	}{{allowedMovie, allowedLibrary}, {hiddenMovie, hiddenLibrary}} {
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'released', '{}')`, movie.id)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, movie.id, movie.library)
	}
	nonexistentLibrary := hiddenLibrary + 1_000_000

	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	cases := []struct {
		name      string
		scope     string
		requested int
		allowed   []int
		want      string // a seeded item that must be listed; "" means the result is empty
	}{
		{name: "movie in an inaccessible library", scope: "movie", requested: hiddenLibrary, allowed: []int{allowedLibrary}},
		{name: "unscoped in an inaccessible library", requested: hiddenLibrary, allowed: []int{allowedLibrary}},
		{name: "movie in a nonexistent library", scope: "movie", requested: nonexistentLibrary, allowed: []int{allowedLibrary}},
		{name: "episode in an inaccessible library", scope: "episode", requested: seriesLibrary, allowed: []int{allowedLibrary}},
		{name: "episode in a nonexistent library", scope: "episode", requested: nonexistentLibrary, allowed: []int{allowedLibrary}},
		{name: "movie in an accessible library", scope: "movie", requested: allowedLibrary, allowed: []int{allowedLibrary}, want: allowedMovie},
		{name: "episode in an accessible library", scope: "episode", requested: seriesLibrary, allowed: []int{seriesLibrary}, want: episodeID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := AccessFilter{AllowedLibraryIDs: tc.allowed}
			for _, cursor := range []bool{false, true} {
				req := CatalogRequest{
					Source:       CatalogSourceQuery,
					Query:        QueryDefinition{MediaScope: tc.scope, LibraryIDs: []int{tc.requested}},
					Limit:        50,
					CursorPaging: cursor,
				}
				result, err := resolver.Resolve(ctx, req, filter)
				if err != nil {
					t.Fatalf("cursor=%v: resolve: %v", cursor, err)
				}
				if tc.want == "" {
					if len(result.Items) != 0 || result.Total != 0 || result.HasMore {
						t.Fatalf("cursor=%v: got %d items, total %d, hasMore %v; want an empty result", cursor, len(result.Items), result.Total, result.HasMore)
					}
					continue
				}
				found := false
				for _, item := range result.Items {
					if item.ContentID == hiddenMovie {
						t.Fatalf("cursor=%v: listed an item from an inaccessible library", cursor)
					}
					found = found || item.ContentID == tc.want
				}
				if !found || result.Total == 0 {
					t.Fatalf("cursor=%v: %s missing from %d items (total %d)", cursor, tc.want, len(result.Items), result.Total)
				}
			}
		})
	}
}
