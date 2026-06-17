package catalog

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// BrowseFilters represents all supported filter, sort, and pagination parameters
// for the /items browse endpoint.
type BrowseFilters struct {
	Type               string   // single type ("movie") or comma-separated ("movie,series")
	Genre              string   // single genre filter (GIN index)
	NamePrefix         string   // case-insensitive prefix filter on sort_title/title
	ContentIDs         []string // optional allowlist of exact content IDs
	LibraryID          int      // filter by specific library
	LibraryIDs         []int    // accessible library IDs (nil = all)
	DisabledLibraryIDs []int    // user-disabled libraries to exclude (only used when LibraryIDs is nil)
	MaxContentRating   string   // maximum allowed content rating ceiling
	YearMin            int      // minimum year (inclusive)
	YearMax            int      // maximum year (inclusive)
	ContentRating      []string // comma-separated content ratings (e.g., PG-13, TV-MA)
	Status             string   // pending, matched, unmatched (optional filter)
	PersonID           int64    // filter items by person (joins item_people)
	Sort               string   // created_at, release_date, rating_imdb, rating_tmdb, year, sort_title, recently_added
	Order              string   // asc, desc
	Limit              int
	MaxLimit           int // optional caller-specific cap; zero keeps the default browse cap
	Offset             int
	SnapshotAt         *time.Time // pagination fence: exclude items created after this timestamp
	RequireBackdrop    bool       // only return items with a non-empty backdrop_path (Jellyfin ImageTypes=Backdrop filter)
}

// BrowseResult contains the paginated result of a browse query.
type BrowseResult struct {
	Items   []*models.MediaItem `json:"items"`
	Total   int                 `json:"total"`
	HasMore bool                `json:"has_more"`
}

// BrowseRepository provides browse/filter queries on the media_items table.
type BrowseRepository struct {
	pool *pgxpool.Pool
}

// NewBrowseRepository creates a new BrowseRepository.
func NewBrowseRepository(pool *pgxpool.Pool) *BrowseRepository {
	return &BrowseRepository{pool: pool}
}

// Pool returns the underlying pgx pool for ad-hoc queries.
func (r *BrowseRepository) Pool() *pgxpool.Pool {
	return r.pool
}

// Browse executes a filtered, sorted, paginated query against media_items.
func (r *BrowseRepository) Browse(ctx context.Context, filters BrowseFilters) (*BrowseResult, error) {
	return r.browse(ctx, filters, true)
}

func (r *BrowseRepository) BrowsePage(ctx context.Context, filters BrowseFilters, includeTotal bool) (*BrowseResult, error) {
	return r.browse(ctx, filters, includeTotal)
}

func (r *BrowseRepository) browse(ctx context.Context, filters BrowseFilters, includeTotal bool) (*BrowseResult, error) {
	plan, earlyEmpty, err := r.buildBrowsePlan(filters)
	if err != nil {
		return nil, err
	}
	if earlyEmpty {
		return &BrowseResult{Items: []*models.MediaItem{}, Total: 0, HasMore: false}, nil
	}

	dataQuery, queryArgs := plan.pagedSQL(false)
	rows, err := r.pool.Query(ctx, dataQuery, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("browsing media items: %w", err)
	}
	defer rows.Close()

	var (
		items []*models.MediaItem
		total int
	)
	items, err = scanBrowseItems(rows)
	if err != nil {
		return nil, err
	}

	hasMore := false
	hasExtraRow := len(items) > plan.limit
	if hasExtraRow {
		items = items[:plan.limit]
	}
	if includeTotal {
		switch {
		case plan.offset == 0 && len(items) == 0:
			total = 0
		case !hasExtraRow && len(items) > 0:
			total = plan.offset + len(items)
		default:
			countSQL, countArgs := plan.countSQL()
			if err := r.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
				return nil, fmt.Errorf("count browse page: %w", err)
			}
		}
		hasMore = total > plan.offset+plan.limit
	} else if hasExtraRow {
		hasMore = true
	}

	return &BrowseResult{
		Items:   items,
		Total:   total,
		HasMore: hasMore,
	}, nil
}

// browseQueryPlan captures the rendered SQL fragments and bound args for a
// browse data query. It is produced by buildBrowsePlan and consumed both by
// browse() (to execute) and by tests (to inspect the emitted SQL without a
// database).
type browseQueryPlan struct {
	selectClause  string
	fromClause    string
	whereClause   string
	groupByClause string
	orderBy       string
	// args holds bound values for the FROM/WHERE/GROUP BY portion of the
	// plan. orderArgs is kept separate so countSQL — which omits ORDER BY —
	// can bind only the args its SQL actually references. Combining the two
	// would make countSQL pass extra bound values past the count query's
	// placeholder set, and pgx/Postgres reject that with "bind message
	// supplies N parameters, but prepared statement requires M". The
	// random+SnapshotAt sort path (buildOrderByPlan) is the canonical case:
	// it bakes a bound timestamp arg into the ORDER BY expression that the
	// count query has no use for.
	args        []any
	orderArgs   []any
	limit       int
	offset      int
	limitArgIdx int // 1-based bound-arg index for the LIMIT param
}

// countSQL renders a count-only query that returns the total number of rows
// matching the plan's FROM/WHERE/GROUP BY, ignoring LIMIT/OFFSET/ORDER BY.
// Used by exact-total callers so the data SELECT can remain a pure page query.
// Wraps the inner query in `SELECT COUNT(*) FROM (...) sub` so any GROUP BY in
// the inner query is preserved.
//
// Binds only p.args (FROM/WHERE/GROUP BY values). orderArgs are deliberately
// excluded — the count SQL has no ORDER BY clause to reference them, and
// passing extra bound values would fail with "bind message supplies N
// parameters, but prepared statement requires M".
func (p browseQueryPlan) countSQL() (string, []any) {
	args := append([]any{}, p.args...)
	sql := fmt.Sprintf(
		"SELECT COUNT(*) FROM (SELECT 1 FROM %s %s %s) sub",
		p.fromClause, p.whereClause, p.groupByClause,
	)
	return sql, args
}

