package catalog

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

type QueryExecutor struct {
	Pool  *pgxpool.Pool
	Scope string
	// BaseRelationSQL, when set, replaces the default "media_items mi" source.
	// The relation must already be aliased as "mi".
	BaseRelationSQL string
	// SnapshotAt, when set, restricts results to items created at or before
	// this timestamp.  This prevents offset-based pagination drift when new
	// items are inserted between page fetches (e.g. during a scan).
	SnapshotAt *time.Time
}

func (e *QueryExecutor) Preview(ctx context.Context, def QueryDefinition, access AccessFilter, limit int) ([]*models.MediaItem, int, error) {
	items, total, _, err := e.PreviewPage(ctx, def, access, limit, 0, true)
	return items, total, err
}

func (e *QueryExecutor) PreviewPage(
	ctx context.Context,
	def QueryDefinition,
	access AccessFilter,
	limit int,
	offset int,
	includeTotal bool,
) ([]*models.MediaItem, int, bool, error) {
	if e == nil || e.Pool == nil {
		return nil, 0, false, fmt.Errorf("query executor requires a database pool")
	}

	if items, total, hasMore, ok, err := e.tryEpisodeCatalogUserStatePreviewPage(
		ctx,
		def,
		access,
		limit,
		offset,
		includeTotal,
	); ok || err != nil {
		return items, total, hasMore, err
	}

	if items, total, hasMore, ok, err := e.tryEpisodeCatalogEntriesPreviewPage(
		ctx,
		def,
		access,
		limit,
		offset,
		includeTotal,
	); ok || err != nil {
		return items, total, hasMore, err
	}

	build, err := e.buildPreviewPagePlan(def, access, limit, offset)
	if err != nil {
		return nil, 0, false, err
	}

	pagedSQL, pagedArgs := build.pagedSQL(false)
	rows, err := e.Pool.Query(ctx, pagedSQL, pagedArgs...)
	if err != nil {
		return nil, 0, false, fmt.Errorf("querying preview items: %w", err)
	}
	defer rows.Close()

	var (
		items []*models.MediaItem
		total int
	)
	items, err = scanItems(rows)
	if err != nil {
		return nil, 0, false, err
	}
	hasMore := false
	if len(items) > build.limit {
		hasMore = true
		items = items[:build.limit]
	}
	// The preview path uses itemColumns which scans CreatedAt but not
	// AddedAt (set only by browse queries via MIN(mil.first_seen_at)).
	// Fall back to CreatedAt so the API response includes added_at.
	for _, item := range items {
		if item.AddedAt == nil && !item.CreatedAt.IsZero() {
			t := item.CreatedAt
			item.AddedAt = &t
		}
	}

	if includeTotal {
		countSQL, countArgs := build.countSQL()
		if err := e.Pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
			return nil, 0, false, fmt.Errorf("counting preview items: %w", err)
		}
		hasMore = total > build.offset+len(items)
		return items, total, hasMore, nil
	}

	return items, 0, hasMore, nil
}

// previewPagePlan captures the rendered SQL fragments and bound args for a
// preview page query. It is produced by buildPreviewPagePlan and consumed by
// PreviewPage (to execute) and by tests (to inspect the emitted SQL without a
// database).
type previewPagePlan struct {
	// ctes holds optional CTE definitions (without the leading "WITH "
	// keyword) prepended to the paged SELECT. cteArgs are bound at the
	// front of the final arg list — the CTE definitions reference $1..$N
	// for cteArgs, and the rest of the SQL has been rebased to start at
	// argIdx (len(cteArgs)+1). Used by the user_last_watched CTE
	// injection (audit 2026-05-01 §3.1 Pattern B).
	ctes    []string
	cteArgs []any
	// fromClausePaged is the FROM clause for the paged query (includes any
	// sort-plan joins).
	fromClausePaged string
	// fromClauseCount is the FROM clause for exact totals. It includes filter
	// joins, but intentionally excludes sort-only joins.
	fromClauseCount string
	whereClause     string
	args            []any
	orderBy         string
	sortArgs        []any
	limit           int
	offset          int
	maxResults      int
	limitArgIdx     int
}

