package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// BrowseFavoritesFilters describes the filter, sort, and pagination inputs for
// a favorites-scoped browse query. The query JOINs user_favorites with
// media_items so that type/library/sort filters and total counts can resolve
// in a single round trip without first fetching every favorite into memory
// (audit 2026-05-01 §3.6 / catalog SQL plan task 4.2).
type BrowseFavoritesFilters struct {
	UserID             int
	ProfileID          string
	ItemType           string // single ("movie") or comma-separated ("movie,series")
	Genre              string // single genre filter (matches mi.genres array)
	NamePrefix         string // case-insensitive prefix on sort_title/title
	LibraryID          int    // restrict to a single specific library (parentLibraryID)
	AllowedLibraryIDs  []int  // nil = no allowlist, []int{} = empty result
	DisabledLibraryIDs []int  // user-disabled libraries to exclude
	MaxContentRating   string
	ExcludedMediaTypes []string // media types the caller's surface never exposes
	SortField          string   // "added_at" (default), "title"/"sort_title", "year", "release_date"
	SortOrder          string   // "asc" or "desc" (default desc)
	Limit              int
	Offset             int
}

// errBrowseFavoritesEmpty is the sentinel returned by buildBrowseFavoritesPlan
// when the filter set is unsatisfiable (e.g. AllowedLibraryIDs is an empty
// slice). The repository converts it to an empty BrowseResult without firing
// SQL.
var errBrowseFavoritesEmpty = errors.New("browse favorites: empty result")

// BrowseFavorites returns the user's favorites filtered, sorted, and paginated
// in a single SQL query. Replaces the legacy fetch-all-then-filter path that
// pulled up to 10,000 favorites into memory before applying browse filters.
func (r *BrowseRepository) BrowseFavorites(ctx context.Context, filters BrowseFavoritesFilters) (*BrowseResult, error) {
	plan, err := buildBrowseFavoritesPlan(filters)
	if err != nil {
		if errors.Is(err, errBrowseFavoritesEmpty) {
			return &BrowseResult{Items: []*models.MediaItem{}, Total: 0, HasMore: false}, nil
		}
		return nil, err
	}

	sql, args := plan.pagedSQL()
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("browsing favorites: %w", err)
	}
	defer rows.Close()

	items, total, err := scanItemsWithTotal(rows)
	if err != nil {
		return nil, err
	}

	// COUNT(*) OVER () emits no rows when the data SELECT is empty, so total
	// stays 0 even when the broader result set has matching rows (e.g. OFFSET
	// past the last page). Re-query the count to give callers the real total.
	// Skip when offset == 0 because in that case an empty page genuinely means
	// total = 0.
	//
	// Use plan.offset (normalized) rather than filters.Offset (caller-supplied,
	// may be negative). buildBrowseFavoritesPlan takes filters by value and
	// normalizes a negative offset to 0; the SQL uses plan.offset, so the
	// HasMore/fallback predicates must match for consistency.
	if len(items) == 0 && plan.offset > 0 {
		countSQL, countArgs := plan.countSQL()
		if err := r.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
			return nil, fmt.Errorf("count fallback for empty favorites page: %w", err)
		}
	}

	return &BrowseResult{
		Items:   items,
		Total:   total,
		HasMore: total > plan.offset+len(items),
	}, nil
}

// browseFavoritesPlan captures the SQL fragments and bound args for a
// favorites browse query. Produced by buildBrowseFavoritesPlan; consumed by
// BrowseFavorites and by tests that inspect the emitted SQL without a
// database. Stored as fragments (not a pre-rendered string) so we can emit
// both the paged SELECT and a count-only fallback that strips
// LIMIT/OFFSET/ORDER BY for the empty-page total recovery path.
type browseFavoritesPlan struct {
	fromClause  string // e.g. "user_favorites uf JOIN media_items mi ON ..."
	whereClause string // includes the leading "WHERE "
	orderBy     string
	whereArgs   []any // args bound only by FROM+WHERE (no LIMIT/OFFSET)
	limit       int
	offset      int
}

