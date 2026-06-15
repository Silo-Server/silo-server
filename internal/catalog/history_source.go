package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func historySourceCanUseOptimizedPageQuery(req CatalogRequest) bool {
	if req.Source != CatalogSourceHistory || !req.UseSourceOrder {
		return false
	}
	if strings.TrimSpace(req.SearchQuery) != "" || strings.TrimSpace(req.NamePrefix) != "" {
		return false
	}

	def := req.Query.Normalize()
	return def.MediaScope == "" &&
		len(def.LibraryIDs) == 0 &&
		len(def.Groups) == 0
}

func (r *CatalogResolver) resolveHistorySourcePage(
	ctx context.Context,
	req CatalogRequest,
	access AccessFilter,
) (*CatalogResult, error) {
	snapshot := time.Now().UTC()
	if req.SnapshotAt != nil {
		snapshot = *req.SnapshotAt
	}

	displayIDs, total, hasMore, err := r.loadHistoryDisplayPage(
		ctx,
		access,
		req.Limit,
		req.Offset,
		!req.SkipTotal,
		&snapshot,
	)
	if err != nil {
		return nil, err
	}

	items, err := r.fetchAccessibleItemsByID(ctx, displayIDs, req, access)
	if err != nil {
		return nil, err
	}

	return &CatalogResult{
		Items:      items,
		Total:      total,
		HasMore:    hasMore,
		TotalExact: !req.SkipTotal,
		SnapshotAt: snapshot,
	}, nil
}

