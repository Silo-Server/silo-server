package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestServerCollectionCountsFollowTheViewerDB pins what a server collection
// shows a viewer whose access narrows the catalog: its count is the members
// that viewer can open, and a collection whose every member it hides is not
// listed, nor is one whose count can't be read. An unrestricted viewer keeps
// the stored count, and a collection that is empty for everyone stays listed
// (#193 S4).
func TestServerCollectionCountsFollowTheViewerDB(t *testing.T) {
	f := seedViewerCountCollections(t)
	h := NewLibraryCollectionHandler(f.repo, nil, catalog.NewItemRepository(f.pool), nil)
	h.FolderRepo = catalog.NewFolderRepository(f.pool)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}

	pgViewer := access.Scope{AllowedLibraryIDs: []int{f.shown}, LibrariesRestricted: true, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}
	for _, tc := range []struct {
		name  string
		scope *access.Scope
		want  map[string]int
	}{
		{"unrestricted", nil, map[string]int{f.ids["hidden"]: 2, f.ids["mixed"]: 3, f.ids["empty"]: 0, f.ids["chapter"]: 1, f.ids["smart"]: 4, f.ids["broken"]: 4, f.ids["uncounted"]: 0, f.ids["emptied"]: 5}},
		{"rating and library limited", &pgViewer, map[string]int{f.ids["mixed"]: 1, f.ids["empty"]: 0, f.ids["smart"]: 2, f.ids["emptied"]: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.scope != nil {
				ctx = access.SetScope(ctx, *tc.scope)
			}
			server, err := h.ListServerCollections(ctx, AccessFilterFromContext(ctx, ""))
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for _, lib := range server.Libraries {
				if lib.LibraryID != f.shown {
					continue
				}
				for _, c := range lib.Collections {
					got[c.ID] = c.ItemCount
				}
				if lib.TotalCount != len(lib.Collections) {
					t.Errorf("server collections total %d, lists %d", lib.TotalCount, len(lib.Collections))
				}
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("server collections list %v, want %v", got, tc.want)
			}

			tab, err := h.LibraryCollectionsTab(ctx, f.shown, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			got = map[string]int{}
			for _, c := range tab.Collections {
				got[c.ID] = c.ItemCount
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("library tab lists %v, want %v", got, tc.want)
			}
		})
	}
}

type viewerCountCollections struct {
	pool  *pgxpool.Pool
	repo  *catalog.LibraryCollectionRepository
	shown int
	// ids maps each seeded collection's name to its ID.
	ids map[string]string
}

// seedViewerCountCollections seeds two movie libraries, titles rated 7 and 17
// and a manga chapter, and server collections scoped to the first: curated
// ones holding only 17-rated titles, also spanning the second library, empty,
// and holding only the chapter; and smart ones matching every movie, whose
// saved rule no longer resolves, whose cached count predates its matches, and
// whose cached count predates it matching nothing.
func seedViewerCountCollections(t *testing.T) viewerCountCollections {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := fmt.Sprintf("viewer-counts-%d", time.Now().UnixNano())
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}

	var shown, other int
	for i, target := range []*int{&shown, &other} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("%s-%d", prefix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM library_collections WHERE slug LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id=ANY($1)`, []int{shown, other})
	})
	item := func(name string, age, library int) string {
		id := prefix + "-" + name
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) VALUES($1,'movie',$1,'released','{}','x',$2)`, id, age)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
		return id
	}
	mature1, mature2 := item("mature1", 17, shown), item("mature2", 17, shown)
	family := item("family", 7, shown)
	elsewhere := item("elsewhere", 7, other)
	// A manga chapter, which a collection's catalog page never lists.
	series, chapter := item("series", 7, shown), item("chapter", 7, shown)
	exec(`INSERT INTO manga_chapters(chapter_content_id,series_content_id) VALUES($1,$2)`, chapter, series)

	f := viewerCountCollections{pool: pool, repo: catalog.NewLibraryCollectionRepository(pool), shown: shown, ids: map[string]string{}}
	create := func(name, kind string, libraries []int, query string, members ...string) {
		t.Helper()
		input := catalog.CreateLibraryCollectionInput{LibraryID: libraries[0], LibraryIDs: libraries, Slug: prefix + "-" + name, Title: name, CollectionType: kind, Visibility: "visible"}
		if query != "" {
			input.QueryDefinition = json.RawMessage(query)
		}
		c, err := f.repo.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		for i, id := range members {
			if err := f.repo.AddItem(ctx, c.ID, id, i); err != nil {
				t.Fatal(err)
			}
		}
		f.ids[name] = c.ID
	}
	everyMovie := fmt.Sprintf(`{"library_ids":[%d],"media_scope":"movie","match":"all","groups":[]}`, shown)
	create("hidden", "manual", []int{shown}, "", mature1, mature2)
	create("mixed", "manual", []int{shown, other}, "", mature1, family, elsewhere)
	create("empty", "manual", []int{shown}, "")
	create("chapter", "manual", []int{shown}, "", chapter)
	create("smart", "smart", []int{shown}, everyMovie)
	create("broken", "smart", []int{shown}, everyMovie)
	// Matches only the title in the other library, but its cached count is
	// still the 0 of a collection the refresher hasn't reached.
	create("uncounted", "smart", []int{shown, other}, fmt.Sprintf(`{"library_ids":[%d],"media_scope":"movie","match":"all","groups":[]}`, other))
	// Matches nothing now, but its cached count is from before.
	create("emptied", "smart", []int{shown}, fmt.Sprintf(`{"library_ids":[%d],"media_scope":"movie","match":"all","groups":[{"match":"all","rules":[{"field":"genre","op":"is","value":"%s-none"}]}]}`, shown, prefix))
	// The smart-count refresher counts with no access filter.
	exec(`UPDATE library_collections SET item_count_cached=4 WHERE id=$1`, f.ids["smart"])
	exec(`UPDATE library_collections SET item_count_cached=5 WHERE id=$1`, f.ids["emptied"])
	exec(`UPDATE library_collections SET item_count_cached=4, query_definition='{"groups":[{"match":"all","rules":[{"field":"no_such_field","op":"is","value":"x"}]}]}' WHERE id=$1`, f.ids["broken"])
	return f
}