// pagedSQL returns the fully-bound paginated SELECT. The plan always emits
// COUNT(*) OVER () AS total_count so the caller can read pagination totals
// from the same scan.
func (p browseFavoritesPlan) pagedSQL() (string, []any) {
	args := append([]any{}, p.whereArgs...)
	limitIdx := len(args) + 1
	offsetIdx := limitIdx + 1
	args = append(args, p.limit, p.offset)
	sql := fmt.Sprintf(
		"SELECT %s, COUNT(*) OVER () AS total_count FROM %s %s %s LIMIT $%d OFFSET $%d",
		qualifiedListItemColumns("mi"), p.fromClause, p.whereClause, p.orderBy,
		limitIdx, offsetIdx,
	)
	return sql, args
}

// countSQL renders a count-only fallback that ignores LIMIT/OFFSET/ORDER BY.
// Used when pagedSQL returns an empty page past offset 0: COUNT(*) OVER ()
// emits no rows when the data SELECT is empty, so the caller would otherwise
// see total=0 even when the broader result set has matching rows.
func (p browseFavoritesPlan) countSQL() (string, []any) {
	args := append([]any{}, p.whereArgs...)
	sql := fmt.Sprintf(
		"SELECT COUNT(*) FROM (SELECT 1 FROM %s %s) sub",
		p.fromClause, p.whereClause,
	)
	return sql, args
}

// buildBrowseFavoritesPlan assembles the favorites-scoped SQL plan. It
// performs no I/O so tests can pin the emitted SQL shape. Returns
// errBrowseFavoritesEmpty when the filter set excludes every row (e.g. the
// caller passed AllowedLibraryIDs: []int{}).
func buildBrowseFavoritesPlan(f BrowseFavoritesFilters) (browseFavoritesPlan, error) {
	// Normalize pagination defaults.
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	// AllowedLibraryIDs == []int{} means the viewer has no library access at
	// all; short-circuit before issuing SQL.
	if f.AllowedLibraryIDs != nil && len(f.AllowedLibraryIDs) == 0 {
		return browseFavoritesPlan{}, errBrowseFavoritesEmpty
	}

	args := make([]any, 0, 8)
	conditions := make([]string, 0, 4)
	argIdx := 1

	args = append(args, f.UserID)
	conditions = append(conditions, fmt.Sprintf("uf.user_id = $%d", argIdx))
	argIdx++

	args = append(args, f.ProfileID)
	conditions = append(conditions, fmt.Sprintf("uf.profile_id = $%d", argIdx))
	argIdx++

	if f.ItemType != "" {
		types := splitTypes(f.ItemType)
		switch len(types) {
		case 0:
			// no-op (fully whitespace)
		case 1:
			conditions = append(conditions, fmt.Sprintf("mi.type = $%d", argIdx))
			args = append(args, types[0])
			argIdx++
		default:
			conditions = append(conditions, fmt.Sprintf("mi.type = ANY($%d)", argIdx))
			args = append(args, types)
			argIdx++
		}
	}

	if f.Genre != "" {
		conditions = append(conditions, fmt.Sprintf("mi.genres @> ARRAY[$%d]::text[]", argIdx))
		args = append(args, f.Genre)
		argIdx++
	}

	if prefix := strings.TrimSpace(f.NamePrefix); prefix != "" {
		// Dual-column OR (sort-key expr || LOWER(title)) so the anchored
		// LIKE can use idx_media_items_sort_key on the first arm and
		// idx_media_items_search_exact_title on the second. See
		// browse.go filterWhereClauseForSource for the rationale.
		conditions = append(conditions, fmt.Sprintf(
			"(LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) LIKE $%d ESCAPE '\\' OR LOWER(mi.title) LIKE $%d ESCAPE '\\')",
			argIdx, argIdx,
		))
		args = append(args, likePrefixPattern(prefix))
		argIdx++
	}

	if f.LibraryID > 0 {
		conditions = append(conditions, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM media_item_libraries mil_one
				WHERE mil_one.content_id = mi.content_id
				  AND mil_one.media_folder_id = $%d)`,
			argIdx,
		))
		args = append(args, f.LibraryID)
		argIdx++
	}

	if f.AllowedLibraryIDs != nil {
		conditions = append(conditions, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id
				  AND mil.media_folder_id = ANY($%d))`,
			argIdx,
		))
		args = append(args, f.AllowedLibraryIDs)
		argIdx++
	}

	if len(f.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf(
			`NOT EXISTS (SELECT 1 FROM media_item_libraries mil_dis
				WHERE mil_dis.content_id = mi.content_id
				  AND mil_dis.media_folder_id = ANY($%d))`,
			argIdx,
		))
		args = append(args, f.DisabledLibraryIDs)
		argIdx++
	}

	applyAccessFilter("mi", AccessFilter{MaxContentRating: f.MaxContentRating, ExcludedMediaTypes: f.ExcludedMediaTypes}, &conditions, &args, &argIdx)

	// Manga chapters (type='ebook' rows linked into a manga series) are internal
	// sub-units and must never surface as standalone cards, matching the
	// exclusion applied across browse/search/discovery/sections.
	conditions = append(conditions, MangaChapterExclusionWhere("mi"))

	orderBy := buildBrowseFavoritesOrderBy(f.SortField, f.SortOrder)

	return browseFavoritesPlan{
		fromClause:  "user_favorites uf JOIN media_items mi ON mi.content_id = uf.media_item_id",
		whereClause: "WHERE " + strings.Join(conditions, " AND "),
		orderBy:     orderBy,
		whereArgs:   args,
		limit:       f.Limit,
		offset:      f.Offset,
	}, nil
}

