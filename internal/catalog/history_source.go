package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	historyDateViewedSort = "date_viewed"
	historyAscendingOrder = "asc"
)

func historyDateViewedAscending(req CatalogRequest) bool {
	return req.Source == CatalogSourceHistory && req.Query.Sort.Field == historyDateViewedSort && req.Query.Sort.Order == historyAscendingOrder
}

// All history pages fence watch events at one timestamp. Filter and limit in
// SQL so a small page never hydrates the viewer's entire history.
func (r *CatalogResolver) resolveHistoryQueryPage(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	if access.UserID <= 0 || strings.TrimSpace(access.ProfileID) == "" {
		return nil, fmt.Errorf("%w: history source requires active user scope", ErrInvalidCatalogRequest)
	}
	if req.SnapshotAt == nil {
		req.SnapshotAt = new(time.Now().UTC())
	}
	build, err := r.buildHistoryPreviewPagePlan(req, access)
	if err != nil {
		return nil, err
	}

	// Match the existing catalog search fallback: if no strict substring match
	// exists anywhere in the filtered source, keep its existing order. Probe
	// before pagination so an empty later page cannot trigger the fallback.
	if strings.TrimSpace(req.SearchQuery) != "" && eligibleForFuzzy(parseSearchQuery(req.SearchQuery)) {
		existsSQL := fmt.Sprintf("WITH %s SELECT EXISTS (SELECT 1 %s %s)", strings.Join(build.ctes, ",\n"), build.fromClauseCount, build.whereClause)
		args := append(append([]any{}, build.cteArgs...), build.args...)
		var matched bool
		if err := r.itemRepo.pool.QueryRow(ctx, existsSQL, args...).Scan(&matched); err != nil {
			return nil, fmt.Errorf("checking history search matches: %w", err)
		}
		if !matched {
			req.SearchQuery = ""
			build, err = r.buildHistoryPreviewPagePlan(req, access)
			if err != nil {
				return nil, err
			}
		}
	}

	items, total, hasMore, err := r.queryExecutorForScope(req.Query.MediaScope, nil).executePreviewPagePlan(ctx, build, !req.SkipTotal)
	if err != nil {
		return nil, err
	}
	return &CatalogResult{
		Items: items, Total: total, HasMore: hasMore,
		TotalExact: !req.SkipTotal, SnapshotAt: *req.SnapshotAt,
	}, nil
}

func (r *CatalogResolver) buildHistoryPreviewPagePlan(req CatalogRequest, access AccessFilter) (previewPagePlan, error) {
	def := req.Query
	useHistoryOrder := req.UseSourceOrder || def.Sort.Field == historyDateViewedSort
	if useHistoryOrder {
		def.Sort = QuerySort{}
	}
	build, err := r.queryExecutorForScope(def.MediaScope, nil).buildPreviewPagePlan(def, access, req.Limit, req.Offset)
	if err != nil {
		return previewPagePlan{}, err
	}
	if useHistoryOrder {
		build.fromClausePaged = build.fromClauseCount
		build.limitArgIdx -= len(build.sortArgs)
		build.sortArgs = nil
		build.orderBy = "ORDER BY history_source.watched_at DESC, mi.content_id ASC"
		if historyDateViewedAscending(req) {
			build.orderBy = "ORDER BY history_source.watched_at ASC, mi.content_id ASC"
		}
	}

	historySQL, prefixArgs := buildHistoryDisplayBaseQuery(access, req.SnapshotAt, isEpisodeCatalogScope(def.MediaScope))
	var searchConditions []string
	query := strings.TrimSpace(req.SearchQuery)
	if query != "" {
		parsed := parseSearchQuery(query)
		needle := normalizeTitleForComparison(firstNonEmptySearchValue(parsed.Text, query))
		for token := range strings.FieldsSeq(needle) {
			prefixArgs = append(prefixArgs, token)
			searchConditions = append(searchConditions, fmt.Sprintf("strpos(public.normalize_search_text(concat_ws(' ', mi.title, mi.sort_title, mi.original_title, mi.overview)), $%d) > 0", len(prefixArgs)))
		}
	}
	if prefix := strings.ToLower(strings.TrimSpace(req.NamePrefix)); prefix != "" {
		prefixArgs = append(prefixArgs, prefix)
		searchConditions = append(searchConditions, fmt.Sprintf("(starts_with(lower(btrim(mi.title)), $%d) OR starts_with(lower(btrim(mi.sort_title)), $%d))", len(prefixArgs), len(prefixArgs)))
	}

	// Put history/search arguments before the existing plan's CTE, filter, and
	// sort arguments; every existing placeholder moves by the same amount.
	shift := len(prefixArgs)
	ctes := []string{"history_display AS (" + historySQL + ")"}
	for _, cte := range build.ctes {
		ctes = append(ctes, rebindSQLPlaceholders(cte, shift))
	}
	build.ctes = ctes
	build.cteArgs = append(prefixArgs, build.cteArgs...)
	const historyJoin = " JOIN history_display history_source ON history_source.display_id = mi.content_id"
	build.fromClauseCount = rebindSQLPlaceholders(build.fromClauseCount, shift) + historyJoin
	build.fromClausePaged = rebindSQLPlaceholders(build.fromClausePaged, shift) + historyJoin
	build.whereClause = rebindSQLPlaceholders(build.whereClause, shift)
	build.orderBy = rebindSQLPlaceholders(build.orderBy, shift)
	build.limitArgIdx += shift
	if len(searchConditions) > 0 {
		build.whereClause += " AND " + strings.Join(searchConditions, " AND ")
	}
	return build, nil
}

// Keep the full history scope inside each facet query, including profile,
// hidden-event, snapshot, and episode/display identity constraints.
func scopeHistoryFacetFilters(filters *BrowseFilters, req CatalogRequest, access AccessFilter) {
	if req.SnapshotAt == nil {
		req.SnapshotAt = new(time.Now().UTC())
	}
	baseQuery, args := buildHistoryDisplayBaseQuery(access, req.SnapshotAt, isEpisodeCatalogScope(req.Query.MediaScope))
	filters.contentSourceSQL = "SELECT display_id FROM (" + baseQuery + ") history_source"
	filters.contentSourceArgs = args
}

func buildHistoryDisplayBaseQuery(access AccessFilter, snapshot *time.Time, episodeScope bool) (string, []any) {
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
			contentIDExpr := "mi.content_id"
			if episodeScope {
				contentIDExpr = "h.media_item_id"
			}
			conditions = append(conditions, fmt.Sprintf("%s = ANY($%d)", contentIDExpr, argIdx))
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

	historyIDExpr := displayIDExpr
	if episodeScope {
		historyIDExpr = "h.media_item_id"
	}

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
			SELECT %[4]s AS display_id, h.watched_at
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
		historyIDExpr,
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
