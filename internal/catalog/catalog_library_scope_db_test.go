package catalog

import (
	"context"
	"fmt"
	"slices"
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

// TestCatalogQueryWithoutLibraryLeavesOutItemsInNoLibraryDB covers issue
// #2818: a browse that names no library (GET /api/v2/catalog?type=movie) by
// an unrestricted viewer leaves out an item in no library, such as one whose
// file was renamed away and that only the scanner's file removal grace keeps,
// as that item's library page already does. The offset browse that v1 and
// source-order lists use leaves it out too. The genre filter values leave out
// a genre only that item carries, so no offered filter returns nothing.
func TestCatalogQueryWithoutLibraryLeavesOutItemsInNoLibraryDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := t.Context()
	prefix := fmt.Sprintf("noLibrary-%d", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	member, orphan := prefix+"-member", prefix+"-orphan"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{member, orphan})
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, library)
	})
	for _, id := range []string{member, orphan} {
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'unmatched', ARRAY[$1])`, id)
	}
	batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, member, library)

	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	for _, cursor := range []bool{false, true} {
		result, err := resolver.Resolve(ctx, CatalogRequest{
			Source:       CatalogSourceQuery,
			Query:        QueryDefinition{MediaScope: "movie"},
			NamePrefix:   prefix,
			Limit:        50,
			CursorPaging: cursor,
		}, AccessFilter{})
		if err != nil {
			t.Fatalf("cursor=%v: resolve: %v", cursor, err)
		}
		if len(result.Items) != 1 || result.Items[0].ContentID != member || result.Total != 1 {
			ids := make([]string, 0, len(result.Items))
			for _, item := range result.Items {
				ids = append(ids, item.ContentID)
			}
			t.Fatalf("cursor=%v: listed %v (total %d), want only %s", cursor, ids, result.Total, member)
		}
	}
	browse := NewBrowseRepository(pool)
	browsed, err := browse.Browse(ctx, BrowseFilters{Type: "movie", NamePrefix: prefix, Limit: 50})
	if err != nil {
		t.Fatalf("offset browse: %v", err)
	}
	if len(browsed.Items) != 1 || browsed.Items[0].ContentID != member || browsed.Total != 1 {
		t.Fatalf("offset browse listed %d items (total %d), want only %s", len(browsed.Items), browsed.Total, member)
	}
	genres, err := browse.ListGenres(ctx, BrowseFilters{Type: "movie"})
	if err != nil {
		t.Fatalf("list genres: %v", err)
	}
	if !slices.Contains(genres, member) || slices.Contains(genres, orphan) {
		t.Fatalf("genres include member %v and orphan %v, want only the member's", slices.Contains(genres, member), slices.Contains(genres, orphan))
	}
}

// TestCountsAndListingsLeaveOutItemsHeldByRemovalGraceDB covers a title whose
// file was moved or renamed away: it has no library membership and is kept
// only by the scanner's file removal grace. Counts and listings that name no
// library leave it out for an unrestricted viewer, as its library page does:
// collection item counts, a person's credit counts and search, and upcoming
// episodes of a held series. A read by ID still opens a person credited only
// on the held title, as it opens the title.
func TestCountsAndListingsLeaveOutItemsHeldByRemovalGraceDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := t.Context()
	prefix := fmt.Sprintf("held-%d", time.Now().UnixNano())
	var library, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	profile := prefix + "-profile"
	member, held, heldSeries := prefix+"-member", prefix+"-held", prefix+"-held-series"
	stored, personal := prefix+"-stored", prefix+"-personal"
	var both, heldOnly int64
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM library_collections WHERE id = $1`, stored)
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = $1`, heldSeries)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{member, held, heldSeries})
		_, _ = pool.Exec(ctx, `DELETE FROM people WHERE id = ANY($1)`, []int64{both, heldOnly})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, library)
	})
	batchEquivExec(t, pool, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'viewer')`, profile, userID)
	batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, sort_title, status) VALUES
		($1, 'movie', $1, $1, 'unmatched'), ($2, 'movie', $2, $2, 'unmatched'), ($3, 'series', $3, $3, 'unmatched')`, member, held, heldSeries)
	batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, member, library)
	batchEquivExec(t, pool, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, air_date)
		VALUES ($1 || '-s01e01', $1, 1, 1, 'Pilot', NOW() + INTERVAL '2 days')`, heldSeries)
	batchEquivExec(t, pool, `INSERT INTO library_collections (id, library_id, slug, title, collection_type) VALUES ($1, $2, $1, $1, 'manual')`, stored, library)
	batchEquivExec(t, pool, `INSERT INTO library_collection_items (collection_id, media_item_id) VALUES ($1, $2), ($1, $3)`, stored, member, held)
	batchEquivExec(t, pool, `INSERT INTO user_personal_collections (id, user_id, profile_id, name) VALUES ($1, $2, $3, $1)`, personal, userID, profile)
	batchEquivExec(t, pool, `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id) VALUES ($1, $2, $3), ($1, $2, $4)`, userID, personal, member, held)
	both, heldOnly = time.Now().UnixNano(), time.Now().UnixNano()+1
	batchEquivExec(t, pool, `INSERT INTO people (id, name) VALUES ($1, $3 || ' Both'), ($2, $3 || ' HeldOnly')`, both, heldOnly, prefix)
	batchEquivExec(t, pool, `INSERT INTO item_people (id, content_id, person_id, kind) VALUES ($3, $1, $3, 0), ($4, $2, $3, 0), ($5, $2, $4, 0)`, member, held, both, heldOnly, both+2)

	viewer := AccessFilter{UserID: userID, ProfileID: profile}

	t.Run("stored collection item count", func(t *testing.T) {
		c, err := NewLibraryCollectionRepository(pool).GetByID(ctx, stored)
		if err != nil {
			t.Fatal(err)
		}
		if c.ItemCount != 1 {
			t.Fatalf("item count %d, want 1", c.ItemCount)
		}
	})
	t.Run("personal collection item count", func(t *testing.T) {
		counts, err := NewItemRepository(pool).CountVisiblePersonalCollectionMembers(ctx, userID, []string{personal}, viewer)
		if err != nil {
			t.Fatal(err)
		}
		if counts[personal] != 1 {
			t.Fatalf("item count %d, want 1", counts[personal])
		}
	})
	t.Run("person credit counts", func(t *testing.T) {
		counts, err := NewPersonRepository(pool).CountItemsByType(ctx, both)
		if err != nil {
			t.Fatal(err)
		}
		if counts["movie"] != 1 {
			t.Fatalf("movie count %d, want 1", counts["movie"])
		}
	})
	t.Run("people search", func(t *testing.T) {
		people, total, err := NewPersonRepository(pool).SearchVisible(ctx, prefix, false, 10, 0, AccessFilter{}, true)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(people) != 1 || people[0].ID != both {
			t.Fatalf("found %d people (total %d), want only %d", len(people), total, both)
		}
		if _, err := NewPersonRepository(pool).GetVisible(ctx, heldOnly, AccessFilter{}); err != nil {
			t.Fatalf("read by ID of a person credited only on the held title: %v", err)
		}
	})
	t.Run("upcoming episodes", func(t *testing.T) {
		episodes, total, err := NewEpisodeRepository(pool).ListUpcoming(ctx, time.Now().UTC(), heldSeries, "", 0, 10, 0, AccessFilter{}, true)
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 || len(episodes) != 0 {
			t.Fatalf("listed %d upcoming episodes (total %d) of a held series, want none", len(episodes), total)
		}
	})
}
