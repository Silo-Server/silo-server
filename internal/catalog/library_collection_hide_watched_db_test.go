package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// hideWatchedFixture is a library of three movies, one account with two
// profiles of which only watcher has finished the first movie, and server
// collections over those movies.
type hideWatchedFixture struct {
	pool     *pgxpool.Pool
	repo     *LibraryCollectionRepository
	library  int
	movies   []string
	watcher  AccessFilter
	other    AccessFilter
	resolver *CatalogResolver
}

func newHideWatchedFixture(t *testing.T) hideWatchedFixture {
	t.Helper()
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("hide-watched-%d", time.Now().UnixNano())
	f := hideWatchedFixture{pool: pool, repo: NewLibraryCollectionRepository(pool)}
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix).Scan(&f.library); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM library_collections WHERE slug LIKE $1`, prefix+"%")
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		batchEquivExec(t, pool, `DELETE FROM users WHERE id = $1`, userID)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, f.library)
	})
	watcher, other := prefix+"-watcher", prefix+"-other"
	batchEquivExec(t, pool, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $3, 'watcher'), ($2, $3, 'other')`, watcher, other, userID)
	for i := range 3 {
		id := fmt.Sprintf("%s-movie-%d", prefix, i)
		f.movies = append(f.movies, id)
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, status, genres, year) VALUES ($1, 'movie', $1, 'released', '{}', 2000)`, id)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, f.library)
	}
	batchEquivExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, 5400, 5400, TRUE, NOW())`,
		userID, watcher, f.movies[0])
	f.watcher = AccessFilter{UserID: userID, ProfileID: watcher}
	f.other = AccessFilter{UserID: userID, ProfileID: other}
	f.resolver = NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).
		WithUserStoreProvider(postgresCatalogStateProvider{})
	return f
}

// collections creates a manual and a smart collection holding every movie,
// with hideWatched as given, and reads them back as a listing does.
func (f hideWatchedFixture) collections(t *testing.T, hideWatched bool) []*models.LibraryCollection {
	t.Helper()
	ctx := context.Background()
	definition, err := json.Marshal(QueryDefinition{LibraryIDs: []int{f.library}, MediaScope: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	var out []*models.LibraryCollection
	for _, kind := range []string{"manual", "smart"} {
		input := CreateLibraryCollectionInput{
			LibraryID: f.library, Slug: fmt.Sprintf("%s-%s-%t", f.movies[0], kind, hideWatched), Title: kind,
			CollectionType: kind, Visibility: "visible", HideWatched: hideWatched,
		}
		if kind == "smart" {
			input.QueryDefinition = definition
		}
		c, err := f.repo.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "manual" {
			for pos, id := range f.movies {
				if err := f.repo.AddItem(ctx, c.ID, id, pos); err != nil {
					t.Fatal(err)
				}
			}
		}
		// Smart collections carry a cached count the refresher keeps; a
		// listing reads it.
		batchEquivExec(t, f.pool, `UPDATE library_collections SET item_count_cached = 3 WHERE id = $1`, c.ID)
		read, err := f.repo.GetByID(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if read.HideWatched != hideWatched {
			t.Fatalf("%s collection reads hide_watched=%t, want %t", kind, read.HideWatched, hideWatched)
		}
		out = append(out, read)
	}
	return out
}

// A server collection that hides watched titles leaves out, on its page,
// what the viewer's profile has watched, and only that: another profile on
// the same account still sees every member.
func TestLibraryCollectionHidesViewersWatchedTitlesDB(t *testing.T) {
	f := newHideWatchedFixture(t)
	ctx := context.Background()
	everyone := f.movies
	unwatched := f.movies[1:]
	byFlag := map[bool][]*models.LibraryCollection{true: f.collections(t, true), false: f.collections(t, false)}
	for _, tc := range []struct {
		name        string
		hideWatched bool
		viewer      AccessFilter
		want        []string
	}{
		{"hides what the viewer watched", true, f.watcher, unwatched},
		{"another profile sees every member", true, f.other, everyone},
		{"off keeps watched titles", false, f.watcher, everyone},
	} {
		for _, c := range byFlag[tc.hideWatched] {
			for _, cursor := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/cursor=%t", tc.name, c.CollectionType, cursor), func(t *testing.T) {
					result, err := f.resolver.Resolve(ctx, CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: c.ID, CursorPaging: cursor, Limit: 50}, tc.viewer)
					if err != nil {
						t.Fatal(err)
					}
					var got []string
					for _, item := range result.Items {
						got = append(got, item.ContentID)
					}
					slices.Sort(got)
					if !slices.Equal(got, tc.want) {
						t.Fatalf("items = %v, want %v", got, tc.want)
					}
					if result.TotalExact && result.Total != len(tc.want) {
						t.Fatalf("total = %d, want %d", result.Total, len(tc.want))
					}
				})
			}
		}
	}
}

