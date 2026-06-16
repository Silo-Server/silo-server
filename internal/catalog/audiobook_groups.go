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
	LibraryID int
	GroupBy   AudiobookGroupBy
	// Sort is one of "name" (default), "count", "duration".
	Sort   string
	Limit  int
	Offset int
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
	PosterPaths []string
}

// audiobookGroupsPageCap bounds a single externally-requested page.
const audiobookGroupsPageCap = 500

// audiobookGroupsFullCap bounds the full-list fetch used to warm the groups
// cache. It is far above any real author/narrator/series count so the cached
// list is effectively complete, while still capping a pathological library.
const audiobookGroupsFullCap = 50000

// ListAudiobookGroups returns grouped browse rows for an audiobook library.
// Groups are aggregated per person name (authors/narrators) or per series
// name, matching the case-insensitive semantics of the corresponding catalog
// filters, so a group name can be fed straight back into an author/narrator/
// series filter rule. Progress counts are scoped to the requesting profile via
// filter.UserID / filter.ProfileID.
func ListAudiobookGroups(ctx context.Context, pool *pgxpool.Pool, q AudiobookGroupsQuery, filter AccessFilter) ([]AudiobookGroup, int, error) {
	return listAudiobookGroups(ctx, pool, q, filter, audiobookGroupsPageCap)
}

// listAllAudiobookGroups fetches the complete grouped list (offset 0, full cap)
// in a single query so the result can be cached and sliced per page without
// re-aggregating on every offset.
func listAllAudiobookGroups(ctx context.Context, pool *pgxpool.Pool, q AudiobookGroupsQuery, filter AccessFilter) ([]AudiobookGroup, int, error) {
	full := q
	full.Limit = audiobookGroupsFullCap
	full.Offset = 0
	return listAudiobookGroups(ctx, pool, full, filter, audiobookGroupsFullCap)
}

func listAudiobookGroups(ctx context.Context, pool *pgxpool.Pool, q AudiobookGroupsQuery, filter AccessFilter, maxLimit int) ([]AudiobookGroup, int, error) {
	if pool == nil {
		return nil, 0, fmt.Errorf("audiobook groups: no database pool")
	}
	if q.LibraryID <= 0 {
		return nil, 0, fmt.Errorf("audiobook groups: library id is required")
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := max(q.Offset, 0)

	args := []any{q.LibraryID}
	argIdx := 2

	conditions := []string{
		"mi.type = 'audiobook'",
		"mil.media_folder_id = $1",
	}
	if !appendAudiobookItemAccessConditions("mi", filter, &conditions, &args, &argIdx) {
		return []AudiobookGroup{}, 0, nil
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
		return nil, 0, fmt.Errorf("audiobook groups: unsupported group_by %q", q.GroupBy)
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
		return nil, 0, fmt.Errorf("audiobook groups: unsupported sort %q", q.Sort)
	}

	query := fmt.Sprintf(`
		WITH books AS (
			SELECT
				mi.content_id,
				mi.poster_path,
				mi.sort_title,
				COALESCE((
					SELECT SUM(mf.duration)
					FROM media_files mf
					WHERE mf.content_id = mi.content_id AND mf.missing_since IS NULL
				), 0) AS duration_seconds
			FROM media_items mi
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			WHERE %s
		),
		grouped AS (
			SELECT
				%s AS name,
				COUNT(*)::int AS item_count,
				COALESCE(SUM(b.duration_seconds), 0)::bigint AS total_duration_seconds,
				COUNT(*) FILTER (WHERE uwp.media_item_id IS NOT NULL AND NOT uwp.completed)::int AS in_progress_count,
				COUNT(*) FILTER (WHERE uwp.completed)::int AS finished_count,
				(ARRAY_AGG(b.poster_path ORDER BY %s) FILTER (WHERE NULLIF(b.poster_path, '') IS NOT NULL))[1:4] AS poster_paths
			FROM books b
			%s
			LEFT JOIN user_watch_progress uwp
			       ON uwp.media_item_id = b.content_id
			      AND uwp.user_id = %s
			      AND uwp.profile_id = %s
			GROUP BY %s
		)
		SELECT name, item_count, total_duration_seconds, in_progress_count, finished_count, poster_paths,
		       COUNT(*) OVER ()::int AS total_groups
		FROM grouped
		ORDER BY %s
		LIMIT $%d OFFSET $%d`,
		strings.Join(conditions, " AND "),
		nameExpr, posterOrder, joinClause, userArg, profileArg, groupExpr, orderClause,
		argIdx, argIdx+1,
	)
	args = append(args, limit, offset)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying audiobook groups: %w", err)
	}
	defer rows.Close()

	total := 0
	groups := make([]AudiobookGroup, 0, limit)
	for rows.Next() {
		var g AudiobookGroup
		var posterPaths []string
		if err := rows.Scan(&g.Name, &g.ItemCount, &g.TotalDurationSeconds, &g.InProgressCount, &g.FinishedCount, &posterPaths, &total); err != nil {
			return nil, 0, fmt.Errorf("scanning audiobook group: %w", err)
		}
		if posterPaths == nil {
			posterPaths = []string{}
		}
		g.PosterPaths = posterPaths
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating audiobook groups: %w", err)
	}
	return groups, total, nil
}
