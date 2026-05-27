package catalog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/models"
)

type episodeCatalogEntryPageRow struct {
	episodeID string
	addedAt   time.Time
}

type episodeCatalogUserStatePlan struct {
	entryDef             QueryDefinition
	source               string
	alias                string
	positiveSource       bool
	userClauses          []string
	userArgs             []any
	sortField            string
	sortOrder            string
	sortOnly             bool
	requiresFullPageGate bool
	requireProgressRatio bool
}

func (e *QueryExecutor) tryEpisodeCatalogUserStatePreviewPage(
	ctx context.Context,
	def QueryDefinition,
	access AccessFilter,
	limit int,
	offset int,
	includeTotal bool,
) ([]*models.MediaItem, int, bool, bool, error) {
	def = def.Normalize()
	effectiveScope := e.Scope
	if effectiveScope == "" {
		effectiveScope = def.MediaScope
	}
	if !isEpisodeCatalogScope(effectiveScope) {
		return nil, 0, false, false, nil
	}
	if access.UserID == 0 || strings.TrimSpace(access.ProfileID) == "" {
		return nil, 0, false, false, nil
	}

	plan, ok, err := extractEpisodeCatalogUserStatePlan(def)
	if err != nil || !ok {
		return nil, 0, false, ok, err
	}
	if includeTotal && plan.sortOnly {
		return nil, 0, false, false, nil
	}

	libraryID, empty, ok := singleEpisodeCatalogLibraryID(def, access)
	if !ok {
		return nil, 0, false, false, nil
	}
	if empty {
		return []*models.MediaItem{}, 0, false, true, nil
	}

	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	ctes := []string{episodeCatalogUserStateCTE(plan)}
	args := []any{access.UserID, access.ProfileID, libraryID}
	argIdx := 4
	whereParts := []string{"ece.media_folder_id = $3"}

	if access.AllowedContentIDs != nil {
		if len(access.AllowedContentIDs) == 0 {
			return []*models.MediaItem{}, 0, false, true, nil
		}
		whereParts = append(whereParts, fmt.Sprintf("ece.episode_id = ANY($%d)", argIdx))
		args = append(args, access.AllowedContentIDs)
		argIdx++
	}

	ApplySectionAccessFilter("ece", access, &whereParts, &args, &argIdx)

	if e.SnapshotAt != nil {
		whereParts = append(whereParts, fmt.Sprintf("ece.episode_created_at <= $%d", argIdx))
		args = append(args, *e.SnapshotAt)
		argIdx++
	}

	if prefix := strings.TrimSpace(access.NamePrefix); prefix != "" {
		whereParts = append(whereParts, fmt.Sprintf(
			"(ece.sort_key LIKE $%d ESCAPE '\\' OR LOWER(ece.title) LIKE $%d ESCAPE '\\')",
			argIdx,
			argIdx,
		))
		args = append(args, escapePrefixForLike(prefix)+"%")
		argIdx++
	}

	queryWhere, queryArgs, nextArgIdx, ok, err := buildEpisodeCatalogEntryQueryWhere(plan.entryDef, argIdx)
	if err != nil || !ok {
		return nil, 0, false, ok, err
	}
	if queryWhere != "" {
		whereParts = append(whereParts, queryWhere)
		args = append(args, queryArgs...)
		argIdx = nextArgIdx
	}

	userClauses := rebindUserStateClauses(plan.userClauses, argIdx)
	whereParts = append(whereParts, userClauses...)
	args = append(args, plan.userArgs...)
	argIdx += len(plan.userArgs)

	fromClause := episodeCatalogUserStateFromClause(plan)
	orderBy, ok := episodeCatalogUserStateOrderBy(plan)
	if !ok {
		return nil, 0, false, false, nil
	}
	whereClause := "WHERE " + strings.Join(whereParts, " AND ")

	pageLimit := limit + 1
	pageArgs := append([]any{}, args...)
	limitArgIdx := argIdx
	pageArgs = append(pageArgs, pageLimit)
	offsetClause := ""
	if offset > 0 {
		offsetClause = fmt.Sprintf(" OFFSET $%d", limitArgIdx+1)
		pageArgs = append(pageArgs, offset)
	}

	pageSQL := fmt.Sprintf(
		`WITH %s
		SELECT ece.episode_id, ece.added_at
		%s
		%s
		%s
		LIMIT $%d%s`,
		strings.Join(ctes, ",\n"),
		fromClause,
		whereClause,
		orderBy,
		limitArgIdx,
		offsetClause,
	)

	pageRows, err := e.queryEpisodeCatalogEntryPage(ctx, pageSQL, pageArgs...)
	if err != nil {
		if episodeCatalogEntriesUnavailable(err) {
			return nil, 0, false, false, nil
		}
		return nil, 0, false, true, err
	}
	if plan.requiresFullPageGate && len(pageRows) < pageLimit {
		return nil, 0, false, false, nil
	}

	hasMore := len(pageRows) > limit
	if hasMore {
		pageRows = pageRows[:limit]
	}

	items, err := e.hydrateEpisodeCatalogEntryPage(ctx, pageRows)
	if err != nil {
		if episodeCatalogEntriesUnavailable(err) {
			return nil, 0, false, false, nil
		}
		return nil, 0, false, true, err
	}

	total := 0
	if includeTotal {
		countSQL := fmt.Sprintf(
			`WITH %s
			SELECT COUNT(*)
			%s
			%s`,
			strings.Join(ctes, ",\n"),
			fromClause,
			whereClause,
		)
		if err := e.Pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
			if episodeCatalogEntriesUnavailable(err) {
				return nil, 0, false, false, nil
			}
			return nil, 0, false, true, fmt.Errorf("counting episode user-state catalog entries: %w", err)
		}
		hasMore = total > offset+len(items)
	}

	return items, total, hasMore, true, nil
}