// countSQL renders a count-only query that returns the total number of rows
// matching the plan's WHERE clause, ignoring LIMIT/OFFSET/ORDER BY. PreviewPage
// uses it when callers need an exact total; keeping the count separate lets the
// data SELECT use top-N/index plans instead of forcing COUNT(*) OVER () across
// every matching row. Wraps the inner query in `SELECT COUNT(*) FROM (...) sub`
// so any GROUP BY in the inner query is preserved.
//
// Bind cteArgs + args only. Sort-only joins and their args are intentionally
// excluded because ordering does not affect the filtered row count.
func (p previewPagePlan) countSQL() (string, []any) {
	args := append([]any{}, p.cteArgs...)
	args = append(args, p.args...)
	withClause := ""
	if len(p.ctes) > 0 {
		withClause = "WITH " + strings.Join(p.ctes, ",\n") + "\n"
	}
	if p.maxResults > 0 {
		countLimitArgIdx := len(args) + 1
		args = append(args, p.maxResults)
		sql := fmt.Sprintf(
			"%sSELECT COUNT(*) FROM (SELECT 1 %s %s LIMIT $%d) sub",
			withClause, p.fromClauseCount, p.whereClause, countLimitArgIdx,
		)
		return sql, args
	}
	sql := fmt.Sprintf(
		"%sSELECT COUNT(*) FROM (SELECT 1 %s %s) sub",
		withClause, p.fromClauseCount, p.whereClause,
	)
	return sql, args
}

// pagedSQL renders the final paged SELECT and returns it together with the
// fully-bound arg list. When includeTotal is false we ask the database for one
// extra row to detect more pages without an exact count. Exact totals are
// handled by countSQL instead of COUNT(*) OVER () so the page query can stop
// after the requested rows.
func (p previewPagePlan) pagedSQL(includeTotal bool) (string, []any) {
	queryLimit := p.limit
	if !includeTotal && (p.maxResults <= 0 || p.offset+p.limit < p.maxResults) {
		queryLimit++
	}
	args := append([]any{}, p.cteArgs...)
	args = append(args, p.args...)
	args = append(args, p.sortArgs...)
	args = append(args, queryLimit)
	offsetClause := ""
	if p.offset > 0 {
		offsetArgIdx := p.limitArgIdx + 1
		offsetClause = fmt.Sprintf(" OFFSET $%d", offsetArgIdx)
		args = append(args, p.offset)
	}
	selectList := qualifiedListItemColumns("mi")
	withClause := ""
	if len(p.ctes) > 0 {
		withClause = "WITH " + strings.Join(p.ctes, ",\n") + "\n"
	}
	sql := fmt.Sprintf(
		`%sSELECT %s %s %s %s LIMIT $%d%s`,
		withClause,
		selectList,
		p.fromClausePaged,
		p.whereClause,
		p.orderBy,
		p.limitArgIdx,
		offsetClause,
	)
	return sql, args
}

