package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// AudiobookGroupBy enumerates the supported audiobook grouping axes.
type AudiobookGroupBy string

const (
	AudiobookGroupByAuthor   AudiobookGroupBy = "author"
	AudiobookGroupByNarrator AudiobookGroupBy = "narrator"
	AudiobookGroupBySeries   AudiobookGroupBy = "series"
)

const (
	audiobookGroupSortCount    = "count"
	audiobookGroupSortDuration = "duration"
)

// ParseAudiobookGroupBy validates a group_by request parameter.
func ParseAudiobookGroupBy(raw string) (AudiobookGroupBy, bool) {
	switch AudiobookGroupBy(strings.ToLower(strings.TrimSpace(raw))) {
	case AudiobookGroupByAuthor:
		return AudiobookGroupByAuthor, true
	case AudiobookGroupByNarrator:
		return AudiobookGroupByNarrator, true
	case AudiobookGroupBySeries:
		return AudiobookGroupBySeries, true
	default:
		return "", false
	}
}

// AudiobookGroupsQuery controls the grouped audiobook browse lookup.
type AudiobookGroupsQuery struct {
	LibraryID    int
	GroupBy      AudiobookGroupBy
	SearchPrefix string
	IncludeTotal bool
	// Sort is one of "name" (default), "count", "duration".
	Sort         string
	Limit        int
	Offset       int
	CursorPaging bool
	After        *AudiobookGroupCursor
}

// AudiobookGroupCursor is a live aggregate tuple. GroupKey is the exact SQL
// LOWER(BTRIM) identity, not a display name normalized in Go.
type AudiobookGroupCursor struct {
	GroupKey string `json:"group_key"`
	Value    int64  `json:"value"`
}

// AudiobookGroupsResult is the paged grouped browse response before API image
// URL resolution.
type AudiobookGroupsResult struct {
	Groups     []AudiobookGroup
	Next       *AudiobookGroupCursor
	Total      int
	HasMore    bool
	TotalExact bool
}

// AudiobookGroup is one grouped browse row: an author, narrator, or series
// with aggregate stats over the audiobooks visible to the viewer.
type AudiobookGroup struct {
	Name                 string
	GroupKey             string
	ItemCount            int
	TotalDurationSeconds int64
	InProgressCount      int
	FinishedCount        int
	// PosterPaths holds up to four raw poster paths for cover stacks; the
	// API layer presigns them.
	PosterPaths []string
}

type audiobookGroupsSQLPlan struct {
	SQL          string
	CountSQL     string
	CountArgs    []any
	Args         []any
	Limit        int
	FetchLimit   int
	Offset       int
	IncludeTotal bool
}

// audiobookGroupsPageCap bounds externally-requested pages.
const audiobookGroupsPageCap = 500

// ListAudiobookGroups returns grouped browse rows for an audiobook library.
// Groups are aggregated per person name (authors/narrators) or per series
// name, matching the case-insensitive semantics of the corresponding catalog
// filters, so a group name can be fed straight back into an author/narrator/
// series filter rule. Progress counts are scoped to the requesting profile via
// filter.UserID / filter.ProfileID.
func ListAudiobookGroups(ctx context.Context, pool *pgxpool.Pool, q AudiobookGroupsQuery, filter AccessFilter) (AudiobookGroupsResult, error) {
	return listAudiobookGroupsWithLimit(ctx, pool, q, filter, audiobookGroupsPageCap)
}