func (e *QueryExecutor) tryEpisodeCatalogEntriesPreviewPage(
	ctx context.Context,
	def QueryDefinition,
	access AccessFilter,
	limit int,
	offset int,
	includeTotal bool,
) ([]*models.MediaItem, int, bool, bool, error) {
	def = def.Normalize()
	effectiveScope := e.Scope
	if effectiveScope == "" {
		effectiveScope = def.MediaScope
	}
	if !isEpisodeCatalogScope(effectiveScope) {
		return nil, 0, false, false, nil
	}

	allowPersonalized := strings.TrimSpace(access.ProfileID) != ""
	if err := def.ValidateWithOptions(allowPersonalized, allowPersonalized); err != nil {
		return nil, 0, false, true, err
	}

	libraryID, empty, ok := singleEpisodeCatalogLibraryID(def, access)
	if !ok {
		return nil, 0, false, false, nil
	}
	if empty {
		return []*models.MediaItem{}, 0, false, true, nil
	}

	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	whereParts := []string{"ece.media_folder_id = $1"}
	args := []any{libraryID}
	argIdx := 2

	if access.AllowedContentIDs != nil {
		if len(access.AllowedContentIDs) == 0 {
			return []*models.MediaItem{}, 0, false, true, nil
		}
		whereParts = append(whereParts, fmt.Sprintf("ece.episode_id = ANY($%d)", argIdx))
		args = append(args, access.AllowedContentIDs)
		argIdx++
	}

	ApplySectionAccessFilter("ece", access, &whereParts, &args, &argIdx)

	if e.SnapshotAt != nil {
		whereParts = append(whereParts, fmt.Sprintf("ece.episode_created_at <= $%d", argIdx))
		args = append(args, *e.SnapshotAt)
		argIdx++
	}

	if prefix := strings.TrimSpace(access.NamePrefix); prefix != "" {
		whereParts = append(whereParts, fmt.Sprintf(
			"(ece.sort_key LIKE $%d ESCAPE '\\' OR LOWER(ece.title) LIKE $%d ESCAPE '\\')",
			argIdx,
			argIdx,
		))
		args = append(args, escapePrefixForLike(prefix)+"%")
		argIdx++
	}

	queryWhere, queryArgs, nextArgIdx, ok, err := buildEpisodeCatalogEntryQueryWhere(def, argIdx)
	if err != nil {
		return nil, 0, false, true, err
	}
	if !ok {
		return nil, 0, false, false, nil
	}
	if queryWhere != "" {
		whereParts = append(whereParts, queryWhere)
		args = append(args, queryArgs...)
		argIdx = nextArgIdx
	}

	orderBy, ok := episodeCatalogEntryOrderBy(def.Sort)
	if !ok {
		return nil, 0, false, false, nil
	}

	whereClause := "WHERE " + strings.Join(whereParts, " AND ")
	pageLimit := limit + 1
	pageArgs := append([]any{}, args...)
	limitArgIdx := argIdx
	pageArgs = append(pageArgs, pageLimit)
	offsetClause := ""
	if offset > 0 {
		offsetClause = fmt.Sprintf(" OFFSET $%d", limitArgIdx+1)
		pageArgs = append(pageArgs, offset)
	}

	pageSQL := fmt.Sprintf(
		`SELECT ece.episode_id, ece.added_at
		FROM episode_catalog_entries ece
		%s
		%s
		LIMIT $%d%s`,
		whereClause,
		orderBy,
		limitArgIdx,
		offsetClause,
	)

	pageRows, err := e.queryEpisodeCatalogEntryPage(ctx, pageSQL, pageArgs...)
	if err != nil {
		if episodeCatalogEntriesUnavailable(err) {
			return nil, 0, false, false, nil
		}
		return nil, 0, false, true, err
	}

	hasMore := len(pageRows) > limit
	if hasMore {
		pageRows = pageRows[:limit]
	}

	items, err := e.hydrateEpisodeCatalogEntryPage(ctx, pageRows)
	if err != nil {
		if episodeCatalogEntriesUnavailable(err) {
			return nil, 0, false, false, nil
		}
		return nil, 0, false, true, err
	}

	total := 0
	if includeTotal {
		countSQL := fmt.Sprintf(
			`SELECT COUNT(*)
			FROM episode_catalog_entries ece
			%s`,
			whereClause,
		)
		if err := e.Pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
			if episodeCatalogEntriesUnavailable(err) {
				return nil, 0, false, false, nil
			}
			return nil, 0, false, true, fmt.Errorf("counting episode catalog entries: %w", err)
		}
		hasMore = total > offset+len(items)
	}

	return items, total, hasMore, true, nil
}