// pagedSQL renders the final paged SELECT and returns it together with the
// fully-bound arg list. The query always asks for one extra row so callers can
// detect whether more pages exist without coupling the sorted page fetch to an
// exact count.
func (p browseQueryPlan) pagedSQL(_ bool) (string, []any) {
	queryLimit := p.limit
	queryLimit++
	// Bind order: where/from args, then order-by args, then LIMIT, then OFFSET.
	// limitArgIdx is computed by buildBrowsePlan after consuming order args, so
	// it correctly points at LIMIT after the orderArgs are bound below.
	args := append([]any{}, p.args...)
	args = append(args, p.orderArgs...)
	args = append(args, queryLimit, p.offset)
	offsetArgIdx := p.limitArgIdx + 1

	selectList := p.selectClause
	sql := fmt.Sprintf(
		"SELECT %s FROM %s %s %s %s LIMIT $%d OFFSET $%d",
		selectList, p.fromClause, p.whereClause, p.groupByClause, p.orderBy,
		p.limitArgIdx, offsetArgIdx,
	)
	return sql, args
}

// buildBrowsePlan assembles the WHERE clause, FROM clause, args, ORDER BY, and
// GROUP BY for a browse query. It performs no I/O. browse() uses it to run the
// actual query; tests use it to assert the emitted SQL. earlyEmpty == true
// means the filters are unsatisfiable (e.g. empty library allowlist) and the
// caller should return an empty result without executing.
func (r *BrowseRepository) buildBrowsePlan(filters BrowseFilters) (browseQueryPlan, bool, error) {
	// Normalize defaults.
	if filters.Limit <= 0 {
		filters.Limit = 20
	}
	maxLimit := filters.MaxLimit
	if maxLimit <= 0 {
		maxLimit = 100
	}
	if filters.Limit > maxLimit {
		filters.Limit = maxLimit
	}
	if filters.Offset < 0 {
		filters.Offset = 0
	}
	if filters.Order == "" {
		filters.Order = "desc"
	}
	if filters.Sort == "" {
		filters.Sort = "created_at"
	}

	// Build WHERE clause and args.
	var conditions []string
	var args []any
	argIdx := 1

	// Type filter (supports comma-separated multi-type, e.g. "movie,series").
	if filters.Type != "" {
		types := splitTypes(filters.Type)
		if len(types) == 1 {
			conditions = append(conditions, fmt.Sprintf("mi.type = $%d", argIdx))
			args = append(args, types[0])
			argIdx++
		} else {
			conditions = append(conditions, fmt.Sprintf("mi.type = ANY($%d)", argIdx))
			args = append(args, types)
			argIdx++
		}
	}

	if filters.ContentIDs != nil {
		if len(filters.ContentIDs) == 0 {
			return browseQueryPlan{}, true, nil
		}
		conditions = append(conditions, fmt.Sprintf("mi.content_id = ANY($%d)", argIdx))
		args = append(args, filters.ContentIDs)
		argIdx++
	}

	// Genre filter (GIN array containment).
	if filters.Genre != "" {
		conditions = append(conditions, fmt.Sprintf("mi.genres @> ARRAY[$%d]::text[]", argIdx))
		args = append(args, filters.Genre)
		argIdx++
	}

	if prefix := strings.TrimSpace(filters.NamePrefix); prefix != "" {
		// Dual-column OR so titles without a curated sort_title still match.
		// First arm matches the idx_media_items_sort_key expression
		// (LOWER(COALESCE(NULLIF(BTRIM(sort_title),''), title))) so the
		// anchored LIKE is sargable; second arm uses idx_media_items_search_exact_title
		// (LOWER(title)). Both arms are equivalent when sort_title is empty,
		// which is harmless — the planner can BitmapOr the two index scans.
		conditions = append(conditions, fmt.Sprintf(
			"(LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) LIKE $%d ESCAPE '\\' OR LOWER(mi.title) LIKE $%d ESCAPE '\\')",
			argIdx, argIdx,
		))
		args = append(args, likePrefixPattern(prefix))
		argIdx++
	}

	// Year range filters.
	if filters.YearMin > 0 {
		conditions = append(conditions, fmt.Sprintf("mi.year >= $%d", argIdx))
		args = append(args, filters.YearMin)
		argIdx++
	}
	if filters.YearMax > 0 {
		conditions = append(conditions, fmt.Sprintf("mi.year <= $%d", argIdx))
		args = append(args, filters.YearMax)
		argIdx++
	}

	// Content rating filter (multi-value).
	if len(filters.ContentRating) > 0 {
		conditions = append(conditions, fmt.Sprintf("mi.content_rating = ANY($%d)", argIdx))
		args = append(args, filters.ContentRating)
		argIdx++
	}

	// Status filter.
	if filters.Status != "" {
		conditions = append(conditions, fmt.Sprintf("mi.status = $%d", argIdx))
		args = append(args, filters.Status)
		argIdx++
	}

	// Backdrop presence filter (Jellyfin ImageTypes=Backdrop). Pushed down so
	// random/limited selections only ever pick items that actually have a
	// backdrop — filtering after the LIMIT would wrongly return empty pages.
	if filters.RequireBackdrop {
		conditions = append(conditions, "NULLIF(BTRIM(mi.backdrop_path), '') IS NOT NULL")
	}

	// Library access control: restrict to user's accessible libraries.
	needsLibJoin := filters.LibraryID > 0 || filters.LibraryIDs != nil || len(filters.DisabledLibraryIDs) > 0 || filters.Sort == "recently_added"
	needsPersonJoin := filters.PersonID > 0
	// When filtering by exactly one library and no person join, each
	// content_id matches at most one mil row, so the GROUP BY usually added
	// to dedup the junction join is unnecessary. Skipping it lets ORDER BY
	// mil.first_seen_at exploit idx_item_libraries_folder_seen_content
	// directly — turning a full-library scan + heapsort into a top-N index
	// walk.
	singleLibraryNoDedup := filters.LibraryID > 0 && filters.LibraryIDs == nil && len(filters.DisabledLibraryIDs) == 0 && !needsPersonJoin

	if filters.PersonID > 0 {
		conditions = append(conditions, fmt.Sprintf("ip.person_id = $%d", argIdx))
		args = append(args, filters.PersonID)
		argIdx++
	}

	if filters.LibraryID > 0 {
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = $%d", argIdx))
		args = append(args, filters.LibraryID)
		argIdx++
	}

	if filters.LibraryIDs != nil {
		if len(filters.LibraryIDs) == 0 {
			// User has empty library access — return no results.
			return browseQueryPlan{}, true, nil
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = ANY($%d)", argIdx))
		args = append(args, filters.LibraryIDs)
		argIdx++
	}

	if len(filters.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (mil.media_folder_id = ANY($%d))", argIdx))
		args = append(args, filters.DisabledLibraryIDs)
		argIdx++
	}

	applyAccessFilter("mi", AccessFilter{MaxContentRating: filters.MaxContentRating}, &conditions, &args, &argIdx)

	// Manga chapters (type='ebook' rows linked into a manga series) are internal
	// sub-units and must never surface as standalone catalog items.
	conditions = append(conditions, MangaChapterExclusionWhere("mi"))

	if filters.SnapshotAt != nil {
		conditions = append(conditions, fmt.Sprintf("mi.created_at <= $%d", argIdx))
		args = append(args, *filters.SnapshotAt)
		argIdx++
	}

	// Build FROM clause with optional JOIN.
	fromClause := "media_items mi"
	if needsLibJoin {
		fromClause = "media_items mi JOIN media_item_libraries mil ON mi.content_id = mil.content_id"
	}
	if needsPersonJoin {
		fromClause += " JOIN item_people ip ON ip.content_id = mi.content_id"
	}

	// Build WHERE string.
	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Build ORDER BY clause. orderArgs are kept out of `args` so countSQL can
	// bind only its FROM/WHERE/GROUP BY values; pagedSQL binds them together.
	// argIdx still advances past them so limitArgIdx points at the right
	// LIMIT placeholder once orderArgs are bound positionally before LIMIT.
	orderBy, orderArgs := buildOrderByPlan(filters.Sort, filters.Order, filters.SnapshotAt, argIdx, singleLibraryNoDedup, browseFiltersAreMovieOnly(filters))
	argIdx += len(orderArgs)

	// Only run the manga count subqueries when the scope can contain manga
	// series; a non-manga type filter rules them out, so substitute NULL
	// placeholders and skip two correlated subqueries per row on the hot path.
	mangaCounts := mangaCountColumns("mi")
	if !browseScopeMayContainManga(filters) {
		mangaCounts = nullMangaCountColumns()
	}
	selectClause := browseItemColumns("mi") + ", " + mangaCounts
	groupByClause := ""
	switch {
	case singleLibraryNoDedup:
		// Single library, no person join: mil row is unique per content_id.
		// Project mil.first_seen_at directly so ORDER BY can use the index.
		selectClause += ", mil.first_seen_at"
	case needsLibJoin:
		// Multi-library or disabled-library filter: dedup with GROUP BY and
		// expose the earliest first_seen_at as the "added at" timestamp.
		selectClause += ", MIN(mil.first_seen_at)"
		groupByClause = "GROUP BY " + browseGroupByColumns("mi")
	case needsPersonJoin:
		// Person join needs GROUP BY to deduplicate, but mil is not available.
		selectClause += ", mi.created_at"
		groupByClause = "GROUP BY " + browseGroupByColumns("mi")
	default:
		// No junction join — use created_at as the added_at fallback.
		selectClause += ", mi.created_at"
	}

	return browseQueryPlan{
		selectClause:  selectClause,
		fromClause:    fromClause,
		whereClause:   whereClause,
		groupByClause: groupByClause,
		orderBy:       orderBy,
		args:          args,
		orderArgs:     orderArgs,
		limit:         filters.Limit,
		offset:        filters.Offset,
		limitArgIdx:   argIdx,
	}, false, nil
}

// filterWhereClause builds the common WHERE clause and FROM clause used by the
// ListXxx distinct-value methods.  If the returned earlyEmpty flag is true the
// caller should return an empty result immediately (e.g. when LibraryIDs is an
// empty slice).
func filterWhereClause(filters BrowseFilters) (fromClause, whereClause string, args []any, earlyEmpty bool) {
	return filterWhereClauseForSource(filters, "media_items mi", "")
}

func filterWhereClauseForSource(filters BrowseFilters, baseRelation string, mediaScope string) (fromClause, whereClause string, args []any, earlyEmpty bool) {
	var conditions []string
	argIdx := 1

	if filters.Type != "" {
		types := splitTypes(filters.Type)
		if len(types) == 1 {
			conditions = append(conditions, fmt.Sprintf("mi.type = $%d", argIdx))
			args = append(args, types[0])
			argIdx++
		} else {
			conditions = append(conditions, fmt.Sprintf("mi.type = ANY($%d)", argIdx))
			args = append(args, types)
			argIdx++
		}
	}
	if prefix := strings.TrimSpace(filters.NamePrefix); prefix != "" {
		// Same dual-column shape as filterWhereClauseForSource's primary
		// browse path — see comment there for index-alignment rationale.
		conditions = append(conditions, fmt.Sprintf(
			"(LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) LIKE $%d ESCAPE '\\' OR LOWER(mi.title) LIKE $%d ESCAPE '\\')",
			argIdx, argIdx,
		))
		args = append(args, likePrefixPattern(prefix))
		argIdx++
	}
	if filters.Status != "" {
		conditions = append(conditions, fmt.Sprintf("mi.status = $%d", argIdx))
		args = append(args, filters.Status)
		argIdx++
	}
	if filters.PersonID > 0 {
		conditions = append(conditions, fmt.Sprintf("ip.person_id = $%d", argIdx))
		args = append(args, filters.PersonID)
		argIdx++
	}
	if filters.LibraryID > 0 {
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = $%d", argIdx))
		args = append(args, filters.LibraryID)
		argIdx++
	}
	if filters.ContentIDs != nil {
		if len(filters.ContentIDs) == 0 {
			return "", "", nil, true
		}
		conditions = append(conditions, fmt.Sprintf("mi.content_id = ANY($%d)", argIdx))
		args = append(args, filters.ContentIDs)
		argIdx++
	}
	if filters.LibraryIDs != nil {
		if len(filters.LibraryIDs) == 0 {
			return "", "", nil, true
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = ANY($%d)", argIdx))
		args = append(args, filters.LibraryIDs)
		argIdx++
	}

	if len(filters.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (mil.media_folder_id = ANY($%d))", argIdx))
		args = append(args, filters.DisabledLibraryIDs)
		argIdx++
	}

	applyAccessFilter("mi", AccessFilter{MaxContentRating: filters.MaxContentRating}, &conditions, &args, &argIdx)

	fromClause = baseRelation
	libraryContentExpr := catalogLibraryContentExprForScope(mediaScope, "mi")
	if filters.LibraryID > 0 || filters.LibraryIDs != nil || len(filters.DisabledLibraryIDs) > 0 {
		membershipTable, membershipKey := catalogLibraryMembershipTableAndKeyForScope(mediaScope)
		fromClause += " JOIN " + membershipTable + " mil ON " + libraryContentExpr + " = mil." + membershipKey
	}
	if filters.PersonID > 0 {
		fromClause += " JOIN item_people ip ON ip.content_id = mi.content_id"
	}

	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}
	return fromClause, whereClause, args, false
}

// listDistinctArrayColumn returns sorted distinct non-empty values from an
// array column (genres, studios, networks, countries).
func (r *BrowseRepository) listDistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters) ([]string, error) {
	return listDistinctArrayColumnWithSource(ctx, r.pool, column, filters, "media_items mi", "")
}

func listDistinctArrayColumnWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	column string,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
) ([]string, error) {
	fromClause, whereClause, args, empty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if empty {
		return []string{}, nil
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT val
		FROM (
			SELECT UNNEST(mi.%s) AS val
			FROM %s
			%s
		) vals
		WHERE val <> ''
		ORDER BY val ASC
		LIMIT %d
	`, column, fromClause, whereClause, catalogFacetMaxValues)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", column, err)
	}
	defer rows.Close()

	values := make([]string, 0)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan %s: %w", column, err)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", column, err)
	}

	slices.Sort(values)
	return values, nil
}

// listDistinctScalarColumn returns sorted distinct non-empty values from a
// scalar text column (e.g. content_rating).
func (r *BrowseRepository) listDistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters) ([]string, error) {
	return listDistinctScalarColumnWithSource(ctx, r.pool, column, filters, "media_items mi", "")
}

func listDistinctScalarColumnWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	column string,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
) ([]string, error) {
	fromClause, whereClause, args, empty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if empty {
		return []string{}, nil
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT mi.%s
		FROM %s
		%s
		  AND mi.%s <> ''
		ORDER BY mi.%s ASC
		LIMIT %d
	`, column, fromClause, whereClause, column, column, catalogFacetMaxValues)

	// When there are no WHERE conditions, the extra AND is invalid — prepend WHERE instead.
	if whereClause == "" {
		query = fmt.Sprintf(`
			SELECT DISTINCT mi.%s
			FROM %s
			WHERE mi.%s <> ''
			ORDER BY mi.%s ASC
			LIMIT %d
		`, column, fromClause, column, column, catalogFacetMaxValues)
	}

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", column, err)
	}
	defer rows.Close()

	values := make([]string, 0)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan %s: %w", column, err)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", column, err)
	}

	slices.Sort(values)
	return values, nil
}