// buildPreviewPagePlan assembles the WHERE clause, FROM clause, args, and sort
// plan for a preview-page query. It performs no I/O. PreviewPage uses it to
// run the actual queries; tests use it (via buildPreviewPageSQL) to assert the
// emitted SQL.
func (e *QueryExecutor) buildPreviewPagePlan(
	def QueryDefinition,
	access AccessFilter,
	limit int,
	offset int,
) (previewPagePlan, error) {
	def = def.Normalize()
	allowPersonalized := strings.TrimSpace(access.ProfileID) != ""
	if err := def.ValidateWithOptions(allowPersonalized, allowPersonalized); err != nil {
		return previewPagePlan{}, err
	}

	libraryIDs := append([]int(nil), def.LibraryIDs...)
	if access.AllowedLibraryIDs != nil {
		if len(libraryIDs) == 0 {
			libraryIDs = append([]int(nil), access.AllowedLibraryIDs...)
		} else {
			libraryIDs = intersectInts(libraryIDs, access.AllowedLibraryIDs)
		}
	}

	// Scope can be set externally (e.g. catalog resolver pre-fills it from
	// the request) or implied by the query definition's MediaScope (e.g.
	// section fetchers construct executors without setting Scope and rely
	// on def.MediaScope being authoritative). Without this fallback, an
	// "episode" def routed through a bare executor would keep the default
	// media_items base relation while QueryBuilder emits mi.episode_air_date.
	effectiveScope := e.Scope
	if effectiveScope == "" {
		effectiveScope = def.MediaScope
	}

	baseRelation := e.BaseRelationSQL
	baseArgs := []any(nil)
	libraryScopeHandledInBaseRelation := false
	if isEpisodeCatalogScope(effectiveScope) {
		var handled bool
		baseRelation, baseArgs, handled = episodeCatalogBaseRelationForLibraries(
			libraryIDs,
			access.DisabledLibraryIDs,
			1,
		)
		libraryScopeHandledInBaseRelation = handled
	}
	if strings.TrimSpace(baseRelation) == "" {
		baseRelation = "media_items mi"
	}

	builder := NewQueryBuilder("mi").
		WithArgIdx(len(baseArgs)+1).
		WithUserScope(access.UserID, access.ProfileID).
		WithMediaScope(effectiveScope).
		WithLibraryScope(libraryIDs)
	filterWhere, filterArgs, err := builder.Build(def)
	if err != nil {
		return previewPagePlan{}, err
	}
	filterArgOffset := len(baseArgs)
	filterWhere = rebindSQLPlaceholders(filterWhere, filterArgOffset)

	var conditions []string
	args := append(append([]any{}, baseArgs...), filterArgs...)
	argIdx := builder.ArgIdx() + filterArgOffset

	if filterWhere != "" {
		conditions = append(conditions, filterWhere)
	}
	if def.MediaScope != "" && !isEpisodeCatalogScope(def.MediaScope) {
		conditions = append(conditions, fmt.Sprintf("mi.type = $%d", argIdx))
		args = append(args, def.MediaScope)
		argIdx++
	}
	libScopeWhere, libScopeArgs, hasLibraryScope := "", []any(nil), false
	if !libraryScopeHandledInBaseRelation {
		libScopeWhere, libScopeArgs, hasLibraryScope = buildLibraryScopeJoin(
			libraryIDs,
			access.DisabledLibraryIDs,
			argIdx,
			def.MediaScope,
			catalogLibraryContentExprForScope(def.MediaScope, "mi"),
		)
	}
	if hasLibraryScope {
		conditions = append(conditions, libScopeWhere)
		args = append(args, libScopeArgs...)
		argIdx += len(libScopeArgs)
	} else if access.AllowedLibraryIDs != nil && len(access.AllowedLibraryIDs) == 0 {
		conditions = append(conditions, "1 = 0")
	}
	if access.AllowedContentIDs != nil {
		if len(access.AllowedContentIDs) == 0 {
			conditions = append(conditions, "1 = 0")
		} else {
			conditions = append(conditions, fmt.Sprintf("mi.content_id = ANY($%d)", argIdx))
			args = append(args, access.AllowedContentIDs)
			argIdx++
		}
	}

	ApplySectionAccessFilter("mi", access, &conditions, &args, &argIdx)

	if e.SnapshotAt != nil {
		conditions = append(conditions, fmt.Sprintf("mi.created_at <= $%d", argIdx))
		args = append(args, *e.SnapshotAt)
		argIdx++
	}

	if prefix := strings.TrimSpace(access.NamePrefix); prefix != "" {
		// Dual-column OR matching browse.go and favorites_browse.go: items where
		// a curated sort_title differs from title (e.g. title="The Office",
		// sort_title="Office, The") would be silently lost on prefix="the" if we
		// only checked the COALESCE'd sort-key expression. The first arm uses the
		// scope-specific sort key; the second arm keeps literal title prefixes.
		prefixSortExpr := builder.normalizedTitleExpr()
		conditions = append(conditions, fmt.Sprintf(
			"(%s LIKE $%d ESCAPE '\\' OR LOWER(mi.title) LIKE $%d ESCAPE '\\')",
			prefixSortExpr, argIdx, argIdx,
		))
		args = append(args, escapePrefixForLike(prefix)+"%")
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}
	fromClauseBase := "FROM " + baseRelation
	fromClauseCount := fromClauseBase

	limit, offset, maxResults := normalizePreviewPageBounds(def, limit, offset)
	sortPlan, err := builder.WithArgIdx(argIdx).BuildSortPlan(def.Sort)
	if err != nil {
		return previewPagePlan{}, err
	}
	fromClausePaged := fromClauseBase
	if len(sortPlan.Joins) > 0 {
		fromClausePaged += " " + strings.Join(sortPlan.Joins, " ")
	}
	limitArgIdx := argIdx + len(sortPlan.Args)

	var ctes []string
	var cteArgs []any
	// When the filter clause references last_watched, the QueryBuilder flips
	// requireUserHistoryCTE so we splice in the user_last_watched CTE and a
	// LEFT JOIN aliased uhist (audit 2026-05-01 §3.1 Pattern B). The CTE
	// args are bound at $1, $2 — every other placeholder shifts by 2.
	if builder.RequiresUserHistoryCTE() {
		const cteShift = 2
		cteArgs = builder.UserHistoryCTEArgs()
		ctes = []string{UserHistoryCTESQL(1)}

		fromClausePaged = rebindSQLPlaceholders(fromClausePaged, cteShift)
		fromClauseCount = rebindSQLPlaceholders(fromClauseCount, cteShift)
		whereClause = rebindSQLPlaceholders(whereClause, cteShift)
		sortPlan.OrderBy = rebindSQLPlaceholders(sortPlan.OrderBy, cteShift)
		fromClausePaged += " LEFT JOIN user_last_watched uhist ON uhist.media_item_id = mi.content_id"
		fromClauseCount += " LEFT JOIN user_last_watched uhist ON uhist.media_item_id = mi.content_id"
		limitArgIdx += cteShift
	}

	return previewPagePlan{
		ctes:            ctes,
		cteArgs:         cteArgs,
		fromClausePaged: fromClausePaged,
		fromClauseCount: fromClauseCount,
		whereClause:     whereClause,
		args:            args,
		orderBy:         sortPlan.OrderBy,
		sortArgs:        sortPlan.Args,
		limit:           limit,
		offset:          offset,
		maxResults:      maxResults,
		limitArgIdx:     limitArgIdx,
	}, nil
}