// A capped smart collection picks its members before hiding any: the
// viewer's watched member leaves a gap rather than letting the next match in.
func TestCappedLibraryCollectionHidesAfterPickingMembersDB(t *testing.T) {
	f := newHideWatchedFixture(t)
	ctx := context.Background()
	definition, err := json.Marshal(QueryDefinition{LibraryIDs: []int{f.library}, MediaScope: "movie", Sort: QuerySort{Field: "title", Order: "asc"}, Limit: new(2)})
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryID: f.library, Slug: f.movies[0] + "-capped", Title: "capped", CollectionType: "smart",
		Visibility: "visible", HideWatched: true, QueryDefinition: definition,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []bool{false, true} {
		result, err := f.resolver.Resolve(ctx, CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: c.ID, CursorPaging: cursor, Limit: 50}, f.watcher)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, item := range result.Items {
			got = append(got, item.ContentID)
		}
		if want := f.movies[1:2]; !slices.Equal(got, want) {
			t.Fatalf("cursor=%t: items = %v, want %v", cursor, got, want)
		}
	}
}

// A server collection that hides watched titles counts, for each viewer, the
// members that viewer still sees, so its card matches its page. Collections
// listed beside it keep their stored counts.
func TestLibraryCollectionCountsHideViewersWatchedTitlesDB(t *testing.T) {
	f := newHideWatchedFixture(t)
	ctx := context.Background()
	hiding := f.collections(t, true)
	keeping := f.collections(t, false)
	listed := append(slices.Clone(hiding), keeping...)
	for _, tc := range []struct {
		name   string
		viewer AccessFilter
		hiding int
	}{
		{"the profile that watched one", f.watcher, 2},
		{"another profile", f.other, 3},
		{"a library-limited viewer", AccessFilter{UserID: f.watcher.UserID, ProfileID: f.watcher.ProfileID, AllowedLibraryIDs: []int{f.library}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts, err := f.repo.ViewerCollectionCounts(ctx, listed, tc.viewer)
			if err != nil {
				t.Fatal(err)
			}
			shown := CollectionsShownToViewer(listed, counts)
			if len(shown) != len(listed) {
				t.Fatalf("shown %d collections, want %d", len(shown), len(listed))
			}
			for _, c := range shown {
				want := 3
				if c.HideWatched {
					want = tc.hiding
				}
				if c.ItemCount != want {
					t.Errorf("%s collection (hide_watched=%t) counts %d, want %d", c.CollectionType, c.HideWatched, c.ItemCount, want)
				}
			}
		})
	}
}

// An episode collection that hides watched titles lists the episodes the
// viewer has not watched on every read: offset pages (with and without a
// sort) and play or shuffle read them as episodes, as the cursor page does.
func TestEpisodeLibraryCollectionHidesWatchedEpisodesDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := t.Context()
	f := seedEpisodeCountLibrary(t, pool)
	repo := NewLibraryCollectionRepository(pool)
	definition, err := json.Marshal(QueryDefinition{MediaScope: "episode", LibraryIDs: []int{f.library}, Sort: QuerySort{Field: "title", Order: "asc"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryID: f.library, Slug: f.id("hide-watched-episodes"), Title: "episodes", CollectionType: "smart",
		Visibility: "visible", HideWatched: true, QueryDefinition: definition,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { batchEquivExec(t, pool, `DELETE FROM library_collections WHERE id = $1`, c.ID) })
	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithUserStoreProvider(postgresCatalogStateProvider{})
	viewer := AccessFilter{UserID: f.userID, ProfileID: f.profile, AllowedLibraryIDs: []int{f.library}}
	// The profile has finished two of the library's eight episodes.
	const unwatched = 6
	for name, req := range map[string]CatalogRequest{
		"offset":         {Limit: 50},
		"offset, sorted": {Limit: 50, Query: QueryDefinition{Sort: QuerySort{Field: "title", Order: "desc"}}},
		"cursor":         {Limit: 50, CursorPaging: true},
	} {
		req.Source, req.CollectionID = CatalogSourceLibraryCollection, c.ID
		result, err := resolver.Resolve(ctx, req, viewer)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(result.Items) != unwatched {
			t.Errorf("%s lists %d episodes, want %d", name, len(result.Items), unwatched)
		}
	}
	_, items, err := resolver.loadCollectionSource(ctx, CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: c.ID}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != unwatched {
		t.Errorf("play and shuffle read %d episodes, want %d", len(items), unwatched)
	}
}

// A viewer whose access narrows nothing could always see every server
// collection's card. When a collection that hides watched titles can't be
// counted for that viewer (here its stored rules no longer validate), it keeps
// its stored count instead of dropping out of the list.
func TestHideWatchedCollectionKeepsItsCardWhenItsCountFailsDB(t *testing.T) {
	f := newHideWatchedFixture(t)
	ctx := t.Context()
	c := f.collections(t, true)[1]
	batchEquivExec(t, f.pool, `UPDATE library_collections SET query_definition = '{"groups":[{"match":"all","rules":[{"field":"no_such_field","op":"is","value":true}]}]}' WHERE id = $1`, c.ID)
	read, err := f.repo.GetByID(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	listed := []*models.LibraryCollection{read}
	counts, err := f.repo.ViewerCollectionCounts(ctx, listed, f.watcher)
	if err == nil {
		t.Fatal("counting a collection whose rules don't validate succeeded; the test needs a failing count")
	}
	shown := CollectionsShownToViewer(listed, counts)
	if len(shown) != 1 || shown[0].ItemCount != read.ItemCount {
		t.Fatalf("shown = %+v, want the collection with its stored count %d", shown, read.ItemCount)
	}
}