func extractEpisodeCatalogUserStatePlan(def QueryDefinition) (episodeCatalogUserStatePlan, bool, error) {
	plan := episodeCatalogUserStatePlan{
		entryDef:  def,
		sortField: NormalizeQuerySort(def.Sort).Field,
		sortOrder: NormalizeQuerySort(def.Sort).Order,
	}
	switch plan.sortField {
	case "date_viewed", "plays":
		plan.source = "viewed"
		plan.alias = "uv"
		plan.positiveSource = true
		plan.sortOnly = true
		plan.requiresFullPageGate = true
	case "progress":
		plan.source = "progress"
		plan.alias = "up"
		plan.positiveSource = true
		plan.sortOnly = true
		plan.requiresFullPageGate = true
	}

	if len(def.Groups) == 0 {
		if plan.source == "" {
			return episodeCatalogUserStatePlan{}, false, nil
		}
		plan.requireProgressRatio = plan.source == "progress" && plan.sortOnly
		return plan, true, nil
	}
	if def.Match != "all" {
		return episodeCatalogUserStatePlan{}, false, nil
	}

	filteredGroups := make([]QueryGroup, 0, len(def.Groups))
	for _, group := range def.Groups {
		if group.Match != "all" {
			for _, rule := range group.Rules {
				if episodeCatalogUserStateRule(rule) {
					return episodeCatalogUserStatePlan{}, false, nil
				}
			}
			filteredGroups = append(filteredGroups, group)
			continue
		}

		filteredRules := make([]QueryRule, 0, len(group.Rules))
		for _, rule := range group.Rules {
			source, alias, positive, clause, args, ok, err := buildEpisodeCatalogUserStateRule(rule)
			if err != nil {
				return episodeCatalogUserStatePlan{}, true, err
			}
			if !ok {
				filteredRules = append(filteredRules, rule)
				continue
			}
			if plan.source != "" && plan.source != source {
				return episodeCatalogUserStatePlan{}, false, nil
			}
			plan.source = source
			plan.alias = alias
			plan.positiveSource = positive
			plan.userClauses = append(plan.userClauses, clause)
			plan.userArgs = append(plan.userArgs, args...)
			plan.sortOnly = false
			plan.requiresFullPageGate = false
		}
		if len(filteredRules) > 0 {
			group.Rules = filteredRules
			filteredGroups = append(filteredGroups, group)
		}
	}
	if plan.source == "" {
		return episodeCatalogUserStatePlan{}, false, nil
	}
	plan.entryDef.Groups = filteredGroups
	plan.requireProgressRatio = plan.source == "progress" && plan.sortOnly
	return plan, true, nil
}

func episodeCatalogUserStateRule(rule QueryRule) bool {
	switch rule.Field {
	case "watched", "in_progress", "last_watched":
		return true
	default:
		return false
	}
}

