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

// namePrefixSearchFixture is one isolated set of titles sharing a random token.
type namePrefixSearchFixture struct {
	token, movie, series, episode string
	echoes                        []string
	folder                        int
	kinds                         map[string]string
	// byPrefix maps each name_prefix to the IDs a q=<token> search returns.
	byPrefix map[string][]string
}

func seedNamePrefixSearchFixture(t *testing.T, pool *pgxpool.Pool) namePrefixSearchFixture {
	t.Helper()
	ctx := t.Context()
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
	f := namePrefixSearchFixture{token: token, movie: token + "-movie", series: token + "-series", episode: token + "-episode"}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, token).Scan(&f.folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, token+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, f.folder)
	})
	exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$2,'matched','{Drama}',2001)`, f.movie, token+" Arrival")
	// The series files under its sort title, so "<token> c" must not match it.
	exec(`INSERT INTO media_items(content_id,type,title,sort_title,status,genres,year) VALUES($1,'series',$2,$3,'matched','{Drama}',2001)`, f.series, token+" Chronicles", token+" Dawn")
	exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,1,$3)`, f.episode, f.series, token+" Begins")
	exec(`INSERT INTO episode_libraries(episode_id,media_folder_id,first_seen_at) VALUES($1,$2,NOW())`, f.episode, f.folder)
	// Matches only by overview, which title hits suppress, so no prefix may
	// surface it either.
	overviewOnly := token + "-overview"
	exec(`INSERT INTO media_items(content_id,type,title,overview,status,genres,year) VALUES($1,'movie','Quiet Harbor',$2,'matched','{Drama}',2001)`, overviewOnly, strings.Repeat(token+" ", 3))
	exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$4),($2,$4),($3,$4)`, f.movie, f.series, overviewOnly, f.folder)
	// Five or more title hits keep the search on its FTS path; fewer switch it
	// to the combined FTS-plus-fuzzy path.
	f.kinds = map[string]string{f.movie: "movie", f.series: "series", f.episode: "episode"}
	for i := range 4 {
		id := fmt.Sprintf("%s-echo-%d", token, i)
		f.echoes = append(f.echoes, id)
		f.kinds[id] = "movie"
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$2,'matched','{Drama}',2001)`, id, fmt.Sprintf("%s Echo %d", token, i))
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, f.folder)
	}
	f.byPrefix = map[string][]string{
		token + " a": {f.movie},
		token + " b": {f.episode},
		token + " c": nil,
		token + " d": {f.series},
		"zzz":        nil,
		"quiet":      nil,
		token + " e": f.echoes,
		token:        append([]string{f.movie, f.series, f.episode}, f.echoes...),
	}
	return f
}

// namePrefixSearchResolvers returns a PostgreSQL resolver and a Meilisearch
// one whose index fails the test if contacted: Meilisearch cannot apply the
// prefix before its capped ranking window, so a prefixed search must reach
// PostgreSQL instead.
func namePrefixSearchResolvers(t *testing.T, pool *pgxpool.Pool) map[string]*CatalogResolver {
	t.Helper()
	meili := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Meilisearch contacted for a prefixed search: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(meili.Close)
	meiliProvider, err := NewMeilisearchSearchProvider(NewItemRepository(pool), nil, nil, MeilisearchProviderConfig{URL: meili.URL, IndexTypes: []string{"movie", "series", "episode"}})
	if err != nil {
		t.Fatal(err)
	}
	meiliProvider.stateRepo = fakeMeilisearchIndexStateStore{state: SearchIndexState{
		ActiveIndexUID: "name_prefix_test",
		SchemaVersion:  catalogSearchMeilisearchSchemaVersion(meiliProvider.config.Embedder, meiliProvider.config.IndexTypes, false, false),
	}}
	return map[string]*CatalogResolver{
		"postgres":    NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)),
		"meilisearch": NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithSearchProvider(meiliProvider),
	}
}

