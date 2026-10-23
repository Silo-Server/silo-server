package catalog

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// name_prefix narrows every text search the way it narrows browse: relevance
// and sorted, with and without a single-kind type, on the PostgreSQL provider.
func TestSearchNamePrefixNarrowsTextSearchDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	letters := make([]byte, 8)
	for i := range letters {
		letters[i] = byte('a' + rand.IntN(26))
	}
	token := "zq" + string(letters)
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, token).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, token+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder)
	}()
	movie, series, episode := token+"-movie", token+"-series", token+"-episode"
	exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$2,'matched','{Drama}',2001)`, movie, token+" Arrival")
	// The series files under its sort title, so "<token> c" must not match it.
	exec(`INSERT INTO media_items(content_id,type,title,sort_title,status,genres,year) VALUES($1,'series',$2,$3,'matched','{Drama}',2001)`, series, token+" Chronicles", token+" Dawn")
	exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,1,$3)`, episode, series, token+" Begins")
	exec(`INSERT INTO episode_libraries(episode_id,media_folder_id,first_seen_at) VALUES($1,$2,NOW())`, episode, folder)
	// Matches only by overview, which title hits suppress, so no prefix may
	// surface it either.
	overviewOnly := token + "-overview"
	exec(`INSERT INTO media_items(content_id,type,title,overview,status,genres,year) VALUES($1,'movie','Quiet Harbor',$2,'matched','{Drama}',2001)`, overviewOnly, strings.Repeat(token+" ", 3))
	exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$4),($2,$4),($3,$4)`, movie, series, overviewOnly, folder)
	// Five or more title hits keep the search on its FTS path; fewer switch it
	// to the combined FTS-plus-fuzzy path.
	var echoes []string
	for i := range 4 {
		id := fmt.Sprintf("%s-echo-%d", token, i)
		echoes = append(echoes, id)
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$2,'matched','{Drama}',2001)`, id, fmt.Sprintf("%s Echo %d", token, i))
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, folder)
	}

	// Meilisearch cannot apply the prefix before its capped ranking window, so a
	// prefixed search must reach PostgreSQL without contacting it.
	meili := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Meilisearch contacted for a prefixed search: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer meili.Close()
	meiliProvider, err := NewMeilisearchSearchProvider(NewItemRepository(pool), nil, nil, MeilisearchProviderConfig{URL: meili.URL, IndexTypes: []string{"movie", "series", "episode"}})
	if err != nil {
		t.Fatal(err)
	}
	meiliProvider.stateRepo = fakeMeilisearchIndexStateStore{state: SearchIndexState{
		ActiveIndexUID: "name_prefix_test",
		SchemaVersion:  catalogSearchMeilisearchSchemaVersion(meiliProvider.config.Embedder, meiliProvider.config.IndexTypes, false, false),
	}}
	resolvers := map[string]*CatalogResolver{
		"postgres":    NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)),
		"meilisearch": NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithSearchProvider(meiliProvider),
	}
	byPrefix := map[string][]string{
		token + " a": {movie},
		token + " b": {episode},
		token + " c": nil,
		token + " d": {series},
		"zzz":        nil,
		"quiet":      nil,
		token + " e": echoes,
		token:        append([]string{movie, series, episode}, echoes...),
	}
	kinds := map[string]string{movie: "movie", series: "series", episode: "episode"}
	for _, id := range echoes {
		kinds[id] = "movie"
	}
	// A library restriction moves episode admission inside a planning fence.
	accesses := map[string]AccessFilter{"unrestricted": {}, "library": {AllowedLibraryIDs: []int{folder}}}
	for providerName, resolver := range resolvers {
		for accessName, access := range accesses {
			for _, sort := range []string{"", "title"} {
				for _, kind := range []string{"", "movie", "series", "episode"} {
					for _, genre := range []string{"", "Drama"} {
						for prefix, matches := range byPrefix {
							name := fmt.Sprintf("%s %s sort=%q type=%q genre=%q prefix=%q", providerName, accessName, sort, kind, genre, prefix)
							values := url.Values{"q": {token}, "name_prefix": {prefix}}
							if sort != "" {
								values.Set("sort", sort)
							}
							if kind != "" {
								values.Set("type", kind)
							}
							// A filter rule adds a definition predicate to matching.
							if genre != "" {
								values.Set("genre", genre)
							}
							req, err := ParseCatalogRequest(values)
							if err != nil {
								t.Fatalf("%s: parse: %v", name, err)
							}
							req.CursorPaging = true
							req.Limit = 50
							res, err := resolver.Resolve(ctx, req, access)
							if err != nil {
								t.Fatalf("%s: resolve: %v", name, err)
							}
							var got, want []string
							for _, item := range res.Items {
								got = append(got, item.ContentID)
							}
							for _, id := range matches {
								if kind == "" || kinds[id] == kind {
									want = append(want, id)
								}
							}
							slices.Sort(got)
							slices.Sort(want)
							if !slices.Equal(got, want) {
								t.Errorf("%s: got %v, want %v", name, got, want)
							}
							if res.HasMore {
								t.Errorf("%s: HasMore set on a complete page", name)
							}
						}
					}
				}
			}
		}
	}

	// A single title hit takes the combined FTS-plus-fuzzy path.
	for prefix, want := range map[string][]string{"": {episode}, token + " b": {episode}, token + " a": nil, "zzz": nil} {
		req, err := ParseCatalogRequest(url.Values{"q": {token + " begins"}, "name_prefix": {prefix}})
		if err != nil {
			t.Fatal(err)
		}
		req.CursorPaging = true
		req.Limit = 50
		res, err := resolvers["postgres"].Resolve(ctx, req, AccessFilter{})
		if err != nil {
			t.Fatalf("combined prefix=%q: %v", prefix, err)
		}
		var got []string
		for _, item := range res.Items {
			got = append(got, item.ContentID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("combined prefix=%q: got %v, want %v", prefix, got, want)
		}
	}

	// Sorted mixed searches and Meilisearch ranking windows both hand
	// searchCandidatesExecutor a list of candidate IDs; the prefix narrows it.
	repo := NewItemRepository(pool)
	ids := append([]string{movie, series, episode}, echoes...)
	for prefix, want := range byPrefix {
		executor, outer, outerAccess, err := repo.searchCandidatesExecutor(QueryDefinition{Sort: QuerySort{Field: querySortTitle, Order: querySortAsc}},
			AccessFilter{NamePrefix: prefix}, "SELECT unnest($1::text[])", []any{ids})
		if err != nil {
			t.Fatal(err)
		}
		page, err := executor.PreviewCursorPage(ctx, outer, outerAccess, 50, nil, false)
		if err != nil {
			t.Fatalf("window prefix=%q: %v", prefix, err)
		}
		var got []string
		for _, item := range page.Items {
			got = append(got, item.ContentID)
		}
		slices.Sort(got)
		want = slices.Sorted(slices.Values(want))
		if !slices.Equal(got, want) {
			t.Errorf("window prefix=%q: got %v, want %v", prefix, got, want)
		}
	}
}