func listAudiobookGroupsWithLimit(ctx context.Context, pool *pgxpool.Pool, q AudiobookGroupsQuery, filter AccessFilter, maxLimit int) (AudiobookGroupsResult, error) {
	if pool == nil {
		return AudiobookGroupsResult{}, fmt.Errorf("audiobook groups: no database pool")
	}

	plan, err := buildAudiobookGroupsSQLWithLimit(q, filter, maxLimit)
	if err != nil {
		return AudiobookGroupsResult{}, err
	}
	if plan.SQL == "" {
		return AudiobookGroupsResult{Groups: []AudiobookGroup{}, TotalExact: plan.IncludeTotal}, nil
	}

	var queryer interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
		QueryRow(context.Context, string, ...any) pgx.Row
	} = pool
	var tx pgx.Tx
	if q.CursorPaging {
		tx, err = pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return AudiobookGroupsResult{}, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		queryer = tx
	}
	rows, err := queryer.Query(ctx, plan.SQL, plan.Args...)
	if err != nil {
		return AudiobookGroupsResult{}, fmt.Errorf("querying audiobook groups: %w", err)
	}
	defer rows.Close()

	total := 0
	groups := make([]AudiobookGroup, 0, plan.Limit)
	for rows.Next() {
		var g AudiobookGroup
		var posterPaths []string
		dest := []any{&g.Name, &g.ItemCount, &g.TotalDurationSeconds, &g.InProgressCount, &g.FinishedCount, &posterPaths}
		if plan.IncludeTotal {
			dest = append(dest, &total)
		}
		if q.CursorPaging {
			dest = append(dest, &g.GroupKey)
		}
		if err := rows.Scan(dest...); err != nil {
			return AudiobookGroupsResult{}, fmt.Errorf("scanning audiobook group: %w", err)
		}

		if posterPaths == nil {
			posterPaths = []string{}
		}
		g.PosterPaths = posterPaths
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return AudiobookGroupsResult{}, fmt.Errorf("iterating audiobook groups: %w", err)
	}

	rows.Close()
	if q.CursorPaging && plan.IncludeTotal && len(groups) == 0 {
		if err := queryer.QueryRow(ctx, plan.CountSQL, plan.CountArgs...).Scan(&total); err != nil {
			return AudiobookGroupsResult{}, fmt.Errorf("counting empty audiobook group page: %w", err)
		}
	}
	if tx != nil {
		if err := tx.Commit(ctx); err != nil {
			return AudiobookGroupsResult{}, err
		}
	}
	hasMore := false
	var next *AudiobookGroupCursor
	if q.CursorPaging {
		hasMore = len(groups) > plan.Limit
		if hasMore {
			groups = groups[:plan.Limit]
			last := groups[len(groups)-1]
			next = &AudiobookGroupCursor{GroupKey: last.GroupKey}
			switch strings.ToLower(strings.TrimSpace(q.Sort)) {
			case audiobookGroupSortCount:
				next.Value = int64(last.ItemCount)
			case audiobookGroupSortDuration:
				next.Value = last.TotalDurationSeconds
			}
		}
		if !plan.IncludeTotal {
			total = len(groups)
			if hasMore {
				total++
			}
		}
	} else if plan.IncludeTotal {
		hasMore = plan.Offset+len(groups) < total
	} else if len(groups) > plan.Limit {
		hasMore = true
		groups = groups[:plan.Limit]
		total = plan.Offset + len(groups) + 1
	} else {
		total = plan.Offset + len(groups)
	}

	return AudiobookGroupsResult{
		Groups:     groups,
		Next:       next,
		Total:      total,
		HasMore:    hasMore,
		TotalExact: plan.IncludeTotal,
	}, nil
}

func buildAudiobookGroupsSQL(q AudiobookGroupsQuery, filter AccessFilter) (audiobookGroupsSQLPlan, error) {
	return buildAudiobookGroupsSQLWithLimit(q, filter, audiobookGroupsPageCap)
}