func buildEpisodeCatalogUserStateRule(rule QueryRule) (string, string, bool, string, []any, bool, error) {
	switch rule.Field {
	case "watched":
		value, ok := rule.Value.(bool)
		if !ok {
			return "", "", false, "", nil, true, fmt.Errorf("watched requires a boolean value")
		}
		if value {
			return "viewed", "uv", true, "uv.episode_id IS NOT NULL", nil, true, nil
		}
		return "viewed", "uv", false, "uv.episode_id IS NULL", nil, true, nil
	case "in_progress":
		value, ok := rule.Value.(bool)
		if !ok {
			return "", "", false, "", nil, true, fmt.Errorf("in_progress requires a boolean value")
		}
		if value {
			return "progress", "up", true, "up.episode_id IS NOT NULL", nil, true, nil
		}
		return "progress", "up", false, "up.episode_id IS NULL", nil, true, nil
	case "last_watched":
		switch rule.Op {
		case "gt", "gte", "between", "in_last":
		default:
			return "", "", false, "", nil, false, nil
		}
		clause, args, err := buildEpisodeCatalogLastWatchedClause(rule)
		if err != nil {
			return "", "", false, "", nil, true, err
		}
		return "viewed", "uv", true, clause, args, true, nil
	default:
		return "", "", false, "", nil, false, nil
	}
}

func buildEpisodeCatalogLastWatchedClause(rule QueryRule) (string, []any, error) {
	switch rule.Op {
	case "gt", "gte":
		operator := map[string]string{"gt": ">", "gte": ">="}[rule.Op]
		return "uv.last_watched " + operator + " $%d::timestamptz", []any{rule.Value}, nil
	case "between":
		values, err := toBetweenValues(rule.Value)
		if err != nil {
			return "", nil, fmt.Errorf("between requires [min, max] array: %w", err)
		}
		return "uv.last_watched >= $%d::timestamptz AND uv.last_watched <= $%d::timestamptz", []any{values[0], values[1]}, nil
	case "in_last":
		duration, ok := rule.Value.(string)
		if !ok {
			return "", nil, fmt.Errorf("in_last requires a duration string like '30d'")
		}
		interval, err := parseDuration(duration)
		if err != nil {
			return "", nil, err
		}
		return "uv.last_watched >= NOW() - INTERVAL '" + interval + "'", nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported last_watched operator %q", rule.Op)
	}
}

func rebindUserStateClauses(clauses []string, argIdx int) []string {
	if len(clauses) == 0 {
		return nil
	}
	out := make([]string, len(clauses))
	nextArg := argIdx
	for i, clause := range clauses {
		for strings.Contains(clause, "$%d") {
			clause = strings.Replace(clause, "$%d", "$"+strconv.Itoa(nextArg), 1)
			nextArg++
		}
		out[i] = clause
	}
	return out
}

func episodeCatalogUserStateCTE(plan episodeCatalogUserStatePlan) string {
	switch plan.source {
	case "progress":
		progressRatioGate := ""
		if plan.requireProgressRatio {
			progressRatioGate = `
				  AND COALESCE(uwp.duration_seconds, 0) > 0`
		}
		return fmt.Sprintf(`user_progress AS (
				SELECT uwp.media_item_id AS episode_id,
					uwp.position_seconds::double precision / NULLIF(uwp.duration_seconds, 0) AS progress_ratio
				FROM user_watch_progress uwp
			LEFT JOIN user_history_hidden_items hhi
				ON hhi.user_id = $1
			   AND hhi.profile_id = $2
			   AND hhi.media_item_id = uwp.media_item_id
			WHERE uwp.user_id = $1
				  AND uwp.profile_id = $2
				  AND uwp.completed = FALSE
				  AND uwp.position_seconds > 0%s
				  AND (hhi.media_item_id IS NULL OR uwp.updated_at > hhi.hidden_before)
			)`, progressRatioGate)
	default:
		return `user_viewed AS (
			SELECT src.episode_id,
				MAX(src.last_watched) AS last_watched,
				NULLIF(GREATEST(
					COALESCE(MAX(src.history_play_count), 0),
					COALESCE(MAX(src.progress_play_count), 0)
				), 0) AS play_count
			FROM (
				SELECT uwh.media_item_id AS episode_id,
					MAX(uwh.watched_at) AS last_watched,
					COUNT(*)::integer AS history_play_count,
					NULL::integer AS progress_play_count
				FROM user_watch_history uwh
				LEFT JOIN user_history_hidden_items hhi
					ON hhi.user_id = $1
				   AND hhi.profile_id = $2
				   AND hhi.media_item_id = uwh.media_item_id
				WHERE uwh.user_id = $1
				  AND uwh.profile_id = $2
				  AND uwh.completed = TRUE
				  AND (hhi.media_item_id IS NULL OR uwh.watched_at > hhi.hidden_before)
				GROUP BY uwh.media_item_id
				UNION ALL
				SELECT uwp.media_item_id AS episode_id,
					uwp.updated_at AS last_watched,
					NULL::integer AS history_play_count,
					1 AS progress_play_count
				FROM user_watch_progress uwp
				LEFT JOIN user_history_hidden_items hhi
					ON hhi.user_id = $1
				   AND hhi.profile_id = $2
				   AND hhi.media_item_id = uwp.media_item_id
				WHERE uwp.user_id = $1
				  AND uwp.profile_id = $2
				  AND uwp.completed = TRUE
				  AND (hhi.media_item_id IS NULL OR uwp.updated_at > hhi.hidden_before)
			) src
			GROUP BY src.episode_id
		)`
	}
}

