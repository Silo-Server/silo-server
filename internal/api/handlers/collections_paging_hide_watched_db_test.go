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
