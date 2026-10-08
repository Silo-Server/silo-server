package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The v1 facet typeahead is frozen: it must answer exactly what the query
// it shipped with answers, under the database's own collation, with LIKE
// wildcards in the prefix and names trimmed only after DISTINCT.
func TestSearchFacetV1AnswersTheFrozenQuery(t *testing.T) {
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
	sfx := fmt.Sprintf("v1f%d", time.Now().UnixNano())
	var lib int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, sfx).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	var ids []string
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, lib)
	})
	g := func(name string) string { return sfx + " " + name }
	for i, genres := range [][]string{
		{g("Science Fiction"), g("Sci-Fi"), g("Comédie")},
		{g("Comedy"), g("Comic"), g("Drama") + " "},
		{g("Drama"), g("100% Fun"), g("100x Fun")},
		{g("Ab_c"), g("Abxc")},
	} {
		id := fmt.Sprintf("%s-%d", sfx, i)
		ids = append(ids, id)
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'matched', $2)`, id, genres); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, lib); err != nil {
			t.Fatal(err)
		}
	}

	// The query /api/v1/catalog/filters/search ran for array facets before
	// the v2 typeahead got its cached value lists.
	frozen := func(t *testing.T, prefix string, limit int) ([]string, bool) {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT name FROM (
				SELECT DISTINCT UNNEST(mi.genres) AS name
				FROM media_items mi
				JOIN media_item_libraries mil ON mil.content_id = mi.content_id AND mil.media_folder_id = $1
			) vals
			WHERE name IS NOT NULL
			  AND BTRIM(name) <> ''
			  AND LOWER(name) LIKE LOWER($2)
			ORDER BY LOWER(name) ASC
			LIMIT $3`, lib, strings.TrimSpace(prefix)+"%", limit+1)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, strings.TrimSpace(name))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(names) > limit {
			return names[:limit], true
		}
		return names, false
	}

	resolver := NewCatalogResolver(NewBrowseRepository(pool), nil)
	req := CatalogRequest{Source: CatalogSourceQuery, Query: QueryDefinition{LibraryIDs: []int{lib}}}
	for _, tc := range []struct {
		prefix string
		limit  int
		// want pins what holds under any collation; the frozen query
		// decides the order. No two seeded names tie on LOWER(name), so
		// that order is total.
		want []string
	}{
		{prefix: g("sci"), limit: 10, want: []string{g("Science Fiction"), g("Sci-Fi")}},
		{prefix: g("sci"), limit: 1},
		{prefix: g("com"), limit: 10, want: []string{g("Comédie"), g("Comedy"), g("Comic")}},
		{prefix: g("dra"), limit: 10, want: []string{g("Drama"), g("Drama")}},
		{prefix: g("100%"), limit: 10, want: []string{g("100% Fun"), g("100x Fun")}},
		{prefix: g("ab_c"), limit: 10, want: []string{g("Ab_c"), g("Abxc")}},
		{prefix: "  " + g("c") + "  ", limit: 2},
	} {
		got, hasMore, err := resolver.SearchFacetV1(ctx, req, AccessFilter{}, "genre", tc.prefix, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		want, wantMore := frozen(t, tc.prefix, tc.limit)
		if !slices.Equal(got, want) || hasMore != wantMore {
			t.Errorf("q=%q limit=%d: got %q more=%v, the frozen query answers %q more=%v", tc.prefix, tc.limit, got, hasMore, want, wantMore)
		}
		if tc.want != nil && !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(tc.want))) {
			t.Errorf("q=%q: got %q, want the names %q", tc.prefix, got, tc.want)
		}
	}

	if got, more, err := resolver.SearchFacetV1(ctx, req, AccessFilter{}, "genre", "  ", 10); err != nil || got == nil || len(got) != 0 || more {
		t.Errorf("blank prefix = %q more=%v err=%v, want an empty list", got, more, err)
	}
}
