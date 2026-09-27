package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	querySortYear               = "year"
	querySortRuntime            = "runtime"
	querySortRatingIMDb         = "rating_imdb"
	querySortRatingTMDb         = "rating_tmdb"
	querySortRatingRTCritic     = "rating_rt_critic"
	querySortRatingRTAudience   = "rating_rt_audience"
	querySortResolution         = "resolution"
	querySortProgress           = "progress"
	querySortLatestEpisodeAdded = "latest_episode_added"
	querySortPlays              = "plays"
	querySortTitle              = "title"
	querySortDateViewed         = "date_viewed"
	querySortContentRating      = "content_rating"
	cursorContentIDColumn       = "content_id"
	cursorKindText              = "text"
	cursorKindNumber            = "number"
	cursorKindTimestamp         = "timestamp"
	cursorKindDate              = "date"
	cursorContentIDExpression   = "mi.content_id"
	cursorTruePredicate         = "TRUE"
	querySortAsc                = "asc"
	querySortReleaseDate        = "release_date"
	querySortLastAirDate        = "last_air_date"
	querySortBitrate            = "bitrate"
	querySortRandom             = "random"
)

// QueryCursor carries the complete SQL ordering tuple. The API must bind it to
// the normalized query and viewer scope in its signed cursor envelope.
// Values retain PostgreSQL's text precision rather than passing through float64.
type QueryCursor struct {
	Search     *CatalogSearchCursor `json:"search,omitempty"`
	Collection *CollectionCursor    `json:"collection,omitempty"`
	Keys       []QueryCursorValue   `json:"keys"`
	Consumed   int                  `json:"consumed"`
}
type QueryCursorValue struct {
	Kind  string  `json:"kind"`
	Value *string `json:"value"`
}
type QueryCursorPage struct {
	Items      []*models.MediaItem
	Total      int
	TotalExact bool
	HasMore    bool
	Next       *QueryCursor
}
type queryCursorTerm struct {
	expression string
	kind       string
	descending bool
	nullsLast  bool
}

func setCursorTermKinds(terms []queryCursorTerm, field string) {
	for i := range terms {
		terms[i].kind = cursorKindText
		if !terms[i].descending {
			terms[i].nullsLast = true
		}
	}
	if len(terms) == 0 {
		return
	}
	switch field {
	case querySortLastAirDate:
		terms[0].kind = cursorKindDate
	case querySortReleaseDate:
		if strings.HasSuffix(terms[0].expression, ".episode_air_date") {
			terms[0].kind = cursorKindDate
		}
	case defaultSortField, querySortDateViewed, querySortLatestEpisodeAdded:
		terms[0].kind = cursorKindTimestamp
	case querySortYear, querySortRuntime, querySortRatingIMDb, querySortRatingTMDb, querySortRatingRTCritic, querySortRatingRTAudience, querySortResolution, querySortBitrate, querySortProgress, querySortPlays, querySortContentRating:
		terms[0].kind = cursorKindNumber
	case playableTypeSeries:
		terms[1].kind = cursorKindNumber
	}
}

func (t queryCursorTerm) cast() string {
	switch t.kind {
	case cursorKindDate:
		return cursorKindDate
	case cursorKindNumber:
		return "numeric"
	case cursorKindTimestamp:
		return "timestamptz"
	default:
		return cursorKindText
	}
}