// IsBrowseFavoritesSortSupported reports whether buildBrowseFavoritesOrderBy
// has an explicit ORDER BY for the given field. Used by upstream callers
// (jellycompat) to gate the SQL fast path: any sort outside this set would
// silently fall through to the added_at default, changing observed
// ordering vs. the legacy two-query path that delegated to a broader sorter.
//
// Keep aligned with the switch in buildBrowseFavoritesOrderBy. Empty string
// is permitted and maps to added_at (the default sort).
func IsBrowseFavoritesSortSupported(field string) bool {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "", "added_at", "title", "sort_title", "year", "release_date", "created_at":
		return true
	}
	return false
}

// buildBrowseFavoritesOrderBy maps user-facing sort fields to SQL expressions.
// The default sort is uf.added_at DESC (most-recently favorited first).
// mi.content_id is appended as a deterministic tiebreaker so OFFSET-based
// pagination is stable when many rows share the primary sort value.
//
// Keep the supported-fields list aligned with IsBrowseFavoritesSortSupported.
func buildBrowseFavoritesOrderBy(field, order string) string {
	direction := "DESC"
	if strings.EqualFold(strings.TrimSpace(order), "asc") {
		direction = "ASC"
	}
	nullsClause := ""
	if direction == "DESC" {
		nullsClause = " NULLS LAST"
	}

	switch strings.ToLower(strings.TrimSpace(field)) {
	case "title", "sort_title":
		return fmt.Sprintf(
			"ORDER BY LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) %s, LOWER(mi.title) %s, mi.content_id ASC",
			direction, direction,
		)
	case "year":
		return fmt.Sprintf("ORDER BY mi.year %s%s, mi.content_id ASC", direction, nullsClause)
	case "release_date":
		return fmt.Sprintf(
			"ORDER BY COALESCE(mi.release_date::text, NULLIF(BTRIM(mi.first_air_date), '')) %s%s, mi.content_id ASC",
			direction, nullsClause,
		)
	case "created_at":
		return fmt.Sprintf("ORDER BY mi.created_at %s, mi.content_id ASC", direction)
	case "added_at", "":
		fallthrough
	default:
		return fmt.Sprintf("ORDER BY uf.added_at %s, mi.content_id ASC", direction)
	}
}