// ListGenres returns the distinct genres matching the supplied browse filters.
func (r *BrowseRepository) ListGenres(ctx context.Context, filters BrowseFilters) ([]string, error) {
	return r.listDistinctArrayColumn(ctx, "genres", filters)
}

// ListStudios returns the distinct studios matching the supplied browse filters.
func (r *BrowseRepository) ListStudios(ctx context.Context, filters BrowseFilters) ([]string, error) {
	return r.listDistinctArrayColumn(ctx, "studios", filters)
}

// ListNetworks returns the distinct networks matching the supplied browse filters.
func (r *BrowseRepository) ListNetworks(ctx context.Context, filters BrowseFilters) ([]string, error) {
	return r.listDistinctArrayColumn(ctx, "networks", filters)
}

// ListCountries returns the distinct countries matching the supplied browse filters.
func (r *BrowseRepository) ListCountries(ctx context.Context, filters BrowseFilters) ([]string, error) {
	return r.listDistinctArrayColumn(ctx, "countries", filters)
}

// ListContentRatings returns the distinct content ratings matching the supplied browse filters.
func (r *BrowseRepository) ListContentRatings(ctx context.Context, filters BrowseFilters) ([]string, error) {
	return r.listDistinctScalarColumn(ctx, "content_rating", filters)
}