func episodeCatalogUserStateFromClause(plan episodeCatalogUserStatePlan) string {
	tableName := "user_viewed"
	if plan.source == "progress" {
		tableName = "user_progress"
	}
	if plan.positiveSource {
		return fmt.Sprintf(
			"FROM %s %s JOIN episode_catalog_entries ece ON ece.episode_id = %s.episode_id",
			tableName,
			plan.alias,
			plan.alias,
		)
	}
	return fmt.Sprintf(
		"FROM episode_catalog_entries ece LEFT JOIN %s %s ON %s.episode_id = ece.episode_id",
		tableName,
		plan.alias,
		plan.alias,
	)
}

func episodeCatalogUserStateOrderBy(plan episodeCatalogUserStatePlan) (string, bool) {
	dir := "DESC"
	if plan.sortOrder == "asc" {
		dir = "ASC"
	}
	switch plan.sortField {
	case "date_viewed":
		return fmt.Sprintf("ORDER BY uv.last_watched %s NULLS LAST, ece.sort_key ASC, ece.episode_id ASC", dir), true
	case "plays":
		return fmt.Sprintf("ORDER BY uv.play_count %s NULLS LAST, ece.sort_key ASC, ece.episode_id ASC", dir), true
	case "progress":
		return fmt.Sprintf("ORDER BY up.progress_ratio %s NULLS LAST, ece.sort_key ASC, ece.episode_id ASC", dir), true
	default:
		return episodeCatalogEntryOrderBy(QuerySort{Field: plan.sortField, Order: plan.sortOrder})
	}
}

func (e *QueryExecutor) queryEpisodeCatalogEntryPage(
	ctx context.Context,
	sql string,
	args ...any,
) ([]episodeCatalogEntryPageRow, error) {
	rows, err := e.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("querying episode catalog entry page: %w", err)
	}
	defer rows.Close()

	pageRows := make([]episodeCatalogEntryPageRow, 0)
	for rows.Next() {
		var row episodeCatalogEntryPageRow
		if err := rows.Scan(&row.episodeID, &row.addedAt); err != nil {
			return nil, fmt.Errorf("scanning episode catalog entry page: %w", err)
		}
		pageRows = append(pageRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating episode catalog entry page: %w", err)
	}
	return pageRows, nil
}

func (e *QueryExecutor) hydrateEpisodeCatalogEntryPage(
	ctx context.Context,
	pageRows []episodeCatalogEntryPageRow,
) ([]*models.MediaItem, error) {
	if len(pageRows) == 0 {
		return []*models.MediaItem{}, nil
	}

	ids := make([]string, 0, len(pageRows))
	addedAtByID := make(map[string]time.Time, len(pageRows))
	for _, row := range pageRows {
		ids = append(ids, row.episodeID)
		addedAtByID[row.episodeID] = row.addedAt
	}

	relation := fmt.Sprintf(episodeCatalogSelectBody, "e.content_id = ANY($1)")
	sql := "SELECT " + qualifiedListItemColumns("mi") + " FROM " + relation
	rows, err := e.Pool.Query(ctx, sql, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrating episode catalog entry page: %w", err)
	}
	defer rows.Close()

	hydrated, err := scanItems(rows)
	if err != nil {
		return nil, err
	}

	itemsByID := make(map[string]*models.MediaItem, len(hydrated))
	for _, item := range hydrated {
		if item == nil {
			continue
		}
		if addedAt, ok := addedAtByID[item.ContentID]; ok {
			t := addedAt
			item.AddedAt = &t
		} else if item.AddedAt == nil && !item.CreatedAt.IsZero() {
			t := item.CreatedAt
			item.AddedAt = &t
		}
		itemsByID[item.ContentID] = item
	}

	ordered := make([]*models.MediaItem, 0, len(pageRows))
	for _, row := range pageRows {
		if item := itemsByID[row.episodeID]; item != nil {
			ordered = append(ordered, item)
		}
	}
	return ordered, nil
}