func normalizePreviewPageBounds(def QueryDefinition, limit int, offset int) (int, int, int) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	if def.Limit == nil || *def.Limit <= 0 {
		return limit, offset, 0
	}

	maxResults := *def.Limit
	remaining := maxResults - offset
	if remaining <= 0 {
		return 0, offset, maxResults
	}
	if limit > remaining {
		limit = remaining
	}
	return limit, offset, maxResults
}

// buildPreviewPageSQL is a test-friendly facade over buildPreviewPagePlan that
// returns the rendered paged SELECT plus the bound args. It performs no I/O.
// includeTotal only controls whether the page query fetches exactly limit rows
// or an extra row for has-more detection; exact totals are rendered separately
// by previewPagePlan.countSQL.
func (e *QueryExecutor) buildPreviewPageSQL(
	def QueryDefinition,
	access AccessFilter,
	limit int,
	offset int,
	includeTotal bool,
) (string, []any, error) {
	build, err := e.buildPreviewPagePlan(def, access, limit, offset)
	if err != nil {
		return "", nil, err
	}
	sql, args := build.pagedSQL(includeTotal)
	return sql, args, nil
}

// escapePrefixForLike lower-cases the prefix and escapes %, _, and \ so the
// resulting string is safe to use as a LIKE pattern with ESCAPE '\'.
func escapePrefixForLike(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// buildLibraryScopeJoin returns a WHERE clause that scopes the outer query
// to items whose library membership matches the allow/deny lists. The clause
// is an EXISTS / NOT EXISTS semi-join that uses membership indexes
// directly without fanning out for items present in multiple libraries — Audit
// Pattern D (2026-05-01 §3 Pattern D). The prior
// shape wrapped the join in a SELECT DISTINCT subquery to defuse that
// fanout; the DISTINCT was load-bearing because the PK is on the (content,
// folder) PAIR, not on content alone. EXISTS is the canonical non-fanout
// idiom and lets the planner hash-semi-join.
//
// Returns the WHERE clause (without a leading "AND"), the bound args, and a
// boolean indicating whether scoping was applied. The caller appends the
// clause to its conditions slice and the args to its arg list.
func buildLibraryScopeJoin(
	allowedLibraryIDs,
	disabledLibraryIDs []int,
	argIdx int,
	mediaScope string,
	itemContentExpr string,
) (string, []any, bool) {
	if len(allowedLibraryIDs) == 0 && len(disabledLibraryIDs) == 0 {
		return "", nil, false
	}

	if strings.TrimSpace(itemContentExpr) == "" {
		itemContentExpr = "mi.content_id"
	}
	tableName, keyColumn := catalogLibraryMembershipTableAndKeyForScope(mediaScope)

	clauses := make([]string, 0, 2)
	args := make([]any, 0, 2)

	if len(allowedLibraryIDs) > 0 {
		clauses = append(clauses, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM %s mil_scope_in WHERE mil_scope_in.%s = %s AND mil_scope_in.media_folder_id = ANY($%d))`,
			tableName, keyColumn, itemContentExpr, argIdx,
		))
		args = append(args, allowedLibraryIDs)
		argIdx++
	}

	if len(disabledLibraryIDs) > 0 {
		clauses = append(clauses, fmt.Sprintf(
			`NOT EXISTS (SELECT 1 FROM %s mil_scope_out WHERE mil_scope_out.%s = %s AND mil_scope_out.media_folder_id = ANY($%d))`,
			tableName, keyColumn, itemContentExpr, argIdx,
		))
		args = append(args, disabledLibraryIDs)
		argIdx++
	}

	return strings.Join(clauses, " AND "), args, true
}

func intersectInts(a, b []int) []int {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	allowed := make(map[int]struct{}, len(b))
	for _, value := range b {
		allowed[value] = struct{}{}
	}
	var result []int
	seen := make(map[int]struct{})
	for _, value := range a {
		if _, ok := allowed[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

var sqlPlaceholderPattern = regexp.MustCompile(`\$(\d+)`)

func rebindSQLPlaceholders(sql string, offset int) string {
	if offset <= 0 || strings.TrimSpace(sql) == "" {
		return sql
	}
	return sqlPlaceholderPattern.ReplaceAllStringFunc(sql, func(match string) string {
		value, err := strconv.Atoi(match[1:])
		if err != nil {
			return match
		}
		return fmt.Sprintf("$%d", value+offset)
	})
}
