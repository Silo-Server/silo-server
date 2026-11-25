package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestHideWatchedCollectionRowsArePerProfileDB checks that a Home row showing
// a server collection that hides watched titles leaves out what the viewing
// profile has watched, for every kind of collection, and never serves one
// profile's row to another through the shared row cache.
func TestHideWatchedCollectionRowsArePerProfileDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	resetResolvedListCacheForTest()
	t.Cleanup(resetResolvedListCacheForTest)
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	prefix := fmt.Sprintf("hide-watched-rail-%d", time.Now().UnixNano())
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
	var movies []string
	for i, title := range []string{"Alpha", "Bravo", "Charlie"} {
		id := fmt.Sprintf("%s-%d", prefix, i)
		movies = append(movies, id)
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$2,'released','{}',2000)`, id, title)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
	}
	exec(`INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, 5400, 5400, TRUE, NOW())`,
		userID, watcher, movies[0])

	repo := catalog.NewLibraryCollectionRepository(pool)
	fetcher := NewFetcher(pool)
	fetcher.CollectionRepo = repo
	fetcher.StoreProvider = pgstore.NewPostgresProvider(pool)
	viewer := catalog.AccessFilter{AllowedLibraryIDs: []int{library}}

	for _, tc := range []struct {
		name  string
		input catalog.CreateLibraryCollectionInput
	}{
		{"manual", catalog.CreateLibraryCollectionInput{CollectionType: "manual"}},
		{"manual with a default sort", catalog.CreateLibraryCollectionInput{CollectionType: "manual", SortConfig: json.RawMessage(`{"field":"title","order":"asc"}`)}},
		{"smart", catalog.CreateLibraryCollectionInput{CollectionType: "smart", QueryDefinition: json.RawMessage(`{"media_scope":"movie","sort":{"field":"title","order":"asc"}}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.input
			input.LibraryID, input.Title, input.Visibility, input.HideWatched = library, tc.name, "visible", true
			input.Slug = fmt.Sprintf("%s-%d", prefix, len(tc.name))
			collection, err := repo.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if input.CollectionType == "manual" {
				for pos, id := range movies {
					if err := repo.AddItem(ctx, collection.ID, id, pos); err != nil {
						t.Fatal(err)
					}
				}
			}
			section := ResolvedSection{
				ID:          prefix + "-" + tc.name,
				SectionType: SectionCollection,
				Title:       "Row",
				ItemLimit:   10,
				Config:      json.RawMessage(fmt.Sprintf(`{"library_collection_id":%q}`, collection.ID)),
			}
			// The watcher reads first, so a shared cache entry would hand its
			// row to the other profile, and the other's back to the watcher.
			for _, read := range []struct {
				profile string
				want    []string
			}{
				{watcher, movies[1:]},
				{other, movies},
				{watcher, movies[1:]},
			} {
				got, err := fetcher.FetchOne(ctx, section, nil, nil, userID, read.profile, viewer)
				if err != nil {
					t.Fatalf("fetch row: %v", err)
				}
				assertContentIDs(t, read.profile+" row", railContentIDs(got.Items), read.want...)
			}
		})
	}
}
