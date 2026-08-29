package catalog

import (
	"context"
	"fmt"
	"strings"

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
	Sort   string
	Limit  int
	Offset int
}

// AudiobookGroupsResult is the paged grouped browse response before API image
// URL resolution.
type AudiobookGroupsResult struct {
	Groups     []AudiobookGroup
	Total      int
	HasMore    bool
	TotalExact bool
}

// AudiobookGroup is one grouped browse row: an author, narrator, or series
// with aggregate stats over the audiobooks visible to the viewer.
type AudiobookGroup struct {
	Name                 string
	ItemCount            int
	TotalDurationSeconds int64
	InProgressCount      int
	FinishedCount        int
	// PosterPaths holds up to four raw poster paths for cover stacks; the
	// API layer presigns them.
	PosterPaths      []string
	PosterContentIDs []string
}

type audiobookGroupsSQLPlan struct {
	SQL          string
	Args         []any
	Limit        int
	FetchLimit   int
	Offset       int
	IncludeTotal bool
}

// audiobookGroupsPageCap bounds externally-requested pages.
const audiobookGroupsPageCap = 500

// audiobookGroupsFullCap preserves the full-list path used by the legacy
// short-lived cache; the endpoint now uses true paged queries instead.
const audiobookGroupsFullCap = 50000

// ListAudiobookGroups returns grouped browse rows for an audiobook library.
// Groups are aggregated per person name (authors/narrators) or per series
// name, matching the case-insensitive semantics of the corresponding catalog
// filters, so a group name can be fed straight back into an author/narrator/
// series filter rule. Progress counts are scoped to the requesting profile via
// filter.UserID / filter.ProfileID.
func ListAudiobookGroups(ctx context.Context, pool *pgxpool.Pool, q AudiobookGroupsQuery, filter AccessFilter) (AudiobookGroupsResult, error) {
	return listAudiobookGroupsWithLimit(ctx, pool, q, filter, audiobookGroupsPageCap)
}

// listAllAudiobookGroups fetches a complete grouped list for the older cache
// helper. New callers should prefer ListAudiobookGroups so each page can avoid
// recomputing exact totals and poster stacks for groups outside the page.
func listAllAudiobookGroups(ctx context.Context, pool *pgxpool.Pool, q AudiobookGroupsQuery, filter AccessFilter) ([]AudiobookGroup, int, error) {
	full := q
	full.Limit = audiobookGroupsFullCap
	full.Offset = 0
	full.IncludeTotal = true
	result, err := listAudiobookGroupsWithLimit(ctx, pool, full, filter, audiobookGroupsFullCap)
	if err != nil {
		return nil, 0, err
	}
	return result.Groups, result.Total, nil
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

	rows, err := pool.Query(ctx, plan.SQL, plan.Args...)
	if err != nil {
		return AudiobookGroupsResult{}, fmt.Errorf("querying audiobook groups: %w", err)
	}
	defer rows.Close()

	total := 0
	groups := make([]AudiobookGroup, 0, plan.Limit)
	for rows.Next() {
		var g AudiobookGroup
		var posterPaths []string
		var posterContentIDs []string
		if plan.IncludeTotal {
			if err := rows.Scan(&g.Name, &g.ItemCount, &g.TotalDurationSeconds, &g.InProgressCount, &g.FinishedCount, &posterPaths, &posterContentIDs, &total); err != nil {
				return AudiobookGroupsResult{}, fmt.Errorf("scanning audiobook group: %w", err)
			}
		} else if err := rows.Scan(&g.Name, &g.ItemCount, &g.TotalDurationSeconds, &g.InProgressCount, &g.FinishedCount, &posterPaths, &posterContentIDs); err != nil {
			return AudiobookGroupsResult{}, fmt.Errorf("scanning audiobook group: %w", err)
		}
		if posterPaths == nil {
			posterPaths = []string{}
		}
		g.PosterPaths = posterPaths
		g.PosterContentIDs = posterContentIDs
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return AudiobookGroupsResult{}, fmt.Errorf("iterating audiobook groups: %w", err)
	}

	hasMore := false
	if plan.IncludeTotal {
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
	if !q.IncludeTotal {
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
	case "count":
		orderClause = "item_count DESC, LOWER(name)"
	case "duration":
		orderClause = "total_duration_seconds DESC, LOWER(name)"
	default:
		return audiobookGroupsSQLPlan{}, fmt.Errorf("audiobook groups: unsupported sort %q", q.Sort)
	}

	totalSelect := ""
	totalColumn := ""
	if q.IncludeTotal {
		totalSelect = ", COUNT(*) OVER ()::int AS total_groups"
		totalColumn = ", pg.total_groups"
	}

	query := fmt.Sprintf(`
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
		),
		paged_groups AS (
			SELECT
				group_key,
				name,
				item_count,
				total_duration_seconds,
				in_progress_count,
				finished_count%s
			FROM grouped
			ORDER BY %s
			LIMIT $%d OFFSET $%d
		)
		SELECT
			pg.name,
			pg.item_count,
			pg.total_duration_seconds,
			pg.in_progress_count,
			pg.finished_count,
			COALESCE(posters.poster_paths, '{}'::text[]) AS poster_paths,
			COALESCE(posters.poster_content_ids, '{}'::text[]) AS poster_content_ids%s
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
			) AS poster_paths,
			ARRAY(
				SELECT b.content_id
				FROM books b
				%s
				WHERE %s = pg.group_key
				  AND NULLIF(b.poster_path, '') IS NOT NULL
				ORDER BY %s
				LIMIT 4
			) AS poster_content_ids
		) posters ON TRUE
		ORDER BY %s`,
		strings.Join(conditions, " AND "),
		groupExpr, nameExpr, joinClause, userArg, profileArg, groupWhereClause, groupExpr,
		totalSelect, orderClause, argIdx, argIdx+1,
		totalColumn, joinClause, groupExpr, posterOrder, joinClause, groupExpr, posterOrder, orderClause,
	)
	args = append(args, fetchLimit, offset)

	return audiobookGroupsSQLPlan{
		SQL:          query,
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