func (r *CatalogResolver) loadHistoryDisplayPage(
	ctx context.Context,
	access AccessFilter,
	limit int,
	offset int,
	includeTotal bool,
	snapshot *time.Time,
) ([]string, int, bool, error) {
	if r == nil || r.itemRepo == nil || r.itemRepo.pool == nil {
		return nil, 0, false, fmt.Errorf("catalog resolver requires an item repository")
	}
	if access.UserID <= 0 || strings.TrimSpace(access.ProfileID) == "" {
		return nil, 0, false, fmt.Errorf("%w: history source requires active user scope", ErrInvalidCatalogRequest)
	}
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	baseQuery, baseArgs := buildHistoryDisplayBaseQuery(access, snapshot)

	total := 0
	if includeTotal {
		countQuery := fmt.Sprintf(`WITH history_display AS (%s) SELECT COUNT(*) FROM history_display`, baseQuery)
		if err := r.itemRepo.pool.QueryRow(ctx, countQuery, baseArgs...).Scan(&total); err != nil {
			return nil, 0, false, fmt.Errorf("counting history display rows: %w", err)
		}
		if total == 0 {
			return []string{}, 0, false, nil
		}
	}

	queryLimit := limit
	if !includeTotal {
		queryLimit++
	}

	args := append([]any{}, baseArgs...)
	limitArgIdx := len(args) + 1
	args = append(args, queryLimit)

	offsetClause := ""
	if offset > 0 {
		offsetArgIdx := len(args) + 1
		offsetClause = fmt.Sprintf(" OFFSET $%d", offsetArgIdx)
		args = append(args, offset)
	}

	pageQuery := fmt.Sprintf(
		`WITH history_display AS (%s)
		SELECT display_id
		FROM history_display
		ORDER BY watched_at DESC, display_id ASC
		LIMIT $%d%s`,
		baseQuery,
		limitArgIdx,
		offsetClause,
	)
	rows, err := r.itemRepo.pool.Query(ctx, pageQuery, args...)
	if err != nil {
		return nil, 0, false, fmt.Errorf("querying history display page: %w", err)
	}
	defer rows.Close()

	displayIDs := make([]string, 0, limit)
	for rows.Next() {
		var displayID string
		if err := rows.Scan(&displayID); err != nil {
			return nil, 0, false, fmt.Errorf("scanning history display row: %w", err)
		}
		displayIDs = append(displayIDs, displayID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, fmt.Errorf("iterating history display rows: %w", err)
	}

	hasMore := false
	if includeTotal {
		hasMore = total > offset+len(displayIDs)
		return displayIDs, total, hasMore, nil
	}
	if len(displayIDs) > limit {
		hasMore = true
		displayIDs = displayIDs[:limit]
	}
	return displayIDs, 0, hasMore, nil
}

func buildHistoryDisplayBaseQuery(access AccessFilter, snapshot *time.Time) (string, []any) {
	args := []any{access.UserID, access.ProfileID}
	argIdx := 3

	conditions := []string{
		"h.user_id = $1",
		"h.profile_id = $2",
		`NOT EXISTS (
			SELECT 1
			FROM user_history_hidden_items hhi
			WHERE hhi.user_id = h.user_id
			  AND hhi.profile_id = h.profile_id
			  AND hhi.media_item_id = h.media_item_id
			  AND h.watched_at <= hhi.hidden_before
		)`,
	}

	if snapshot != nil {
		conditions = append(conditions, fmt.Sprintf("h.watched_at <= $%d", argIdx))
		args = append(args, *snapshot)
		argIdx++
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

	if len(access.AllowedLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1
			FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id
			  AND mil.media_folder_id = ANY($%d)
		)`, argIdx))
		args = append(args, access.AllowedLibraryIDs)
		argIdx++
	} else if access.AllowedLibraryIDs != nil {
		conditions = append(conditions, "1 = 0")
	}

	if len(access.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf(`NOT EXISTS (
			SELECT 1
			FROM media_item_libraries mil_disabled
			WHERE mil_disabled.content_id = mi.content_id
			  AND mil_disabled.media_folder_id = ANY($%d)
		)`, argIdx))
		args = append(args, access.DisabledLibraryIDs)
		argIdx++
	}

	ApplySectionAccessFilter("mi", access, &conditions, &args, &argIdx)

	// display_id resolves a history row (which may be an episode) to its shown
	// item (the series, for episodes). For provider-anchored episode ids the
	// series is a pure string transform of the id (the format invariant), so we
	// skip the episodes_pkey probe entirely. Legacy Sonyflake and local episode
	// ids carry no embedded anchor, so they still fall back to the episodes
	// lookup. The join key is null-poisoned for anchored ids (= NULL is an
	// unsatisfiable b-tree scan key, so the planner never descends the index for
	// them) — an outer-only predicate like NOT LIKE would not actually skip the
	// probe.
	displayIDExpr := fmt.Sprintf(
		"COALESCE(%s, NULLIF(e.series_id, ''), h.media_item_id)",
		seriesFromAnchoredEpisodeExpr("h.media_item_id"),
	)

	// Null-poison the episodes join key for fully-formed anchored episode ids so
	// the planner skips the episodes_pkey probe for them; everything else (legacy
	// Sonyflake, local, malformed) still falls back to the lookup.
	episodeJoinKey := fmt.Sprintf(
		"CASE WHEN %s THEN NULL ELSE h.media_item_id END",
		anchoredEpisodePredicate("h.media_item_id"),
	)

	return fmt.Sprintf(
		`SELECT DISTINCT ON (history_events.display_id) history_events.display_id, history_events.watched_at
		FROM (
			SELECT %[1]s AS display_id, h.watched_at
			FROM user_watch_history h
			LEFT JOIN episodes e
				ON e.content_id = %[3]s
			JOIN media_items mi ON mi.content_id = %[1]s
			WHERE %[2]s
		) history_events
		ORDER BY history_events.display_id ASC, history_events.watched_at DESC`,
		displayIDExpr,
		strings.Join(conditions, " AND "),
		episodeJoinKey,
	), args
}

// anchoredEpisodePredicate is the SQL boolean that is TRUE only for a
// fully-formed provider-anchored episode content_id —
// episode-<provider>-<seriesId>-<season>-<episode>, i.e. five non-empty
// "-"-separated components. It deliberately rejects a broader shape like
// 'episode-broken': matching that on the prefix alone would transform it into
// 'series-broken-' and skip the episodes fallback, so the row would vanish at
// the media_items join. split_part is IMMUTABLE. The "-" delimiter matches the
// content_id format (internal/contentid); keep the two in lockstep.
func anchoredEpisodePredicate(col string) string {
	return fmt.Sprintf(
		`%[1]s LIKE 'episode-%%' `+
			`AND split_part(%[1]s, '-', 2) <> '' `+
			`AND split_part(%[1]s, '-', 3) <> '' `+
			`AND split_part(%[1]s, '-', 4) <> '' `+
			`AND split_part(%[1]s, '-', 5) <> ''`,
		col,
	)
}

// seriesFromAnchoredEpisodeExpr returns a SQL expression that recovers a show's
// content_id from a provider-anchored episode content_id by pure string
// transform — episode-<p>-<sid>-<s>-<e> -> series-<p>-<sid> — per the format
// invariant in docs/architecture/deterministic-content-id.md. It yields NULL
// for any id that is not a fully-formed provider-anchored episode (movies,
// series, local, legacy Sonyflake, or malformed episode ids), so callers
// COALESCE to the episodes-table lookup for those. split_part/||/CASE are all
// IMMUTABLE.
func seriesFromAnchoredEpisodeExpr(col string) string {
	return fmt.Sprintf(
		`CASE WHEN %[2]s `+
			`THEN 'series-' || split_part(%[1]s, '-', 2) || '-' || split_part(%[1]s, '-', 3) END`,
		col,
		anchoredEpisodePredicate(col),
	)
}