func singleEpisodeCatalogLibraryID(def QueryDefinition, access AccessFilter) (int, bool, bool) {
	libraryIDs := append([]int(nil), def.LibraryIDs...)
	if access.AllowedLibraryIDs != nil {
		if len(libraryIDs) == 0 {
			libraryIDs = append([]int(nil), access.AllowedLibraryIDs...)
		} else {
			libraryIDs = intersectInts(libraryIDs, access.AllowedLibraryIDs)
		}
	}
	if len(libraryIDs) == 0 {
		if access.AllowedLibraryIDs != nil {
			return 0, true, true
		}
		return 0, false, false
	}
	if len(libraryIDs) != 1 {
		return 0, false, false
	}
	libraryID := libraryIDs[0]
	if libraryID <= 0 {
		return 0, true, true
	}
	for _, disabledID := range access.DisabledLibraryIDs {
		if disabledID == libraryID {
			return 0, true, true
		}
	}
	return libraryID, false, true
}

func buildEpisodeCatalogEntryQueryWhere(def QueryDefinition, argIdx int) (string, []any, int, bool, error) {
	if len(def.Groups) == 0 {
		return "", nil, argIdx, true, nil
	}

	topJoiner := " AND "
	if def.Match == "any" {
		topJoiner = " OR "
	}

	var args []any
	var groupClauses []string
	for _, group := range def.Groups {
		clause, groupArgs, nextArgIdx, ok, err := buildEpisodeCatalogEntryGroupWhere(group, argIdx)
		if err != nil || !ok {
			return "", nil, argIdx, ok, err
		}
		argIdx = nextArgIdx
		args = append(args, groupArgs...)
		if clause != "" {
			groupClauses = append(groupClauses, clause)
		}
	}

	switch len(groupClauses) {
	case 0:
		return "", args, argIdx, true, nil
	case 1:
		return groupClauses[0], args, argIdx, true, nil
	default:
		wrapped := make([]string, len(groupClauses))
		for i, clause := range groupClauses {
			wrapped[i] = "(" + clause + ")"
		}
		return strings.Join(wrapped, topJoiner), args, argIdx, true, nil
	}
}

func buildEpisodeCatalogEntryGroupWhere(group QueryGroup, argIdx int) (string, []any, int, bool, error) {
	if len(group.Rules) == 0 {
		return "", nil, argIdx, true, nil
	}
	if group.Match == "all" && countSameFileTechnicalRules(group.Rules) > 1 {
		return "", nil, argIdx, false, nil
	}

	joiner := " AND "
	if group.Match == "any" {
		joiner = " OR "
	}

	var args []any
	var clauses []string
	for _, rule := range group.Rules {
		clause, ruleArgs, nextArgIdx, ok, err := buildEpisodeCatalogEntryRuleWhere(rule, argIdx)
		if err != nil || !ok {
			return "", nil, argIdx, ok, err
		}
		argIdx = nextArgIdx
		args = append(args, ruleArgs...)
		if clause != "" {
			clauses = append(clauses, clause)
		}
	}
	if len(clauses) == 0 {
		return "", args, argIdx, true, nil
	}
	return strings.Join(clauses, joiner), args, argIdx, true, nil
}

func buildEpisodeCatalogEntryRuleWhere(rule QueryRule, argIdx int) (string, []any, int, bool, error) {
	switch rule.Field {
	case "type":
		return buildEpisodeCatalogTypeClause(rule, argIdx)
	case "genre":
		return buildEpisodeCatalogArrayClause("ece.genres", rule, argIdx)
	case "studio":
		return buildEpisodeCatalogArrayClause("ece.studios", rule, argIdx)
	case "network":
		return buildEpisodeCatalogArrayClause("ece.networks", rule, argIdx)
	case "country":
		return buildEpisodeCatalogArrayClause("ece.countries", rule, argIdx)
	case "year":
		return buildEpisodeCatalogComparisonClause("ece.year", rule, argIdx, "")
	case "rating_imdb":
		return buildEpisodeCatalogComparisonClause("ece.rating_imdb", rule, argIdx, "")
	case "original_language":
		return buildEpisodeCatalogEqualityClause("ece.original_language", rule, argIdx)
	case "content_rating":
		return buildEpisodeCatalogEqualityClause("ece.content_rating", rule, argIdx)
	case "added_at":
		return buildEpisodeCatalogComparisonClause("ece.episode_created_at", rule, argIdx, "timestamptz")
	case "release_date":
		return buildEpisodeCatalogComparisonClause("ece.episode_air_date", rule, argIdx, "date")
	case "status":
		return buildEpisodeCatalogEqualityClause("ece.status", rule, argIdx)
	case "resolution":
		return buildEpisodeCatalogResolutionClause(rule, argIdx)
	case "hdr":
		return buildEpisodeCatalogBoolPresenceClause("ece.has_hdr", "ece.has_non_hdr", rule, argIdx)
	case "dolby_vision":
		return buildEpisodeCatalogBoolPresenceClause("ece.has_dolby_vision", "ece.has_non_dolby_vision", rule, argIdx)
	case "bitrate":
		return buildEpisodeCatalogBitrateClause(rule, argIdx)
	case "audio_language":
		return buildEpisodeCatalogLanguageArrayClause("ece.audio_language_codes", rule, argIdx)
	case "subtitle_language":
		return buildEpisodeCatalogLanguageArrayClause("ece.subtitle_language_codes", rule, argIdx)
	case "actor", "director", "writer", "producer", "watched", "favorited", "in_watchlist", "in_progress", "last_watched":
		return "", nil, argIdx, false, nil
	default:
		return "", nil, argIdx, false, nil
	}
}

