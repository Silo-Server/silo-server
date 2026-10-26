package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The /api/v1/catalog/filters/search answer is frozen: every facet returns
// names starting with the prefix, read live from the database and ordered
// by LOWER(name) under the database collation. The prefix is a LIKE
// pattern, so % and _ in it match any text. The queries below keep that
// answer; /api/v2 searches cached value lists instead (facet_values.go).

// searchFacetV1 answers one v1 facet typeahead. facet is already known to
// be a supported name or is rejected by searchNamedFacet.
func searchFacetV1(
	ctx context.Context,
	pool *pgxpool.Pool,
	facet string,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
	prefix string,
	limit int,
) ([]string, bool, error) {
	if column, ok := facetColumns[facet]; ok {
		if column.array {
			return searchDistinctArrayColumnWithSource(ctx, pool, column.name, filters, baseRelation, mediaScope, prefix, limit)
		}
		return searchDistinctScalarColumnWithSource(ctx, pool, column.name, filters, baseRelation, mediaScope, prefix, limit)
	}
	values, hasMore, err := searchNamedFacet(ctx, &pgxFacetFetcher{pool: pool}, facet, filters, baseRelation, mediaScope, strings.TrimSpace(prefix)+"%", limit)
	if err != nil {
		return nil, false, err
	}
	names := make([]string, len(values))
	for i, v := range values {
		names[i] = v.Value
	}
	return names, hasMore, nil
}

// searchDistinctArrayColumnWithSource prefix-searches the distinct values
// of an array column (genres, studios, networks, countries) within the
// scoped result set. Returns up to limit matches in alphabetical order
// plus a hasMore flag (true when the underlying result set held more
// rows than limit).
func searchDistinctArrayColumnWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	column string,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
	prefix string,
	limit int,
) ([]string, bool, error) {
	prefix = strings.TrimSpace(prefix)
	if limit <= 0 {
		return []string{}, false, nil
	}
	fromClause, whereClause, args, empty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if empty {
		return []string{}, false, nil
	}
	args = append(args, prefix+"%")
	prefixIdx := len(args)
	// LOWER() in ORDER BY: this query is already wrapped in a subquery
	// (the DISTINCT UNNEST), so the outer ORDER BY can reference any
	// expression freely.
	query := fmt.Sprintf(`
		SELECT name FROM (
			SELECT DISTINCT UNNEST(mi.%s) AS name
			FROM %s
			%s
		) vals
		WHERE name IS NOT NULL
		  AND BTRIM(name) <> ''
		  AND LOWER(name) LIKE LOWER($%d)
		ORDER BY LOWER(name) ASC
		LIMIT %d
	`, column, fromClause, whereClause, prefixIdx, limit+1)
	return queryFacetSearchResults(ctx, pool, query, args, limit)
}

// searchDistinctScalarColumnWithSource is the scalar (e.g. content_rating)
// variant of searchDistinctArrayColumnWithSource.
func searchDistinctScalarColumnWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	column string,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
	prefix string,
	limit int,
) ([]string, bool, error) {
	prefix = strings.TrimSpace(prefix)
	if limit <= 0 {
		return []string{}, false, nil
	}
	fromClause, whereClause, args, empty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if empty {
		return []string{}, false, nil
	}
	args = append(args, prefix+"%")
	prefixIdx := len(args)
	// DISTINCT in an inline subquery so the outer ORDER BY can apply
	// LOWER() without violating the SELECT DISTINCT rule.
	query := fmt.Sprintf(`
		SELECT name FROM (
			SELECT DISTINCT mi.%s AS name
			FROM %s
			%s
			  AND mi.%s IS NOT NULL
			  AND BTRIM(mi.%s) <> ''
			  AND LOWER(mi.%s) LIKE LOWER($%d)
		) matches
		ORDER BY LOWER(name) ASC
		LIMIT %d
	`, column, fromClause, browseFilterPrefix(whereClause), column, column, column, prefixIdx, limit+1)
	return queryFacetSearchResults(ctx, pool, query, args, limit)
}

// queryFacetSearchResults executes a facet search query that was built
// with LIMIT N+1 and returns the first N rows plus a hasMore flag
// indicating whether an additional row was present (i.e. the result set
// exceeded the requested limit).
func queryFacetSearchResults(ctx context.Context, pool *pgxpool.Pool, query string, args []any, limit int) ([]string, bool, error) {
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	values := make([]string, 0, limit)
	hasMore := false
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, false, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(values) >= limit {
			hasMore = true
			continue
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return values, hasMore, nil
}
