package handlers

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestLibraryCollectionItemsPageReadsPastWatchedMembersDB checks that paging
// a manual server collection that hides watched titles reads on past the
// members the viewer has watched, so a page is not left empty while unwatched
// members follow, and that its continuation lists each remaining member once.
func TestLibraryCollectionItemsPageReadsPastWatchedMembersDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	prefix := fmt.Sprintf("hide-watched-page-%d", time.Now().UnixNano())
	var library, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM library_collections WHERE slug LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, library)
	})
	watcher, partial, other := prefix+"-watcher", prefix+"-partial", prefix+"-other"
	exec(`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $4, 'watcher'), ($2, $4, 'partial'), ($3, $4, 'other')`, watcher, partial, other, userID)

	repo := catalog.NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{
		LibraryID: library, Slug: prefix, Title: prefix, CollectionType: "manual", Visibility: "visible", HideWatched: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var movies []string
	for i := range 5 {
		id := fmt.Sprintf("%s-%d", prefix, i)
		movies = append(movies, id)
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$1,'released','{}',2000)`, id)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
		if err := repo.AddItem(ctx, collection.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	// The watcher has finished the first three members, and partial only the
	// first, so its first page fills partway through the second stored page.
	watched := func(profile string, ids ...string) {
		for _, id := range ids {
			exec(`INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, 5400, 5400, TRUE, NOW())`,
				userID, profile, id)
		}
	}
	watched(watcher, movies[:3]...)
	watched(partial, movies[0])

	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(pool), nil)
	h.Executor = &catalog.QueryExecutor{Pool: pool}
	for _, tc := range []struct {
		profile string
		limit   int
		want    []string
	}{
		{watcher, 1, movies[3:]},
		{watcher, 2, movies[3:]},
		{partial, 2, movies[1:]},
		{other, 2, movies},
	} {
		t.Run(fmt.Sprintf("%s/limit=%d", tc.profile[len(prefix)+1:], tc.limit), func(t *testing.T) {
			access := catalog.AccessFilter{UserID: userID, ProfileID: tc.profile}
			opts := userstore.CollectionItemsPageOptions{Limit: tc.limit}
			var got []string
			for page := 0; page <= len(movies); page++ {
				v, err := h.LibraryCollectionItemsPage(ctx, library, collection.ID, access, opts, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(v.Items) == 0 && v.HasMore {
					t.Fatalf("page %d is empty but has more", page)
				}
				for _, item := range v.Items {
					got = append(got, item.ContentID)
				}
				if !v.HasMore {
					break
				}
				opts.After, opts.Revision = v.Last, v.Revision
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("paged %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLibraryCollectionItemsPageReadsPastALongWatchedRunDB checks that a
// small page of a collection that hides watched titles, such as a three-title
// peek, still finds the unwatched member after a long run of watched ones.
func TestLibraryCollectionItemsPageReadsPastALongWatchedRunDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("hide-watched-run-%d", time.Now().UnixNano())
	var library, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM library_collections WHERE slug LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, library)
	})
	profile := prefix + "-viewer"
	exec(`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'viewer')`, profile, userID)
	repo := catalog.NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{
		LibraryID: library, Slug: prefix, Title: prefix, CollectionType: "manual", Visibility: "visible", HideWatched: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Ten reads of a three-title page's own size would stop at member 30.
	const members = 40
	for i := range members {
		id := fmt.Sprintf("%s-%02d", prefix, i)
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$1,'released','{}',2000)`, id)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
		if err := repo.AddItem(ctx, collection.ID, id, i); err != nil {
			t.Fatal(err)
		}
		if i < members-1 {
			exec(`INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, 5400, 5400, TRUE, NOW())`,
				userID, profile, id)
		}
	}
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(pool), nil)
	h.Executor = &catalog.QueryExecutor{Pool: pool}
	v, err := h.LibraryCollectionItemsPage(ctx, library, collection.ID, catalog.AccessFilter{UserID: userID, ProfileID: profile}, userstore.CollectionItemsPageOptions{Limit: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%s-%02d", prefix, members-1); len(v.Items) != 1 || v.Items[0].ContentID != want || v.HasMore {
		t.Fatalf("first page = %d items (has more %t), want only %s", len(v.Items), v.HasMore, want)
	}
	// A profile that has watched the first member and everything after the
	// fourth fills its page from the widened second read, which also shows
	// nothing visible follows: no continuation to an empty page.
	early := prefix + "-early"
	exec(`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'early')`, early, userID)
	for i := range members {
		if i >= 1 && i <= 3 {
			continue
		}
		exec(`INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, 5400, 5400, TRUE, NOW())`,
			userID, early, fmt.Sprintf("%s-%02d", prefix, i))
	}
	v, err = h.LibraryCollectionItemsPage(ctx, library, collection.ID, catalog.AccessFilter{UserID: userID, ProfileID: early}, userstore.CollectionItemsPageOptions{Limit: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Items) != 3 || v.HasMore {
		t.Fatalf("early profile's page = %d items (has more %t), want 3 and no more", len(v.Items), v.HasMore)
	}
}

// TestHideWatchedCollectionCardCountsTheProfileDB checks that a row of
// collections on Home, whose filter carries no profile, counts a collection
// that hides watched titles for the viewing profile, as its page lists it.
func TestHideWatchedCollectionCardCountsTheProfileDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("hide-watched-card-%d", time.Now().UnixNano())
	var library, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM library_collections WHERE slug LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, library)
	})
	watcher, other := prefix+"-watcher", prefix+"-other"
	exec(`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $3, 'watcher'), ($2, $3, 'other')`, watcher, other, userID)

	repo := catalog.NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{
		LibraryID: library, Slug: prefix, Title: prefix, CollectionType: "manual", Visibility: "visible", HideWatched: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		id := fmt.Sprintf("%s-%d", prefix, i)
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$1,'released','{}',2000)`, id)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
		if err := repo.AddItem(ctx, collection.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, 5400, 5400, TRUE, NOW())`,
		userID, watcher, prefix+"-0")

	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(pool), nil)
	h.FolderRepo = catalog.NewFolderRepository(pool)
	h.Executor = &catalog.QueryExecutor{Pool: pool}
	cards := &CollectionRowCards{Library: h}
	for _, tc := range []struct {
		profile string
		want    int
	}{{watcher, 2}, {other, 3}} {
		got, err := cards.CollectionCards(ctx, []recipes.CollectionRef{{LibraryCollectionID: collection.ID}}, sections.CollectionCardScope{
			UserID: userID, ProfileID: tc.profile, Filter: catalog.AccessFilter{AllowedLibraryIDs: []int{library}}, AdminRow: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ItemCount != tc.want {
			t.Errorf("%s: cards %+v, want one counting %d", tc.profile[len(prefix)+1:], got, tc.want)
		}
	}
}