// namePrefixSearchRequests lists relevance and title sort, no type and each
// single type, with and without a genre rule. A filter rule adds a definition
// predicate to matching.
func namePrefixSearchRequests(token string) []url.Values {
	var requests []url.Values
	for _, sort := range []string{"", "title"} {
		for _, kind := range []string{"", "movie", "series", "episode"} {
			for _, genre := range []string{"", "Drama"} {
				values := url.Values{"q": {token}}
				for key, value := range map[string]string{"sort": sort, "type": kind, "genre": genre} {
					if value != "" {
						values.Set(key, value)
					}
				}
				requests = append(requests, values)
			}
		}
	}
	return requests
}

func resolveNamePrefixSearch(t *testing.T, resolver *CatalogResolver, values url.Values, access AccessFilter) ([]string, bool) {
	t.Helper()
	req, err := ParseCatalogRequest(values)
	if err != nil {
		t.Fatalf("%s: parse: %v", values.Encode(), err)
	}
	req.CursorPaging = true
	req.Limit = 50
	res, err := resolver.Resolve(t.Context(), req, access)
	if err != nil {
		t.Fatalf("%s: resolve: %v", values.Encode(), err)
	}
	var ids []string
	for _, item := range res.Items {
		ids = append(ids, item.ContentID)
	}
	return ids, res.HasMore
}

// name_prefix narrows every text search the way it narrows browse: relevance
// and sorted, with and without a single-kind type, on the PostgreSQL provider.
func TestSearchNamePrefixNarrowsTextSearchDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	f := seedNamePrefixSearchFixture(t, pool)

	// A library restriction moves episode admission inside a planning fence.
	accesses := map[string]AccessFilter{"unrestricted": {}, "library": {AllowedLibraryIDs: []int{f.folder}}}
	for providerName, resolver := range namePrefixSearchResolvers(t, pool) {
		for accessName, access := range accesses {
			for _, base := range namePrefixSearchRequests(f.token) {
				for prefix, matches := range f.byPrefix {
					values := url.Values{"name_prefix": {prefix}}
					for key := range base {
						values.Set(key, base.Get(key))
					}
					name := providerName + " " + accessName + " " + values.Encode()
					got, hasMore := resolveNamePrefixSearch(t, resolver, values, access)
					kind := values.Get("type")
					want := slices.DeleteFunc(slices.Clone(matches), func(id string) bool { return kind != "" && f.kinds[id] != kind })
					slices.Sort(got)
					slices.Sort(want)
					if !slices.Equal(got, want) {
						t.Errorf("%s: got %v, want %v", name, got, want)
					}
					if hasMore {
						t.Errorf("%s: HasMore set on a complete page", name)
					}
				}
			}
		}
	}

	// A single title hit takes the combined FTS-plus-fuzzy path.
	postgres := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	for prefix, want := range map[string][]string{"": {f.episode}, f.token + " b": {f.episode}, f.token + " a": nil, "zzz": nil} {
		got, _ := resolveNamePrefixSearch(t, postgres, url.Values{"q": {f.token + " begins"}, "name_prefix": {prefix}}, AccessFilter{})
		if !slices.Equal(got, want) {
			t.Errorf("combined prefix=%q: got %v, want %v", prefix, got, want)
		}
	}

	// Sorted mixed searches and Meilisearch ranking windows both hand
	// searchCandidatesExecutor a list of candidate IDs; the prefix narrows it.
	repo := NewItemRepository(pool)
	ids := append([]string{f.movie, f.series, f.episode}, f.echoes...)
	for prefix, want := range f.byPrefix {
		executor, outer, outerAccess, err := repo.searchCandidatesExecutor(QueryDefinition{Sort: QuerySort{Field: querySortTitle, Order: querySortAsc}},
			AccessFilter{NamePrefix: prefix}, "SELECT unnest($1::text[])", []any{ids})
		if err != nil {
			t.Fatal(err)
		}
		page, err := executor.PreviewCursorPage(t.Context(), outer, outerAccess, 50, nil, false)
		if err != nil {
			t.Fatalf("window prefix=%q: %v", prefix, err)
		}
		var got []string
		for _, item := range page.Items {
			got = append(got, item.ContentID)
		}
		slices.Sort(got)
		if want = slices.Sorted(slices.Values(want)); !slices.Equal(got, want) {
			t.Errorf("window prefix=%q: got %v, want %v", prefix, got, want)
		}
	}
}