func (r *BrowseRepository) ListOriginalLanguages(ctx context.Context, filters BrowseFilters) ([]string, error) {
	return r.listDistinctScalarColumn(ctx, "original_language", filters)
}

func (r *BrowseRepository) ListResolutions(ctx context.Context, filters BrowseFilters) ([]string, error) {
	return listResolutionsWithSource(ctx, r.pool, filters, "media_items mi", "")
}

func listResolutionsWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
) ([]string, error) {
	fromClause, whereClause, args, earlyEmpty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if earlyEmpty {
		return nil, nil
	}
	mediaFileJoin := catalogMediaFileJoinConditionForScope(mediaScope, "mf", "mi")

	query := fmt.Sprintf(`
		SELECT DISTINCT mf.resolution
		FROM %s
		JOIN media_files mf ON %s
		%s
		  AND mf.missing_since IS NULL
		  AND mf.resolution IS NOT NULL
		  AND BTRIM(mf.resolution) <> ''
		ORDER BY mf.resolution ASC
	`, fromClause, mediaFileJoin, browseFilterPrefix(whereClause))
	return queryDistinctStrings(ctx, pool, query, args)
}

func (r *BrowseRepository) ListAudioLanguages(ctx context.Context, filters BrowseFilters) ([]string, error) {
	return r.listDistinctJSONBLanguageWithFilters(ctx, "audio_tracks", filters)
}

func (r *BrowseRepository) ListSubtitleLanguages(ctx context.Context, filters BrowseFilters) ([]string, error) {
	return listSubtitleLanguagesWithSource(ctx, r.pool, filters, "media_items mi", "")
}

func listSubtitleLanguagesWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
) ([]string, error) {
	fromClause, whereClause, args, earlyEmpty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if earlyEmpty {
		return nil, nil
	}
	mediaFileJoin := catalogMediaFileJoinConditionForScope(mediaScope, "mf", "mi")

	// Embedded subtitles use the migration 104 generated text[]
	// `subtitle_language_codes`; external subs still need a JSONB unnest
	// because the generated column only covers subtitle_tracks
	// (audit 2026-05-01 §2.5b).
	query := fmt.Sprintf(`
		SELECT DISTINCT value
		FROM (
			SELECT lang AS value
			FROM %s
			JOIN media_files mf ON %s
			CROSS JOIN LATERAL UNNEST(mf.subtitle_language_codes) AS lang
			%s
			  AND mf.missing_since IS NULL

			UNION

			SELECT LOWER(COALESCE(track->>'language', '')) AS value
			FROM %s
			JOIN media_files mf ON %s
			CROSS JOIN LATERAL jsonb_array_elements(COALESCE(mf.external_subtitles, '[]'::jsonb)) AS track
			%s
			  AND mf.missing_since IS NULL
		) languages
		WHERE value IS NOT NULL AND value <> ''
		ORDER BY value ASC
	`, fromClause, mediaFileJoin, browseFilterPrefix(whereClause), fromClause, mediaFileJoin, browseFilterPrefix(whereClause))
	return queryDistinctStrings(ctx, pool, query, args)
}

func (r *BrowseRepository) listDistinctJSONBLanguageWithFilters(ctx context.Context, column string, filters BrowseFilters) ([]string, error) {
	return listDistinctJSONBLanguageWithSource(ctx, r.pool, column, filters, "media_items mi", "")
}

// listDistinctPeopleByKindWithSource returns distinct people.name values for a
// PersonKind across the scoped result set. Powers the Authors and Narrators
// facets — both share item_people, just keyed by `kind`. The from/where
// clauses from filterWhereClauseForSource gate by library / access / scope,
// so an audiobook-only library or audiobook media_scope drops video-only
// content automatically.
func listDistinctPeopleByKindWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	kind models.PersonKind,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
) ([]string, error) {
	fromClause, whereClause, args, earlyEmpty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if earlyEmpty {
		return []string{}, nil
	}
	args = append(args, int(kind))
	kindIdx := len(args)
	query := fmt.Sprintf(`
		SELECT DISTINCT p.name
		FROM %s
		JOIN item_people ip ON ip.content_id = mi.content_id AND ip.kind = $%d
		JOIN people p ON p.id = ip.person_id
		%s
		  AND p.name IS NOT NULL
		  AND BTRIM(p.name) <> ''
		ORDER BY p.name ASC
		LIMIT %d
	`, fromClause, kindIdx, browseFilterPrefix(whereClause), catalogFacetMaxValues)
	return queryDistinctStrings(ctx, pool, query, args)
}