func buildEpisodeCatalogTypeClause(rule QueryRule, argIdx int) (string, []any, int, bool, error) {
	value, ok := rule.Value.(string)
	if !ok {
		return "", nil, argIdx, true, fmt.Errorf("type requires a string value")
	}
	isEpisode := strings.EqualFold(strings.TrimSpace(value), "episode")
	switch rule.Op {
	case "is":
		if isEpisode {
			return "1 = 1", nil, argIdx, true, nil
		}
		return "1 = 0", nil, argIdx, true, nil
	case "is_not":
		if isEpisode {
			return "1 = 0", nil, argIdx, true, nil
		}
		return "1 = 1", nil, argIdx, true, nil
	default:
		return "", nil, argIdx, true, fmt.Errorf("unsupported type operator %q", rule.Op)
	}
}

func buildEpisodeCatalogArrayClause(column string, rule QueryRule, argIdx int) (string, []any, int, bool, error) {
	if rule.Op != "is" && rule.Op != "is_not" && rule.Op != "contains" {
		return "", nil, argIdx, true, fmt.Errorf("unsupported array operator %q", rule.Op)
	}
	clause := fmt.Sprintf("%s @> ARRAY[$%d]::text[]", column, argIdx)
	if rule.Op == "is_not" {
		clause = "NOT (" + clause + ")"
	}
	return clause, []any{rule.Value}, argIdx + 1, true, nil
}

func buildEpisodeCatalogLanguageArrayClause(column string, rule QueryRule, argIdx int) (string, []any, int, bool, error) {
	value, ok := rule.Value.(string)
	if !ok {
		return "", nil, argIdx, true, fmt.Errorf("%s requires a string value", rule.Field)
	}
	rule.Value = strings.ToLower(strings.TrimSpace(value))
	return buildEpisodeCatalogArrayClause(column, rule, argIdx)
}

func buildEpisodeCatalogResolutionClause(rule QueryRule, argIdx int) (string, []any, int, bool, error) {
	value, ok := rule.Value.(string)
	if !ok {
		return "", nil, argIdx, true, fmt.Errorf("resolution requires a string value")
	}
	rule.Value = normalizeResolutionValue(value)
	return buildEpisodeCatalogArrayClause("ece.resolution_codes", rule, argIdx)
}

func buildEpisodeCatalogEqualityClause(column string, rule QueryRule, argIdx int) (string, []any, int, bool, error) {
	if rule.Op != "is" && rule.Op != "is_not" {
		return "", nil, argIdx, true, fmt.Errorf("unsupported equality operator %q", rule.Op)
	}
	clause := fmt.Sprintf("%s = $%d", column, argIdx)
	if rule.Op == "is_not" {
		clause = "NOT (" + clause + ")"
	}
	return clause, []any{rule.Value}, argIdx + 1, true, nil
}

func buildEpisodeCatalogComparisonClause(column string, rule QueryRule, argIdx int, cast string) (string, []any, int, bool, error) {
	placeholder := func(idx int) string {
		if cast == "" {
			return "$" + strconv.Itoa(idx)
		}
		return "$" + strconv.Itoa(idx) + "::" + cast
	}

	switch rule.Op {
	case "gt", "gte", "lt", "lte":
		operator := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[rule.Op]
		return fmt.Sprintf("%s %s %s", column, operator, placeholder(argIdx)), []any{rule.Value}, argIdx + 1, true, nil
	case "between":
		values, err := toBetweenValues(rule.Value)
		if err != nil {
			return "", nil, argIdx, true, fmt.Errorf("between requires [min, max] array: %w", err)
		}
		return fmt.Sprintf("%s >= %s AND %s <= %s", column, placeholder(argIdx), column, placeholder(argIdx+1)),
			[]any{values[0], values[1]}, argIdx + 2, true, nil
	case "is":
		return fmt.Sprintf("%s = %s", column, placeholder(argIdx)), []any{rule.Value}, argIdx + 1, true, nil
	case "is_not":
		return fmt.Sprintf("NOT (%s = %s)", column, placeholder(argIdx)), []any{rule.Value}, argIdx + 1, true, nil
	case "in_last":
		duration, ok := rule.Value.(string)
		if !ok {
			return "", nil, argIdx, true, fmt.Errorf("in_last requires a duration string like '30d'")
		}
		interval, err := parseDuration(duration)
		if err != nil {
			return "", nil, argIdx, true, err
		}
		return fmt.Sprintf("%s >= NOW() - INTERVAL '%s'", column, interval), nil, argIdx, true, nil
	default:
		return "", nil, argIdx, true, fmt.Errorf("unsupported comparison operator %q", rule.Op)
	}
}