func buildAudiobookGroupsSQLWithLimit(q AudiobookGroupsQuery, filter AccessFilter, maxLimit int) (audiobookGroupsSQLPlan, error) {
	if q.LibraryID <= 0 {
		return audiobookGroupsSQLPlan{}, fmt.Errorf("audiobook groups: library id is required")
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	if maxLimit <= 0 {
		maxLimit = audiobookGroupsPageCap
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := max(q.Offset, 0)
	fetchLimit := limit
	if !q.IncludeTotal || q.CursorPaging {
		fetchLimit++
	}

	args := []any{q.LibraryID}
	argIdx := 2

	conditions := []string{
		"mi.type = 'audiobook'",
		"mil.media_folder_id = $1",
	}
	if !appendAudiobookItemAccessConditions("mi", filter, &conditions, &args, &argIdx) {
		return audiobookGroupsSQLPlan{
			Limit:        limit,
			FetchLimit:   fetchLimit,
			Offset:       offset,
			IncludeTotal: q.IncludeTotal,
		}, nil
	}

	var joinClause, nameExpr, groupExpr, posterOrder string
	switch q.GroupBy {
	case AudiobookGroupByAuthor, AudiobookGroupByNarrator:
		kind := models.PersonKindAuthor
		if q.GroupBy == AudiobookGroupByNarrator {
			kind = models.PersonKindNarrator
		}
		joinClause = fmt.Sprintf(`JOIN item_people ip ON ip.content_id = b.content_id AND ip.kind = $%d
			JOIN people p ON p.id = ip.person_id`, argIdx)
		args = append(args, int(kind))
		argIdx++
		nameExpr = "MIN(BTRIM(p.name))"
		groupExpr = "LOWER(BTRIM(p.name))"
		posterOrder = "b.sort_title"
	case AudiobookGroupBySeries:
		joinClause = "JOIN audiobook_series s ON s.content_id = b.content_id"
		nameExpr = "MIN(BTRIM(s.series_name))"
		groupExpr = "LOWER(BTRIM(s.series_name))"
		posterOrder = "s.series_index NULLS LAST, b.sort_title"
	default:
		return audiobookGroupsSQLPlan{}, fmt.Errorf("audiobook groups: unsupported group_by %q", q.GroupBy)
	}

	groupFilters := make([]string, 0, 1)
	if search := audiobookGroupSearchPattern(q.SearchPrefix); search != "" {
		groupFilters = append(groupFilters, fmt.Sprintf(`%s LIKE $%d ESCAPE '\'`, groupExpr, argIdx))
		args = append(args, search)
		argIdx++
	}
	groupWhereClause := ""
	if len(groupFilters) > 0 {
		groupWhereClause = "WHERE " + strings.Join(groupFilters, " AND ")
	}

	userArg := fmt.Sprintf("$%d", argIdx)
	args = append(args, filter.UserID)
	argIdx++
	profileArg := fmt.Sprintf("$%d", argIdx)
	args = append(args, filter.ProfileID)
	argIdx++

	var orderClause string
	switch strings.ToLower(strings.TrimSpace(q.Sort)) {
	case "", "name":
		orderClause = "LOWER(name)"
	case audiobookGroupSortCount:
		orderClause = "item_count DESC, LOWER(name)"
	case audiobookGroupSortDuration:
		orderClause = "total_duration_seconds DESC, LOWER(name)"
	default:
		return audiobookGroupsSQLPlan{}, fmt.Errorf("audiobook groups: unsupported sort %q", q.Sort)
	}

	countArgs := append([]any(nil), args...)
	seekClause := ""
	if q.CursorPaging {
		orderClause = "group_key"
		sortColumn := ""
		switch strings.ToLower(strings.TrimSpace(q.Sort)) {
		case audiobookGroupSortCount:
			sortColumn = "item_count"
		case audiobookGroupSortDuration:
			sortColumn = "total_duration_seconds"
		}
		if sortColumn != "" {
			orderClause = sortColumn + " DESC, group_key"
		}
		if q.After != nil {
			if sortColumn == "" {
				seekClause = fmt.Sprintf("WHERE group_key > $%d", argIdx)
				args = append(args, q.After.GroupKey)
				argIdx++
			} else {
				seekClause = fmt.Sprintf("WHERE (%s < $%d OR (%s = $%d AND group_key > $%d))", sortColumn, argIdx, sortColumn, argIdx, argIdx+1)
				args = append(args, q.After.Value, q.After.GroupKey)
				argIdx += 2
			}
		}
	}
	totalSelect := ""
	totalColumn := ""
	if q.IncludeTotal {
		totalSelect = ", COUNT(*) OVER ()::int AS total_groups"
		totalColumn = ", pg.total_groups"
	}

	pageClause := fmt.Sprintf("LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	pageArgs := []any{fetchLimit, offset}
	keyColumn := ""
	if q.CursorPaging {
		pageClause = fmt.Sprintf("LIMIT $%d", argIdx)
		pageArgs = []any{fetchLimit}
		keyColumn = ", pg.group_key"
	}

	base := fmt.Sprintf(`
		WITH books AS (
			SELECT
				mi.content_id,
				mi.poster_path,
				COALESCE(NULLIF(mi.sort_title, ''), mi.title) AS sort_title,
				COALESCE(afs.duration_seconds, 0)::bigint AS duration_seconds
			FROM media_items mi
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			LEFT JOIN audiobook_item_file_stats afs
			       ON afs.media_folder_id = mil.media_folder_id
			      AND afs.content_id = mi.content_id
			WHERE %s
		),
		grouped AS (
			SELECT
				%s AS group_key,
				%s AS name,
				COUNT(*)::int AS item_count,
				COALESCE(SUM(b.duration_seconds), 0)::bigint AS total_duration_seconds,
				COUNT(*) FILTER (WHERE uwp.media_item_id IS NOT NULL AND NOT uwp.completed)::int AS in_progress_count,
				COUNT(*) FILTER (WHERE uwp.completed)::int AS finished_count
			FROM books b
			%s
			LEFT JOIN user_watch_progress uwp
			       ON uwp.media_item_id = b.content_id
			      AND uwp.user_id = %s
			      AND uwp.profile_id = %s
			%s
			GROUP BY %s
		)`, strings.Join(conditions, " AND "), groupExpr, nameExpr, joinClause, userArg, profileArg, groupWhereClause, groupExpr)
	countSQL := base + " SELECT COUNT(*)::int FROM grouped"
	query := base + fmt.Sprintf(`,
        counted_groups AS (SELECT grouped.*%s FROM grouped),
		paged_groups AS (
			SELECT
				group_key,
				name,
				item_count,
				total_duration_seconds,
				in_progress_count,
				finished_count%s
			FROM counted_groups
            %s
			ORDER BY %s
            %s
		)
		SELECT
			pg.name,
			pg.item_count,
			pg.total_duration_seconds,
			pg.in_progress_count,
			pg.finished_count,
			COALESCE(posters.poster_paths, '{}'::text[]) AS poster_paths%s%s
		FROM paged_groups pg
		LEFT JOIN LATERAL (
			SELECT ARRAY(
				SELECT b.poster_path
				FROM books b
				%s
				WHERE %s = pg.group_key
				  AND NULLIF(b.poster_path, '') IS NOT NULL
				ORDER BY %s
				LIMIT 4
			) AS poster_paths
		) posters ON TRUE
		ORDER BY %s`,
		totalSelect, strings.ReplaceAll(totalColumn, "pg.", ""), seekClause, orderClause, pageClause,
		totalColumn, keyColumn, joinClause, groupExpr, posterOrder, orderClause,
	)
	args = append(args, pageArgs...)

	return audiobookGroupsSQLPlan{
		SQL:      query,
		CountSQL: countSQL, CountArgs: countArgs,
		Args:         args,
		Limit:        limit,
		FetchLimit:   fetchLimit,
		Offset:       offset,
		IncludeTotal: q.IncludeTotal,
	}, nil
}

func audiobookGroupSearchPattern(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(raw) + "%"
}
