package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// postgresCatalogStateStore reports that the user's catalog state lives in
// PostgreSQL, which personalized catalog queries require. Every other method
// is unused here.
type postgresCatalogStateStore struct{ userstore.UserStore }

// CatalogStateInPostgres reports true, so the resolver runs personalized
// queries in SQL.
func (postgresCatalogStateStore) CatalogStateInPostgres() bool { return true }

// postgresCatalogStateProvider hands every user a postgresCatalogStateStore.
type postgresCatalogStateProvider struct{}

// ForUser returns a postgresCatalogStateStore for any user.
func (postgresCatalogStateProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return postgresCatalogStateStore{}, nil
}

// Close has nothing to release.
func (postgresCatalogStateProvider) Close() error { return nil }

// TestCatalogSearchSortsByProfilePlaysDB checks that a search sorted by plays
// orders the matches by the viewer profile's completed plays, ignoring
// unfinished plays and other profiles' plays (#3626).
func TestCatalogSearchSortsByProfilePlaysDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("search-plays-%d", time.Now().UnixNano())
	id := func(name string) string { return prefix + "-" + name }

	var library, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	profile, other := id("profile"), id("other")
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		batchEquivExec(t, pool, `DELETE FROM users WHERE id = $1`, userID)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, library)
	})
	batchEquivExec(t, pool, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $3, 'viewer'), ($2, $3, 'other')`, profile, other, userID)
	for _, movie := range []string{"once", "twice", "never"} {
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, genres, year) VALUES ($1, 'movie', $2, '{}', 2000)`, id(movie), "Zephyrine "+movie)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id(movie), library)
	}
	plays := []struct {
		movie, profile string
		completed      bool
	}{
		{"twice", profile, true}, {"twice", profile, true},
		{"once", profile, true},
		{"never", profile, false},
		{"never", other, true}, {"never", other, true}, {"never", other, true},
	}
	for i, play := range plays {
		batchEquivExec(t, pool, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed) VALUES ($1, $2, $3, $4, NOW() - $5 * INTERVAL '1 hour', $6)`,
			fmt.Sprintf("%s-play-%d", prefix, i), userID, play.profile, id(play.movie), i+1, play.completed)
	}
	// The plays sort also counts a completed progress row; an unfinished one
	// counts nothing, or "never" would tie "once" and sort ahead of it by title.
	batchEquivExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, 600, 5400, FALSE, NOW())`,
		userID, profile, id("never"))

	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).
		WithUserStoreProvider(postgresCatalogStateProvider{})
	viewer := AccessFilter{AllowedLibraryIDs: []int{library}, UserID: userID, ProfileID: profile}
	result, err := resolver.Resolve(ctx, CatalogRequest{
		Source:       CatalogSourceQuery,
		SearchQuery:  "Zephyrine",
		Query:        QueryDefinition{Sort: QuerySort{Field: "plays", Order: "desc"}},
		Limit:        10,
		CursorPaging: true,
	}, viewer)
	if err != nil {
		t.Fatalf("search sorted by plays: %v", err)
	}
	got := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		got = append(got, item.ContentID)
	}
	if want := []string{id("twice"), id("once"), id("never")}; !slices.Equal(got, want) {
		t.Fatalf("search sorted by plays = %v, want %v", got, want)
	}
}

// TestCatalogQuerySortsDateViewedUnwatchedLastDB checks that a library sorted
// by date viewed lists never-watched titles after the watched ones in both
// directions, and that cursor pages cross from watched to unwatched titles
// without dropping or repeating one.
func TestCatalogQuerySortsDateViewedUnwatchedLastDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("date-viewed-%d", time.Now().UnixNano())
	id := func(name string) string { return prefix + "-" + name }

	var library, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	profile := id("profile")
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		batchEquivExec(t, pool, `DELETE FROM users WHERE id = $1`, userID)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, library)
	})
	batchEquivExec(t, pool, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'viewer')`, profile, userID)
	for _, movie := range []string{"a", "b", "c", "d"} {
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, genres, year) VALUES ($1, 'movie', $2, '{}', 2000)`, id(movie), "Movie "+movie)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id(movie), library)
	}
	// b was watched before a, against title order. a's view is a completed
	// progress row, and d's unfinished one leaves it unwatched.
	batchEquivExec(t, pool, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed) VALUES ($1, $2, $3, $4, NOW() - INTERVAL '2 hours', TRUE)`,
		id("play-b"), userID, profile, id("b"))
	batchEquivExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, 5400, 5400, TRUE, NOW() - INTERVAL '1 hour'), ($1, $2, $4, 600, 5400, FALSE, NOW())`,
		userID, profile, id("a"), id("d"))

	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).
		WithUserStoreProvider(postgresCatalogStateProvider{})
	viewer := AccessFilter{AllowedLibraryIDs: []int{library}, UserID: userID, ProfileID: profile}
	for order, want := range map[string][]string{
		"asc":  {id("b"), id("a"), id("c"), id("d")},
		"desc": {id("a"), id("b"), id("c"), id("d")},
	} {
		var got []string
		var after *QueryCursor
		for page := 0; page <= len(want); page++ {
			result, err := resolver.Resolve(ctx, CatalogRequest{
				Source:       CatalogSourceQuery,
				Query:        QueryDefinition{LibraryIDs: []int{library}, Sort: QuerySort{Field: "date_viewed", Order: order}},
				Limit:        1,
				CursorPaging: true,
				After:        after,
			}, viewer)
			if err != nil {
				t.Fatalf("%s page %d: %v", order, page, err)
			}
			for _, item := range result.Items {
				got = append(got, item.ContentID)
			}
			if result.Next == nil {
				break
			}
			after = result.Next
		}
		if !slices.Equal(got, want) {
			t.Fatalf("date viewed %s = %v, want %v", order, got, want)
		}
	}
}