func buildEpisodeCatalogBoolPresenceClause(trueColumn, falseColumn string, rule QueryRule, argIdx int) (string, []any, int, bool, error) {
	if rule.Op != "is" {
		return "", nil, argIdx, true, fmt.Errorf("unsupported boolean operator %q", rule.Op)
	}
	value, ok := rule.Value.(bool)
	if !ok {
		return "", nil, argIdx, true, fmt.Errorf("%s requires a boolean value", rule.Field)
	}
	if value {
		return trueColumn, nil, argIdx, true, nil
	}
	return falseColumn, nil, argIdx, true, nil
}

func buildEpisodeCatalogBitrateClause(rule QueryRule, argIdx int) (string, []any, int, bool, error) {
	switch rule.Op {
	case "gt", "gte":
		operator := map[string]string{"gt": ">", "gte": ">="}[rule.Op]
		return fmt.Sprintf("ece.max_bitrate %s $%d", operator, argIdx), []any{rule.Value}, argIdx + 1, true, nil
	case "lt", "lte":
		operator := map[string]string{"lt": "<", "lte": "<="}[rule.Op]
		return fmt.Sprintf("ece.min_bitrate %s $%d", operator, argIdx), []any{rule.Value}, argIdx + 1, true, nil
	case "between":
		return "", nil, argIdx, false, nil
	default:
		return "", nil, argIdx, true, fmt.Errorf("unsupported bitrate operator %q", rule.Op)
	}
}

func episodeCatalogEntryOrderBy(sortConfig QuerySort) (string, bool) {
	sortConfig = NormalizeQuerySort(sortConfig)
	dir := "DESC"
	if sortConfig.Order == "asc" {
		dir = "ASC"
	}

	switch sortConfig.Field {
	case "title":
		return fmt.Sprintf("ORDER BY ece.sort_key %s, ece.episode_id ASC", dir), true
	case "added_at":
		return fmt.Sprintf("ORDER BY ece.added_at %s, ece.sort_key ASC, ece.episode_id ASC", dir), true
	case "release_date", "last_air_date":
		return fmt.Sprintf("ORDER BY ece.episode_air_date %s NULLS LAST, ece.sort_key ASC, ece.episode_id ASC", dir), true
	case "year":
		return fmt.Sprintf("ORDER BY ece.year %s, ece.sort_key ASC, ece.episode_id ASC", dir), true
	case "content_rating":
		return fmt.Sprintf("ORDER BY ece.content_rating_rank %s, ece.content_rating_label %s, ece.sort_key ASC, ece.episode_id ASC", dir, dir), true
	case "runtime":
		return fmt.Sprintf("ORDER BY ece.runtime %s NULLS LAST, ece.sort_key ASC, ece.episode_id ASC", dir), true
	case "rating_imdb":
		return fmt.Sprintf("ORDER BY ece.rating_imdb %s NULLS LAST, ece.sort_key ASC, ece.episode_id ASC", dir), true
	case "rating_tmdb":
		return fmt.Sprintf("ORDER BY ece.rating_tmdb %s NULLS LAST, ece.sort_key ASC, ece.episode_id ASC", dir), true
	case "rating_rt_critic", "rating_rt_audience":
		return "ORDER BY ece.sort_key ASC, ece.episode_id ASC", true
	case "resolution":
		return fmt.Sprintf("ORDER BY ece.max_resolution_rank %s NULLS LAST, ece.sort_key ASC, ece.episode_id ASC", dir), true
	case "bitrate":
		return fmt.Sprintf("ORDER BY ece.max_bitrate %s NULLS LAST, ece.sort_key ASC, ece.episode_id ASC", dir), true
	default:
		return "", false
	}
}

func countSameFileTechnicalRules(rules []QueryRule) int {
	count := 0
	for _, rule := range rules {
		if isSameFileTechnicalRule(rule) {
			count++
		}
	}
	return count
}

func episodeCatalogEntriesUnavailable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "42P01" || pgErr.Code == "42883"
}
