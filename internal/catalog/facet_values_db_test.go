package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedFacetLibraries makes two libraries with titles whose studios and
// content ratings carry the run's suffix, and returns their ids.
func seedFacetLibraries(t *testing.T) (*pgxpool.Pool, string, int, int) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	sfx := fmt.Sprintf("%d", time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	var libA, libB int
	for _, lib := range []*int{&libA, &libB} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, "facet-"+sfx).Scan(lib); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{libA, libB})
	})
	s := func(name string) string { return name + " " + sfx }
	for i, it := range []struct {
		lib     int
		studios []string
		rating  string
	}{
		{libA, []string{s("Warner Bros."), s("Time Warner")}, s("PG")},
		{libA, []string{s("Warner Bros."), "  " + s("Trim Co") + " "}, s("PG")},
		{libA, []string{s("Trim Co"), s("100% Studios"), ""}, s("R")},
		{libA, []string{s("1000 Studios"), s("aXb Films")}, ""},
		{libB, []string{s("Only In B")}, s("R")},
	} {
		id := fmt.Sprintf("facet-%s-%d", sfx, i)
		ids = append(ids, id)
		exec(`INSERT INTO media_items (content_id, type, title, status, studios, content_rating) VALUES ($1, 'movie', $1, 'matched', $2, $3)`, id, it.studios, it.rating)
		exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, it.lib)
	}
	return pool, sfx, libA, libB
}

func TestSearchColumnFacetDB(t *testing.T) {
	pool, sfx, libA, libB := seedFacetLibraries(t)
	ctx := t.Context()
	studios := facetColumns[facetStudio]
	cache := newFacetValueCache(time.Minute, facetValueCacheBudget)
	search := func(t *testing.T, cache *facetValueCache, column facetColumn, libs []int, s facetSearch) ([]FacetValue, bool) {
		t.Helper()
		got, hasMore, err := searchColumnFacet(ctx, pool, cache, column, BrowseFilters{LibraryIDs: libs}, "media_items mi", "", s)
		if err != nil {
			t.Fatal(err)
		}
		return got, hasMore
	}

	t.Run("counts trimmed values per scope", func(t *testing.T) {
		got, hasMore := search(t, cache, studios, []int{libA}, facetSearch{Limit: 10})
		want := []FacetValue{
			{Value: "Trim Co " + sfx, Count: 2},
			{Value: "Warner Bros. " + sfx, Count: 2},
			{Value: "100% Studios " + sfx, Count: 1},
			{Value: "1000 Studios " + sfx, Count: 1},
			{Value: "aXb Films " + sfx, Count: 1},
			{Value: "Time Warner " + sfx, Count: 1},
		}
		if !slices.Equal(got, want) || hasMore {
			t.Fatalf("values = %v hasMore=%v, want %v", got, hasMore, want)
		}
	})

	t.Run("ranks prefix then word-start matches", func(t *testing.T) {
		got, _ := search(t, cache, studios, []int{libA}, facetSearch{Q: "war", Limit: 10})
		if names := facetValueNames(got); !slices.Equal(names, []string{"Warner Bros. " + sfx, "Time Warner " + sfx}) {
			t.Fatalf("names = %v", names)
		}
	})

	t.Run("scopes do not share a list", func(t *testing.T) {
		got, _ := search(t, cache, studios, []int{libB}, facetSearch{Limit: 10})
		if names := facetValueNames(got); !slices.Equal(names, []string{"Only In B " + sfx}) {
			t.Fatalf("library B values = %v", names)
		}
	})

	t.Run("scalar column", func(t *testing.T) {
		got, _ := search(t, cache, facetColumns[facetContentRating], []int{libA, libB}, facetSearch{Q: "", Limit: 10})
		want := []FacetValue{{Value: "PG " + sfx, Count: 2}, {Value: "R " + sfx, Count: 2}}
		if !slices.Equal(got, want) {
			t.Fatalf("ratings = %v, want %v", got, want)
		}
	})

	t.Run("v2 keeps prefix matches beside ranked values", func(t *testing.T) {
		resolver := NewCatalogResolver(NewBrowseRepository(pool), nil)
		req := CatalogRequest{Source: CatalogSourceQuery, Query: QueryDefinition{LibraryIDs: []int{libA}}}
		got, err := resolver.SearchFacet(ctx, req, AccessFilter{}, "studio", "war", 10)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Matches, []string{"Warner Bros. " + sfx}) || got.HasMore {
			t.Errorf("matches = %v hasMore=%v, want the prefix match only", got.Matches, got.HasMore)
		}
		if names := facetValueNames(got.Values); !slices.Equal(names, []string{"Warner Bros. " + sfx, "Time Warner " + sfx}) {
			t.Errorf("values = %v", names)
		}
		got, err = resolver.SearchFacet(ctx, req, AccessFilter{}, "studio", "t", 1)
		if err != nil {
			t.Fatal(err)
		}
		// A-Z, not by title count: "Time Warner" (1 title) before "Trim
		// Co" (2 titles).
		if !slices.Equal(got.Matches, []string{"Time Warner " + sfx}) || !got.HasMore {
			t.Errorf("matches = %v hasMore=%v, want Time Warner and more", got.Matches, got.HasMore)
		}
		if names := facetValueNames(got.Values); !slices.Equal(names, []string{"Trim Co " + sfx}) || !got.ValuesHasMore {
			t.Errorf("values = %v valuesHasMore=%v, want Trim Co and more", names, got.ValuesHasMore)
		}
	})

	t.Run("over-budget fallback escapes the query", func(t *testing.T) {
		tiny := newFacetValueCache(time.Minute, 1)
		for _, tc := range []struct {
			s    facetSearch
			want []FacetValue
		}{
			{facetSearch{Q: "100%", Limit: 10}, []FacetValue{{Value: "100% Studios " + sfx, Count: 1}}},
			{facetSearch{Q: "a_b", Limit: 10}, []FacetValue{}},
			{facetSearch{Q: "war", Limit: 10}, []FacetValue{{Value: "Warner Bros. " + sfx, Count: 2}, {Value: "Time Warner " + sfx, Count: 1}}},
			{facetSearch{Q: "bros. " + sfx[:3], Limit: 10}, []FacetValue{{Value: "Warner Bros. " + sfx, Count: 2}}},
			{facetSearch{Q: "", Limit: 2}, []FacetValue{{Value: "Trim Co " + sfx, Count: 2}, {Value: "Warner Bros. " + sfx, Count: 2}}},
			{facetSearch{Q: "1", Limit: 10, Mode: facetSearchPrefix}, []FacetValue{{Value: "100% Studios " + sfx, Count: 1}, {Value: "1000 Studios " + sfx, Count: 1}}},
		} {
			got, _ := search(t, tiny, studios, []int{libA}, tc.s)
			if !slices.Equal(got, tc.want) {
				t.Errorf("%+v: got %v, want %v", tc.s, got, tc.want)
			}
		}
		// Only the oversize marker is cached, so later keystrokes skip the
		// list build.
		if len(tiny.entries) != 1 || !tiny.lru.next.list.oversize {
			t.Errorf("cache holds %d entries; want just the oversize marker", len(tiny.entries))
		}
	})
}