// cursorSeekSQL expands a mixed-direction, nullable tuple comparison. SQL row
// comparisons alone cannot express NULLS LAST or a descending primary key with
// ascending title and identity tie breakers.
func cursorSeekSQL(terms []queryCursorTerm, after *QueryCursor, start int) (string, []any, error) {
	if after == nil {
		return "", nil, nil
	}
	if len(after.Keys) != len(terms) {
		return "", nil, fmt.Errorf("cursor key count does not match sort")
	}
	var arms, equal []string
	var args []any
	for i, term := range terms {
		key := after.Keys[i]
		if key.Kind != term.kind {
			return "", nil, fmt.Errorf("cursor key %d has invalid kind", i)
		}
		expr := term.expression
		comparison := "FALSE"
		equality := expr + " IS NULL"
		if key.Value == nil {
			if !term.nullsLast {
				comparison = expr + " IS NOT NULL"
			}
		} else {
			param := fmt.Sprintf("$%d::%s", start+len(args), term.cast())
			args = append(args, *key.Value)
			operator := ">"
			if term.descending {
				operator = "<"
			}
			comparison = fmt.Sprintf("%s %s %s", expr, operator, param)
			if term.nullsLast {
				comparison = "(" + comparison + " OR " + expr + " IS NULL)"
			}
			equality = fmt.Sprintf("%s = %s", expr, param)
		}
		prefix := append(append([]string{}, equal...), comparison)
		arms = append(arms, "("+strings.Join(prefix, " AND ")+")")
		equal = append(equal, equality)
	}
	return "(" + strings.Join(arms, " OR ") + ")", args, nil
}

// PreviewCursorPage uses a SQL keyset, including the identity tie breaker. It
// intentionally uses the general episode relation until optimized projections
// expose exactly the same typed tuple. Page rows and optional count share one
// database snapshot; later pages observe live updates, not a retained snapshot.
func (e *QueryExecutor) PreviewCursorPage(ctx context.Context, def QueryDefinition, access AccessFilter, limit int, after *QueryCursor, includeTotal bool) (QueryCursorPage, error) {
	if e == nil || e.Pool == nil {
		return QueryCursorPage{}, fmt.Errorf("query executor requires a database pool")
	}
	if limit < 1 || limit > 200 {
		return QueryCursorPage{}, fmt.Errorf("cursor limit must be between 1 and 200")
	}
	consumed := 0
	if after != nil {
		consumed = after.Consumed
		if consumed < 0 {
			return QueryCursorPage{}, fmt.Errorf("invalid cursor consumed count")
		}
	}
	if def.Limit != nil && !e.GroupByWork {
		if *def.Limit <= 0 {
			return QueryCursorPage{}, fmt.Errorf("query limit must be positive")
		}
		if consumed >= *def.Limit {
			return QueryCursorPage{Items: []*models.MediaItem{}}, nil
		}
		limit = min(limit, *def.Limit-consumed)
	}
	plan, err := e.buildPreviewPagePlan(def, access, limit, 0)
	if err != nil {
		return QueryCursorPage{}, err
	}
	if def.Limit != nil && !e.GroupByWork {
		plan.maxResults = *def.Limit - consumed
	}
	sql, args, err := plan.cursorSQL(after)
	if err != nil {
		return QueryCursorPage{}, err
	}
	tx, err := e.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return QueryCursorPage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return QueryCursorPage{}, fmt.Errorf("querying cursor page: %w", err)
	}
	wrapped := &cursorRows{Rows: rows, terms: plan.cursorTerms}
	items, err := scanItemsWithMangaCounts(wrapped)
	rows.Close()
	if err != nil {
		return QueryCursorPage{}, err
	}
	result := QueryCursorPage{Items: items, HasMore: len(items) > plan.limit}
	if result.HasMore {
		result.Items = items[:plan.limit]
	}
	if result.HasMore && len(result.Items) > 0 {
		result.Next = &wrapped.keys[len(result.Items)-1]
		result.Next.Consumed = consumed + len(result.Items)
	}
	for _, item := range result.Items {
		if item.AddedAt == nil && !item.CreatedAt.IsZero() {
			item.AddedAt = new(item.CreatedAt)
		}
	}
	if includeTotal {
		if def.Limit != nil && !e.GroupByWork {
			plan.maxResults = *def.Limit
		}
		countSQL, countArgs := plan.countSQL()
		if err := tx.QueryRow(ctx, countSQL, countArgs...).Scan(&result.Total); err != nil {
			return QueryCursorPage{}, fmt.Errorf("counting cursor page: %w", err)
		}
		result.TotalExact = true
	}
	if err := tx.Commit(ctx); err != nil {
		return QueryCursorPage{}, err
	}
	return result, nil
}