// listDistinctAudiobookSeriesWithSource returns distinct series_name values
// from the active book series table joined onto the scoped result set. Names are trimmed
// and case-folded for sort so the picker doesn't show duplicates that differ
// only by whitespace or casing; the literal trimmed series_name is still
// returned so existing rules continue to match.
//
// The DISTINCT happens in an inline subquery (rather than directly on the
// outer SELECT) so the outer ORDER BY can apply LOWER(...) without violating
// Postgres' "ORDER BY expressions must appear in select list" rule for
// SELECT DISTINCT.
func listDistinctAudiobookSeriesWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
) ([]string, error) {
	fromClause, whereClause, args, earlyEmpty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if earlyEmpty {
		return []string{}, nil
	}
	query := fmt.Sprintf(`
		SELECT name FROM (
			SELECT DISTINCT BTRIM(s.series_name) AS name
			FROM %s
			JOIN %s s ON s.content_id = mi.content_id
			%s
			  AND s.series_name IS NOT NULL
			  AND BTRIM(s.series_name) <> ''
		) names
		ORDER BY LOWER(name) ASC
		LIMIT %d
	`, fromClause, bookSeriesTableForMediaScope(mediaScope), browseFilterPrefix(whereClause), catalogFacetMaxValues)
	return queryDistinctStrings(ctx, pool, query, args)
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

// searchDistinctPeopleByKindWithSource is the typeahead equivalent of
// listDistinctPeopleByKindWithSource; powers /api/v1/catalog/filters/search
// for facet=author and facet=narrator.
func searchDistinctPeopleByKindWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	kind models.PersonKind,
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
	args = append(args, int(kind))
	kindIdx := len(args)
	args = append(args, prefix+"%")
	prefixIdx := len(args)
	// DISTINCT in an inline subquery so the outer ORDER BY can apply
	// LOWER() without violating Postgres' "ORDER BY expressions must
	// appear in select list" rule for SELECT DISTINCT.
	query := fmt.Sprintf(`
		SELECT name FROM (
			SELECT DISTINCT p.name AS name
			FROM %s
			JOIN item_people ip ON ip.content_id = mi.content_id AND ip.kind = $%d
			JOIN people p ON p.id = ip.person_id
			%s
			  AND p.name IS NOT NULL
			  AND BTRIM(p.name) <> ''
			  AND LOWER(p.name) LIKE LOWER($%d)
		) matches
		ORDER BY LOWER(name) ASC
		LIMIT %d
	`, fromClause, kindIdx, browseFilterPrefix(whereClause), prefixIdx, limit+1)
	return queryFacetSearchResults(ctx, pool, query, args, limit)
}

// searchDistinctAudiobookSeriesWithSource is the typeahead equivalent of
// listDistinctAudiobookSeriesWithSource.
func searchDistinctAudiobookSeriesWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
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
	query := fmt.Sprintf(`
		SELECT name FROM (
			SELECT DISTINCT BTRIM(s.series_name) AS name
			FROM %s
			JOIN %s s ON s.content_id = mi.content_id
			%s
			  AND s.series_name IS NOT NULL
			  AND BTRIM(s.series_name) <> ''
			  AND LOWER(BTRIM(s.series_name)) LIKE LOWER($%d)
		) names
		ORDER BY LOWER(name) ASC
		LIMIT %d
	`, fromClause, bookSeriesTableForMediaScope(mediaScope), browseFilterPrefix(whereClause), prefixIdx, limit+1)
	return queryFacetSearchResults(ctx, pool, query, args, limit)
}