func (p previewPagePlan) cursorSQL(after *QueryCursor) (string, []any, error) {
	args := append([]any{}, p.cteArgs...)
	args = append(args, p.args...)
	args = append(args, p.sortArgs...)
	seek, seekArgs, err := cursorSeekSQL(p.cursorTerms, after, len(args)+1)
	if err != nil {
		return "", nil, err
	}
	args = append(args, seekArgs...)
	where := p.whereClause
	if seek != "" {
		if where == "" {
			where = "WHERE " + seek
		} else {
			where += " AND " + seek
		}
	}
	columns := qualifiedListItemColumns("mi") + ", " + mangaCountColumns("mi")
	for _, term := range p.cursorTerms {
		columns += ", (" + term.expression + ")::text"
	}
	queryLimit := p.limit + 1
	if p.maxResults > 0 {
		queryLimit = min(queryLimit, p.maxResults)
	}
	args = append(args, queryLimit)
	prefix := ""
	if len(p.ctes) > 0 {
		prefix = "WITH " + strings.Join(p.ctes, ",\n") + "\n"
	}
	return fmt.Sprintf("%sSELECT %s %s %s %s LIMIT $%d", prefix, columns, p.fromClausePaged, where, p.orderBy, len(args)), args, nil
}

type cursorRows struct {
	pgx.Rows
	terms []queryCursorTerm
	keys  []QueryCursor
}

func (r *cursorRows) Scan(dest ...any) error {
	key := QueryCursor{Keys: make([]QueryCursorValue, len(r.terms))}
	for i, term := range r.terms {
		key.Keys[i].Kind = term.kind
		dest = append(dest, &key.Keys[i].Value)
	}
	if err := r.Rows.Scan(dest...); err != nil {
		return err
	}
	r.keys = append(r.keys, key)
	return nil
}

// SeekCursor locates the tuple immediately before a requested zero-based page
// position. Only explicit jumps use OFFSET; continuations always use the tuple.
// The caller still binds the resulting cursor to its query and viewer scope.
func (e *QueryExecutor) SeekCursor(ctx context.Context, def QueryDefinition, access AccessFilter, index int) (*QueryCursor, error) {
	if index < 0 || index > 10000000 {
		return nil, fmt.Errorf("jump index must be between 0 and 10000000")
	}
	if index == 0 {
		return nil, nil
	}
	if e == nil || e.Pool == nil {
		return nil, fmt.Errorf("query executor requires a database pool")
	}
	if def.Limit != nil && *def.Limit > 0 && index >= *def.Limit {
		return nil, pgx.ErrNoRows
	}
	plan, err := e.buildPreviewPagePlan(def, access, 1, 0)
	if err != nil {
		return nil, err
	}
	args := append([]any{}, plan.cteArgs...)
	args = append(args, plan.args...)
	args = append(args, plan.sortArgs...)
	columns := make([]string, len(plan.cursorTerms))
	key := &QueryCursor{Keys: make([]QueryCursorValue, len(plan.cursorTerms)), Consumed: index}
	dest := make([]any, len(columns))
	for i, term := range plan.cursorTerms {
		columns[i] = "(" + term.expression + ")::text"
		key.Keys[i].Kind = term.kind
		dest[i] = &key.Keys[i].Value
	}
	prefix := ""
	if len(plan.ctes) > 0 {
		prefix = "WITH " + strings.Join(plan.ctes, ",\n") + "\n"
	}
	args = append(args, index-1)
	sql := fmt.Sprintf("%sSELECT %s %s %s %s LIMIT 1 OFFSET $%d", prefix, strings.Join(columns, ", "), plan.fromClausePaged, plan.whereClause, plan.orderBy, len(args))
	if err := e.Pool.QueryRow(ctx, sql, args...).Scan(dest...); err != nil {
		return nil, fmt.Errorf("seeking cursor: %w", err)
	}
	return key, nil
}