func bookSeriesTableForMediaScope(mediaScope string) string {
	if mediaScope == "ebook" {
		return "ebook_series"
	}
	return "audiobook_series"
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

func listDistinctJSONBLanguageWithSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	column string,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
) ([]string, error) {
	fromClause, whereClause, args, earlyEmpty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if earlyEmpty {
		return nil, nil
	}
	mediaFileJoin := catalogMediaFileJoinConditionForScope(mediaScope, "mf", "mi")

	// Migration 104 added STORED text[] generated columns derived from the
	// JSONB tracks. Use them when available so this listing UNNESTs an
	// indexed array instead of parsing JSONB per row
	// (audit 2026-05-01 §2.5b).
	arrayColumn := generatedLanguageColumnFor(column)
	if arrayColumn != "" {
		query := fmt.Sprintf(`
			SELECT DISTINCT lang AS value
			FROM %s
			JOIN media_files mf ON %s
			CROSS JOIN LATERAL UNNEST(mf.%s) AS lang
			%s
			  AND mf.missing_since IS NULL
			  AND lang IS NOT NULL
			  AND lang <> ''
			ORDER BY value ASC
			LIMIT %d
		`, fromClause, mediaFileJoin, arrayColumn, browseFilterPrefix(whereClause), catalogFacetMaxValues)
		return queryDistinctStrings(ctx, pool, query, args)
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT LOWER(COALESCE(track->>'language', '')) AS value
		FROM %s
		JOIN media_files mf ON %s
		CROSS JOIN LATERAL jsonb_array_elements(COALESCE(mf.%s, '[]'::jsonb)) AS track
		%s
		  AND mf.missing_since IS NULL
		  AND COALESCE(track->>'language', '') <> ''
		ORDER BY value ASC
		LIMIT %d
	`, fromClause, mediaFileJoin, column, browseFilterPrefix(whereClause), catalogFacetMaxValues)
	return queryDistinctStrings(ctx, pool, query, args)
}

// generatedLanguageColumnFor returns the migration-104 STORED text[] column
// that mirrors the given JSONB tracks column, or "" if none exists.
func generatedLanguageColumnFor(jsonbColumn string) string {
	switch jsonbColumn {
	case "audio_tracks":
		return "audio_language_codes"
	case "subtitle_tracks":
		return "subtitle_language_codes"
	default:
		return ""
	}
}

func browseFilterPrefix(whereClause string) string {
	if whereClause == "" {
		return "WHERE TRUE"
	}
	return whereClause
}

func queryDistinctStrings(ctx context.Context, pool *pgxpool.Pool, query string, args []any) ([]string, error) {
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

// browseItemColumns returns the item columns prefixed with the given alias.
func browseItemColumns(alias string) string {
	cols := []string{
		"content_id", "type", "title", "sort_title", "original_title", "year", "genres",
		"content_rating", "runtime", "overview", "tagline",
		"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
		"imdb_id", "tmdb_id", "tvdb_id",
		"poster_path", "poster_thumbhash", "backdrop_path", "backdrop_thumbhash", "logo_path",
		"metadata_s3_path", "metadata_etag", "season_count",
		"studios", "networks", "countries", "keywords", "original_language", "release_date::text", "first_air_date", "last_air_date",
		"show_status",
		"matched_at", "episode_metadata_incomplete", "episode_metadata_last_checked_at", "status", "created_at", "updated_at",
	}
	prefixed := make([]string, len(cols))
	for i, col := range cols {
		if col == "last_air_date" {
			prefixed[i] = effectiveLastAirDateExpr(alias)
			continue
		}
		prefixed[i] = alias + "." + col
	}
	return strings.Join(prefixed, ", ")
}

// mangaCountColumns returns two index-backed correlated subqueries feeding the
// "X Volumes · X Chapters" poster chip: distinct volume tokens (many chapter
// rows can share one volume) and loose chapter rows without a volume token.
// They return 0 for non-manga rows (no matching manga_chapters), which the
// scan path nils out so only manga cards carry the counts. The subqueries are
// functionally dependent on alias.content_id (the media_items PK, which leads
// browseGroupByColumns), so they remain valid under the dedup GROUP BY without
// being listed there.
func mangaCountColumns(alias string) string {
	return "(SELECT count(*) FROM manga_chapters mc WHERE mc.series_content_id = " + alias + ".content_id AND (mc.volume IS NULL OR mc.volume = '')) AS manga_chapter_count, " +
		"(SELECT count(DISTINCT mc.volume) FROM manga_chapters mc WHERE mc.series_content_id = " + alias + ".content_id AND mc.volume IS NOT NULL AND mc.volume <> '') AS manga_volume_count"
}

// nullMangaCountColumns substitutes NULL placeholders for the manga count
// subqueries when the browse scope cannot contain manga series. Column names
// and order match mangaCountColumns so the scan path is unchanged.
func nullMangaCountColumns() string {
	return "NULL::bigint AS manga_chapter_count, NULL::bigint AS manga_volume_count"
}

// browseScopeMayContainManga reports whether a browse with these filters could
// return type='manga' rows. An empty type filter (all types) or one that
// includes "manga" keeps the counts; any other explicit type filter rules
// manga out, letting the caller skip the count subqueries.
func browseScopeMayContainManga(filters BrowseFilters) bool {
	if filters.Type == "" {
		return true
	}
	for _, t := range strings.Split(filters.Type, ",") {
		if strings.TrimSpace(t) == "manga" {
			return true
		}
	}
	return false
}

// MangaChapterExclusionWhere returns a WHERE predicate that hides manga CHAPTER
// items (type='ebook' rows linked into a type='manga' series via the
// manga_chapters table) from catalog listing surfaces — browse, section
// resolution, and search. Chapters are internal sub-units of a manga series and
// must never appear as standalone catalog items; only the series should.
//
// It is index-backed: manga_chapters.chapter_content_id is the table's primary
// key, so the anti-join is a cheap unique-index probe. The predicate is global
// and harmless for every other row: regular ebooks have no manga_chapters link,
// and non-ebook types never match either, so they all pass. It is redundant
// (but harmless) for type='manga' browse scopes, whose series rows are linked
// via series_content_id, not chapter_content_id.
//
// By-id fetch paths that legitimately resolve chapters — the ebook reader,
// continue-reading (ebook_reader_progress / watch-progress), and the series
// detail chapter list (mangaChaptersQuery) — use separate queries and must NOT
// call this.
func MangaChapterExclusionWhere(alias string) string {
	return "NOT EXISTS (SELECT 1 FROM manga_chapters mc WHERE mc.chapter_content_id = " + alias + ".content_id)"
}

// browseGroupByColumns returns the columns needed for GROUP BY when joining
// with the junction table.
func browseGroupByColumns(alias string) string {
	cols := []string{
		"content_id", "type", "title", "sort_title", "original_title", "year", "genres",
		"content_rating", "runtime", "overview", "tagline",
		"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
		"imdb_id", "tmdb_id", "tvdb_id",
		"poster_path", "poster_thumbhash", "backdrop_path", "backdrop_thumbhash", "logo_path",
		"metadata_s3_path", "metadata_etag", "season_count",
		"studios", "networks", "countries", "keywords", "original_language", "release_date::text", "first_air_date", "last_air_date",
		"show_status",
		"matched_at", "episode_metadata_incomplete", "episode_metadata_last_checked_at", "status", "created_at", "updated_at",
	}
	prefixed := make([]string, len(cols))
	for i, col := range cols {
		prefixed[i] = alias + "." + col
	}
	return strings.Join(prefixed, ", ")
}

// likePrefixPattern returns prefix lowercased and LIKE-escaped (%, _, \) with
// a trailing % so it forms a prefix-anchored LIKE pattern. Thin wrapper over
// escapePrefixForLike so any future special-character fix applies to both
// the browse path and the query_executor preview path.
func likePrefixPattern(prefix string) string {
	return escapePrefixForLike(prefix) + "%"
}

// scanBrowseItems scans rows returned by the browse query, which include an
// extra added_at column appended after the standard item columns.
func scanBrowseItems(rows pgx.Rows) ([]*models.MediaItem, error) {
	var items []*models.MediaItem
	for rows.Next() {
		var item models.MediaItem
		err := rows.Scan(
			&item.ContentID,
			&item.Type,
			&item.Title,
			&item.SortTitle,
			&item.OriginalTitle,
			&item.Year,
			&item.Genres,
			&item.ContentRating,
			&item.Runtime,
			&item.Overview,
			&item.Tagline,
			&item.RatingIMDB,
			&item.RatingTMDB,
			&item.RatingRTCritic,
			&item.RatingRTAudience,
			&item.ImdbID,
			&item.TmdbID,
			&item.TvdbID,
			&item.PosterPath,
			&item.PosterThumbhash,
			&item.BackdropPath,
			&item.BackdropThumbhash,
			&item.LogoPath,
			&item.MetadataS3Path,
			&item.MetadataEtag,
			&item.SeasonCount,
			&item.Studios,
			&item.Networks,
			&item.Countries,
			&item.Keywords,
			&item.OriginalLanguage,
			&item.ReleaseDate,
			&item.FirstAirDate,
			&item.LastAirDate,
			&item.ShowStatus,
			&item.MatchedAt,
			&item.EpisodeMetadataIncomplete,
			&item.EpisodeMetadataLastCheckedAt,
			&item.Status,
			&item.CreatedAt,
			&item.UpdatedAt,
			&item.MangaChapterCount,
			&item.MangaVolumeCount,
			&item.AddedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning browse item row: %w", err)
		}
		// The manga count subqueries return 0 for non-manga rows; drop them so
		// only manga cards carry the counts (movies/series stay clean).
		if item.Type != "manga" {
			item.MangaChapterCount = nil
			item.MangaVolumeCount = nil
		}
		items = append(items, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating browse item rows: %w", err)
	}
	return items, nil
}

// buildOrderBy constructs the ORDER BY clause from sort and order parameters.
func buildOrderBy(sort, order string) string {
	clause, _ := buildOrderByPlan(sort, order, nil, 0, false, false)
	return clause
}

func buildOrderByPlan(sort, order string, snapshot *time.Time, argIdx int, singleLibraryNoDedup bool, movieOnly bool) (string, []any) {
	direction := "DESC"
	if strings.EqualFold(order, "asc") {
		direction = "ASC"
	}

	nullsClause := ""
	if direction == "DESC" {
		nullsClause = " NULLS LAST"
	}

	// Every case includes mi.content_id as a final tiebreaker so that
	// OFFSET-based pagination is deterministic when many rows share the
	// same sort value (e.g. same IMDB rating or NULL).
	switch sort {
	case "random":
		if snapshot != nil && argIdx > 0 {
			return fmt.Sprintf("ORDER BY md5(mi.content_id || $%d::text) ASC, mi.content_id ASC", argIdx), []any{snapshot.UTC().Format(time.RFC3339Nano)}
		}
		return "ORDER BY RANDOM()", nil
	case "release_date":
		if movieOnly {
			return fmt.Sprintf("ORDER BY mi.release_date %s%s, mi.content_id ASC", direction, nullsClause), nil
		}
		return fmt.Sprintf(
			"ORDER BY COALESCE(mi.release_date::text, NULLIF(BTRIM(mi.first_air_date), '')) %s%s, mi.content_id ASC",
			direction,
			nullsClause,
		), nil
	case "last_air_date":
		return fmt.Sprintf(
			"ORDER BY %s %s%s, mi.content_id ASC",
			effectiveLastAirDateExpr("mi"),
			direction,
			nullsClause,
		), nil
	case "rating_imdb":
		return fmt.Sprintf("ORDER BY mi.rating_imdb %s%s, mi.content_id ASC", direction, nullsClause), nil
	case "rating_tmdb":
		return fmt.Sprintf("ORDER BY mi.rating_tmdb %s%s, mi.content_id ASC", direction, nullsClause), nil
	case "year":
		return fmt.Sprintf("ORDER BY mi.year %s, mi.content_id ASC", direction), nil
	case "title", "sort_title":
		return fmt.Sprintf(
			"ORDER BY LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) %s, LOWER(mi.title) %s, mi.content_id ASC",
			direction,
			direction,
		), nil
	case "recently_added":
		// Without GROUP BY (single-library filter), reference mil.first_seen_at
		// directly so the planner can drive the order from
		// idx_item_libraries_folder_seen_content instead of materializing
		// every library row to aggregate.
		if singleLibraryNoDedup {
			return fmt.Sprintf("ORDER BY mil.first_seen_at %s, mi.content_id ASC", direction), nil
		}
		return fmt.Sprintf("ORDER BY MIN(mil.first_seen_at) %s, mi.content_id ASC", direction), nil
	case "created_at":
		return fmt.Sprintf("ORDER BY mi.created_at %s, mi.content_id ASC", direction), nil
	default:
		return fmt.Sprintf("ORDER BY mi.created_at %s, mi.content_id ASC", direction), nil
	}
}

func browseFiltersAreMovieOnly(filters BrowseFilters) bool {
	types := splitTypes(filters.Type)
	return len(types) == 1 && types[0] == "movie"
}

// ParseContentRatings splits a comma-separated content rating string into
// a slice. Empty input returns nil.
func ParseContentRatings(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	ratings := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			ratings = append(ratings, trimmed)
		}
	}
	if len(ratings) == 0 {
		return nil
	}
	return ratings
}

// ParseIntParam parses a string as int, returning 0 if empty or invalid.
func ParseIntParam(s string) int {
	if s == "" {
		return 0
	}
	v, _ := strconv.Atoi(s)
	return v
}

// ParseInt64Param parses a string to int64, returning 0 if empty or invalid.
func ParseInt64Param(s string) int64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// splitTypes splits a comma-separated type string into trimmed, non-empty parts.
func splitTypes(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}
