package catalog

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

type QueryBuilder struct {
	alias      string
	argIdx     int
	args       []any
	userID     int
	profileID  string
	libraryIDs []int
	mediaScope string
	// requireUserHistoryCTE is set when an emitted clause references the
	// per-user history aggregate (audit 2026-05-01 §3.1 Pattern B). The
	// executor reads this flag to inject the user_last_watched CTE and
	// the LEFT JOIN aliased as uhist before running the query.
	requireUserHistoryCTE bool
}

type QuerySortPlan struct {
	Joins   []string
	OrderBy string
	Args    []any
}

func NewQueryBuilder(alias string) *QueryBuilder {
	return &QueryBuilder{alias: alias, argIdx: 1}
}

func (qb *QueryBuilder) WithUserScope(userID int, profileID string) *QueryBuilder {
	qb.userID = userID
	qb.profileID = profileID
	return qb
}

func (qb *QueryBuilder) WithLibraryScope(libraryIDs []int) *QueryBuilder {
	qb.libraryIDs = append([]int(nil), libraryIDs...)
	return qb
}

func (qb *QueryBuilder) WithMediaScope(scope string) *QueryBuilder {
	qb.mediaScope = strings.TrimSpace(scope)
	return qb
}

func (qb *QueryBuilder) WithArgIdx(argIdx int) *QueryBuilder {
	if argIdx > 0 {
		qb.argIdx = argIdx
	}
	return qb
}

func (qb *QueryBuilder) Build(input any) (string, []any, error) {
	normalized, err := normalizeBuilderInput(input)
	if err != nil {
		return "", nil, err
	}
	if err := normalized.Validate(); err != nil {
		return "", nil, err
	}

	qb.argIdx = 1
	qb.args = nil

	if len(normalized.Groups) == 0 {
		return "", nil, nil
	}

	topJoiner := " AND "
	if normalized.Match == "any" {
		topJoiner = " OR "
	}

	var groupClauses []string
	for _, group := range normalized.Groups {
		clause, err := qb.buildGroup(group)
		if err != nil {
			return "", nil, err
		}
		if clause != "" {
			groupClauses = append(groupClauses, clause)
		}
	}

	if len(groupClauses) == 0 {
		return "", nil, nil
	}
	if len(groupClauses) == 1 {
		return groupClauses[0], qb.args, nil
	}

	wrapped := make([]string, len(groupClauses))
	for i, clause := range groupClauses {
		wrapped[i] = "(" + clause + ")"
	}
	return strings.Join(wrapped, topJoiner), qb.args, nil
}

func normalizeBuilderInput(input any) (QueryDefinition, error) {
	switch def := input.(type) {
	case QueryDefinition:
		return def.Normalize(), nil
	case *QueryDefinition:
		if def == nil {
			return QueryDefinition{}.Normalize(), nil
		}
		return def.Normalize(), nil
	}

	var legacy struct {
		Match  string       `json:"match"`
		Groups []QueryGroup `json:"groups"`
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return QueryDefinition{}, err
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return QueryDefinition{}, err
	}

	return QueryDefinition{
		Match:  legacy.Match,
		Groups: legacy.Groups,
	}.Normalize(), nil
}

func (qb *QueryBuilder) ArgIdx() int {
	return qb.argIdx
}

// RequiresUserHistoryCTE reports whether the most recently built clauses
// referenced the user_last_watched derived-table aggregate. The executor
// uses this signal to inject the CTE definition and the LEFT JOIN that
// surfaces uhist.last_watched (audit 2026-05-01 §3.1 Pattern B).
func (qb *QueryBuilder) RequiresUserHistoryCTE() bool {
	return qb.requireUserHistoryCTE
}

// UserHistoryCTEArgs returns the [userID, profileID] pair the executor must
// prepend to the bound argument list when injecting the user_last_watched
// CTE. Returns nil when the CTE is not required.
func (qb *QueryBuilder) UserHistoryCTEArgs() []any {
	if !qb.requireUserHistoryCTE {
		return nil
	}
	return []any{qb.userID, qb.profileID}
}

// UserHistoryCTESQL returns the user_last_watched CTE definition. The two
// placeholder positions (user_id, profile_id) are bound to the args returned
// by UserHistoryCTEArgs and should be the first two entries in the final
// statement's arg list. The CTE aggregates user_watch_history watch_at and
// user_watch_progress.completed=TRUE updated_at into one row per
// media_item_id, filtering out items the user has hidden via
// user_history_hidden_items. The single GROUP BY replaces the two
// correlated MAX subqueries the previous lastWatchedExpr emitted (audit
// 2026-05-01 §3.1 Pattern B).
//
// NULL handling: each UNION ALL branch sets exactly one of (uwh_at, uwp_at);
// the other column is NULL. Postgres GREATEST and LEAST IGNORE NULL inputs
// (unlike MySQL/Oracle, which propagate NULL) — "NULL values in the list
// are ignored. The result will be NULL only if all the expressions evaluate
// to NULL." So GREATEST(uwh_at, NULL) = uwh_at, and the per-row aggregate
// reduces correctly to the timestamp from whichever source produced the row.
// MAX over those values then yields the per-media_item maximum across both
// sources. Do NOT wrap in COALESCE — that would mask future schema changes
// that could legitimately produce two non-NULL values per row.
// See https://www.postgresql.org/docs/current/functions-conditional.html#FUNCTIONS-GREATEST-LEAST
func UserHistoryCTESQL(argIdx int) string {
	return fmt.Sprintf(`user_last_watched AS (
	SELECT src.media_item_id, MAX(GREATEST(src.uwh_at, src.uwp_at)) AS last_watched
	FROM (
		SELECT uwh.media_item_id, uwh.watched_at AS uwh_at, NULL::timestamptz AS uwp_at
		FROM user_watch_history uwh
		WHERE uwh.user_id = $%d AND uwh.profile_id = $%d
		UNION ALL
		SELECT uwp.media_item_id, NULL::timestamptz, uwp.updated_at
		FROM user_watch_progress uwp
		WHERE uwp.user_id = $%d AND uwp.profile_id = $%d AND uwp.completed = TRUE
	) src
	WHERE NOT EXISTS (
		SELECT 1 FROM user_history_hidden_items hhi
		WHERE hhi.user_id = $%d AND hhi.profile_id = $%d
		  AND hhi.media_item_id = src.media_item_id
		  AND COALESCE(src.uwh_at, src.uwp_at) <= hhi.hidden_before
	)
	GROUP BY src.media_item_id
)`, argIdx, argIdx+1, argIdx, argIdx+1, argIdx, argIdx+1)
}

func (qb *QueryBuilder) BuildSortClause(sortConfig QuerySort) (string, []any, error) {
	plan, err := qb.BuildSortPlan(sortConfig)
	if err != nil {
		return "", nil, err
	}
	return plan.OrderBy, plan.Args, nil
}

func (qb *QueryBuilder) BuildSortPlan(sortConfig QuerySort) (QuerySortPlan, error) {
	sortConfig = NormalizeQuerySort(sortConfig)
	sortDef, ok := querySortDefs[sortConfig.Field]
	if !ok {
		sortDef = querySortDefs[defaultSortField]
	}

	dir := "DESC"
	if sortConfig.Order == "asc" {
		dir = "ASC"
	}

	if sortDef.personalized {
		if err := qb.ensureUserScope(sortConfig.Field); err != nil {
			return QuerySortPlan{}, err
		}
	}

	titleExpr := qb.normalizedTitleExpr()
	plan := QuerySortPlan{}

	switch sortConfig.Field {
	case "title":
		plan.OrderBy = fmt.Sprintf("ORDER BY %s %s, %s.content_id ASC", titleExpr, dir, qb.alias)
		return plan, nil
	case "release_date":
		plan.OrderBy = qb.orderByExpr(qb.releaseDateSortExpr(), dir, true, titleExpr)
		return plan, nil
	case "added_at":
		expr, joins, args := qb.addedAtSortPlan()
		plan.Joins = joins
		plan.Args = args
		plan.OrderBy = qb.orderByExpr(expr, dir, len(joins) > 0, titleExpr)
		return plan, nil
	case "content_rating":
		rankExpr := qb.contentRatingRankExpr()
		labelExpr := qb.contentRatingLabelExpr()
		plan.OrderBy = fmt.Sprintf(
			"ORDER BY %s %s, %s %s, %s ASC, %s.content_id ASC",
			rankExpr,
			dir,
			labelExpr,
			dir,
			titleExpr,
			qb.alias,
		)
		return plan, nil
	case "resolution":
		joinSQL, args := qb.mediaFileSortJoin("max_resolution_rank", qb.resolutionRankExpr())
		plan.Joins = []string{joinSQL}
		plan.Args = args
		plan.OrderBy = qb.orderByExpr("sort_files.max_resolution_rank", dir, true, titleExpr)
		return plan, nil
	case "bitrate":
		joinSQL, args := qb.mediaFileSortJoin("max_bitrate", "MAX(mf.bitrate)")
		plan.Joins = []string{joinSQL}
		plan.Args = args
		plan.OrderBy = qb.orderByExpr("sort_files.max_bitrate", dir, true, titleExpr)
		return plan, nil
	case "progress":
		expr, joins, args := qb.progressSortPlan()
		plan.Joins = joins
		plan.Args = args
		plan.OrderBy = qb.orderByExpr(expr, dir, true, titleExpr)
		return plan, nil
	case "date_viewed":
		expr, joins, args := qb.dateViewedSortPlan()
		plan.Joins = joins
		plan.Args = args
		plan.OrderBy = qb.orderByExpr(expr, dir, true, titleExpr)
		return plan, nil
	case "plays":
		expr, joins, args := qb.playsSortPlan()
		plan.Joins = joins
		plan.Args = args
		plan.OrderBy = qb.orderByExpr(expr, dir, true, titleExpr)
		return plan, nil
	case "last_air_date":
		expr, joins := qb.lastAirDateSortPlan()
		plan.Joins = joins
		plan.OrderBy = qb.orderByExpr(expr, dir, true, titleExpr)
		return plan, nil
	case "author":
		plan.Joins = []string{qb.personKindSortJoin(models.PersonKindAuthor, "sort_author")}
		plan.OrderBy = qb.orderByExpr("sort_author.name", dir, true, titleExpr)
		return plan, nil
	case "narrator":
		plan.Joins = []string{qb.personKindSortJoin(models.PersonKindNarrator, "sort_narrator")}
		plan.OrderBy = qb.orderByExpr("sort_narrator.name", dir, true, titleExpr)
		return plan, nil
	case "series":
		plan.Joins = []string{fmt.Sprintf(
			"LEFT JOIN audiobook_series sort_series ON sort_series.content_id = %s.content_id",
			qb.alias,
		)}
		// Sort by series name primarily, then by series_index so books
		// within the same series come back in narrative order. Title
		// breaks ties for books that don't have a series_index.
		clause := fmt.Sprintf(
			"ORDER BY sort_series.series_name %s NULLS LAST, sort_series.series_index ASC NULLS LAST, %s ASC, %s.content_id ASC",
			dir,
			titleExpr,
			qb.alias,
		)
		plan.OrderBy = clause
		return plan, nil
	}

	plan.OrderBy = qb.orderByExpr(queryColumnSQL(qb.alias, sortDef.columnSQL), dir, sortDef.nullsLast, titleExpr)
	if sortDef.titleSortOnly {
		plan.OrderBy = fmt.Sprintf("ORDER BY %s %s, %s.content_id ASC", queryColumnSQL(qb.alias, sortDef.columnSQL), dir, qb.alias)
	}
	return plan, nil
}

func (qb *QueryBuilder) buildGroup(group QueryGroup) (string, error) {
	if len(group.Rules) == 0 {
		return "", nil
	}

	joiner := " AND "
	if group.Match == "any" {
		joiner = " OR "
	}

	combinedTechnicalClause, consumedRules, err := qb.buildSameFileTechnicalClause(group)
	if err != nil {
		return "", err
	}

	var ruleClauses []string
	if combinedTechnicalClause != "" {
		ruleClauses = append(ruleClauses, combinedTechnicalClause)
	}
	for index, rule := range group.Rules {
		if consumedRules[index] {
			continue
		}
		clause, err := qb.buildRule(rule)
		if err != nil {
			return "", err
		}
		if clause != "" {
			ruleClauses = append(ruleClauses, clause)
		}
	}

	if len(ruleClauses) == 0 {
		return "", nil
	}
	return strings.Join(ruleClauses, joiner), nil
}

func (qb *QueryBuilder) buildRule(rule QueryRule) (string, error) {
	def, ok := queryFieldDefs[rule.Field]
	if !ok {
		return "", fmt.Errorf("unknown filter field: %q", rule.Field)
	}
	if !def.validOps[rule.Op] {
		return "", fmt.Errorf("invalid operator %q for field %q", rule.Op, rule.Field)
	}
	if !def.executable {
		return "", fmt.Errorf("field %q is not supported for SQL execution yet", rule.Field)
	}

	switch rule.Field {
	case "actor":
		return qb.buildPersonClause(models.PersonKindActor, rule)
	case "director":
		return qb.buildPersonClause(models.PersonKindDirector, rule)
	case "writer":
		return qb.buildPersonClause(models.PersonKindWriter, rule)
	case "producer":
		return qb.buildPersonClause(models.PersonKindProducer, rule)
	case "author":
		return qb.buildPersonClause(models.PersonKindAuthor, rule)
	case "narrator":
		return qb.buildPersonClause(models.PersonKindNarrator, rule)
	case "series":
		return qb.buildAudiobookSeriesClause(rule)
	case "watched":
		return qb.buildWatchedClause(rule)
	case "favorited":
		return qb.buildFavoriteClause(rule)
	case "in_watchlist":
		return qb.buildWatchlistClause(rule)
	case "in_progress":
		return qb.buildInProgressClause(rule)
	case "last_watched":
		return qb.buildLastWatchedClause(rule)
	case "resolution":
		return qb.buildResolutionClause(rule)
	case "hdr":
		return qb.buildMediaFileEqualityClause("mf.hdr", rule)
	case "dolby_vision":
		return qb.buildDolbyVisionClause(rule)
	case "bitrate":
		return qb.buildMediaFileComparisonClause("mf.bitrate", rule)
	case "audio_language":
		return qb.buildAudioLanguageClause(rule)
	case "subtitle_language":
		return qb.buildSubtitleLanguageClause(rule)
	}

	column := queryColumnSQL(qb.alias, def.columnSQL)

	switch rule.Op {
	case "is":
		return qb.buildEqualityClause(column, def.isArray, rule.Value, false), nil
	case "is_not":
		return qb.buildEqualityClause(column, def.isArray, rule.Value, true), nil
	case "contains":
		if !def.isArray {
			return "", fmt.Errorf("contains operator only valid for array fields")
		}
		qb.args = append(qb.args, rule.Value)
		clause := fmt.Sprintf("%s @> ARRAY[$%d]::text[]", column, qb.argIdx)
		qb.argIdx++
		return clause, nil
	case "gt":
		return qb.buildComparisonClause(column, ">", rule.Value), nil
	case "gte":
		return qb.buildComparisonClause(column, ">=", rule.Value), nil
	case "lt":
		return qb.buildComparisonClause(column, "<", rule.Value), nil
	case "lte":
		return qb.buildComparisonClause(column, "<=", rule.Value), nil
	case "between":
		values, err := toBetweenValues(rule.Value)
		if err != nil {
			return "", fmt.Errorf("between requires [min, max] array: %w", err)
		}
		qb.args = append(qb.args, values[0], values[1])
		clause := fmt.Sprintf("%s >= $%d AND %s <= $%d", column, qb.argIdx, column, qb.argIdx+1)
		qb.argIdx += 2
		return clause, nil
	case "in_last":
		duration, ok := rule.Value.(string)
		if !ok {
			return "", fmt.Errorf("in_last requires a duration string like '30d'")
		}
		interval, err := parseDuration(duration)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s >= NOW() - INTERVAL '%s'", column, interval), nil
	default:
		return "", fmt.Errorf("unhandled operator: %q", rule.Op)
	}
}

func (qb *QueryBuilder) buildPersonClause(kind models.PersonKind, rule QueryRule) (string, error) {
	qb.args = append(qb.args, rule.Value)
	clause := fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM item_people ip
		WHERE ip.content_id = %s.content_id
		  AND ip.kind = %d
		  AND ip.person_id IN (
			SELECT p.id
			FROM people p
			WHERE LOWER(p.name) = LOWER($%d)
		  )
	)`, qb.alias, kind, qb.argIdx)
	qb.argIdx++
	if rule.Op == "is_not" {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

// personKindSortJoin emits a LEFT JOIN LATERAL that resolves the
// alphabetically-first person name of the given kind per item, exposed
// as <alias>.name. Used to power Author / Narrator sort plans without
// a heavyweight aggregate over the whole join.
func (qb *QueryBuilder) personKindSortJoin(kind models.PersonKind, alias string) string {
	return fmt.Sprintf(
		`LEFT JOIN LATERAL (
			SELECT p.name
			FROM item_people ip
			JOIN people p ON p.id = ip.person_id
			WHERE ip.content_id = %s.content_id
			  AND ip.kind = %d
			  AND p.name IS NOT NULL
			  AND BTRIM(p.name) <> ''
			ORDER BY p.name ASC
			LIMIT 1
		) %s ON TRUE`,
		qb.alias, kind, alias,
	)
}

func (qb *QueryBuilder) buildAudiobookSeriesClause(rule QueryRule) (string, error) {
	qb.args = append(qb.args, rule.Value)
	clause := fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM audiobook_series s
		WHERE s.content_id = %s.content_id
		  AND LOWER(BTRIM(s.series_name)) = LOWER(BTRIM($%d))
	)`, qb.alias, qb.argIdx)
	qb.argIdx++
	if rule.Op == "is_not" {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

func (qb *QueryBuilder) buildResolutionClause(rule QueryRule) (string, error) {
	value, ok := rule.Value.(string)
	if !ok {
		return "", fmt.Errorf("resolution requires a string value")
	}
	scopeClause := qb.mediaFileLibraryScopeClause("mf")
	qb.args = append(qb.args, normalizeResolutionValue(value))
	clause := fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM media_files mf
		WHERE %s
		  AND mf.missing_since IS NULL
		  AND %s
		  AND %s = $%d
	)`, qb.mediaFileJoinCondition("mf"), scopeClause, normalizedResolutionExpr("mf.resolution"), qb.argIdx)
	qb.argIdx++
	if rule.Op == "is_not" {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

func (qb *QueryBuilder) buildMediaFileEqualityClause(column string, rule QueryRule) (string, error) {
	scopeClause := qb.mediaFileLibraryScopeClause("mf")
	qb.args = append(qb.args, rule.Value)
	clause := fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM media_files mf
		WHERE %s
		  AND mf.missing_since IS NULL
		  AND %s
		  AND %s = $%d
	)`, qb.mediaFileJoinCondition("mf"), scopeClause, column, qb.argIdx)
	qb.argIdx++
	if rule.Op == "is_not" {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

func (qb *QueryBuilder) buildMediaFileComparisonClause(column string, rule QueryRule) (string, error) {
	scopeClause := qb.mediaFileLibraryScopeClause("mf")
	switch rule.Op {
	case "gt", "gte", "lt", "lte":
		operator := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[rule.Op]
		qb.args = append(qb.args, rule.Value)
		clause := fmt.Sprintf(`EXISTS (
			SELECT 1
			FROM media_files mf
			WHERE %s
			  AND mf.missing_since IS NULL
			  AND %s
			  AND %s %s $%d
		)`, qb.mediaFileJoinCondition("mf"), scopeClause, column, operator, qb.argIdx)
		qb.argIdx++
		return clause, nil
	case "between":
		values, err := toBetweenValues(rule.Value)
		if err != nil {
			return "", fmt.Errorf("between requires [min, max] array: %w", err)
		}
		qb.args = append(qb.args, values[0], values[1])
		clause := fmt.Sprintf(`EXISTS (
			SELECT 1
			FROM media_files mf
			WHERE %s
			  AND mf.missing_since IS NULL
			  AND %s
			  AND %s >= $%d
			  AND %s <= $%d
		)`, qb.mediaFileJoinCondition("mf"), scopeClause, column, qb.argIdx, column, qb.argIdx+1)
		qb.argIdx += 2
		return clause, nil
	default:
		return "", fmt.Errorf("unsupported operator %q for media file comparison", rule.Op)
	}
}

func (qb *QueryBuilder) buildDolbyVisionClause(rule QueryRule) (string, error) {
	value, ok := rule.Value.(bool)
	if !ok {
		return "", fmt.Errorf("dolby_vision requires a boolean value")
	}
	scopeClause := qb.mediaFileLibraryScopeClause("mf")

	inner := `EXISTS (
		SELECT 1
		FROM jsonb_array_elements(COALESCE(mf.video_tracks, '[]'::jsonb)) AS vt
		WHERE NULLIF(BTRIM(vt->>'dolby_vision'), '') IS NOT NULL
	)`
	if !value {
		inner = `NOT ` + inner
	}

	return fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM media_files mf
		WHERE %s
		  AND mf.missing_since IS NULL
		  AND %s
		  AND %s
	)`, qb.mediaFileJoinCondition("mf"), scopeClause, inner), nil
}

func (qb *QueryBuilder) buildAudioLanguageClause(rule QueryRule) (string, error) {
	value, ok := rule.Value.(string)
	if !ok {
		return "", fmt.Errorf("audio_language requires a string value")
	}
	scopeClause := qb.mediaFileLibraryScopeClause("mf")
	// audio_language_codes is the STORED generated text[] of LOWER(language)
	// codes from audio_tracks JSONB (migration 104). Filtering via @>
	// ARRAY[...] uses the GIN index and avoids per-row JSONB unnest
	// (audit 2026-05-01 §2.5b).
	qb.args = append(qb.args, strings.ToLower(value))
	clause := fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM media_files mf
		WHERE %s
		  AND mf.missing_since IS NULL
		  AND %s
		  AND mf.audio_language_codes @> ARRAY[$%d]::text[]
	)`, qb.mediaFileJoinCondition("mf"), scopeClause, qb.argIdx)
	qb.argIdx++
	if rule.Op == "is_not" {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

func (qb *QueryBuilder) buildSubtitleLanguageClause(rule QueryRule) (string, error) {
	value, ok := rule.Value.(string)
	if !ok {
		return "", fmt.Errorf("subtitle_language requires a string value")
	}
	scopeClause := qb.mediaFileLibraryScopeClause("mf")
	// subtitle_language_codes (migration 104) covers embedded subtitle_tracks
	// only. external_subtitles is still JSONB unnest as a fallback so the
	// behavior of the OR-check is preserved (audit 2026-05-01 §2.5b).
	qb.args = append(qb.args, strings.ToLower(value))
	clause := fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM media_files mf
		WHERE %s
		  AND mf.missing_since IS NULL
		  AND %s
		  AND (
			mf.subtitle_language_codes @> ARRAY[$%d]::text[]
			OR EXISTS (
				SELECT 1
				FROM jsonb_array_elements(COALESCE(mf.external_subtitles, '[]'::jsonb)) AS track
				WHERE LOWER(COALESCE(track->>'language', '')) = $%d
			)
		  )
	)`, qb.mediaFileJoinCondition("mf"), scopeClause, qb.argIdx, qb.argIdx)
	qb.argIdx++
	if rule.Op == "is_not" {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

func (qb *QueryBuilder) buildSameFileTechnicalClause(group QueryGroup) (string, map[int]bool, error) {
	if group.Match == "any" {
		return "", nil, nil
	}

	startArgIdx := qb.argIdx
	startArgsLen := len(qb.args)
	consumed := make(map[int]bool)
	predicates := make([]string, 0, len(group.Rules))
	for index, rule := range group.Rules {
		if !isSameFileTechnicalRule(rule) {
			continue
		}
		predicate, err := qb.buildTechnicalMediaFilePredicate(rule, "mf")
		if err != nil {
			return "", nil, err
		}
		if predicate == "" {
			continue
		}
		consumed[index] = true
		predicates = append(predicates, predicate)
	}

	if len(predicates) < 2 {
		qb.argIdx = startArgIdx
		qb.args = qb.args[:startArgsLen]
		return "", nil, nil
	}

	whereParts := []string{
		qb.mediaFileJoinCondition("mf"),
		"mf.missing_since IS NULL",
	}
	if len(qb.libraryIDs) > 0 {
		placeholders, args := qb.consumeIntArgs(qb.libraryIDs)
		qb.args = append(qb.args, args...)
		whereParts = append(whereParts, fmt.Sprintf("mf.media_folder_id IN (%s)", strings.Join(placeholders, ", ")))
	}
	whereParts = append(whereParts, predicates...)

	return fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM media_files mf
		WHERE %s
	)`, strings.Join(whereParts, "\n\t\t  AND ")), consumed, nil
}

func (qb *QueryBuilder) buildTechnicalMediaFilePredicate(rule QueryRule, alias string) (string, error) {
	switch rule.Field {
	case "resolution":
		value, ok := rule.Value.(string)
		if !ok {
			return "", fmt.Errorf("resolution requires a string value")
		}
		qb.args = append(qb.args, normalizeResolutionValue(value))
		predicate := fmt.Sprintf("%s = $%d", normalizedResolutionExpr(alias+".resolution"), qb.argIdx)
		qb.argIdx++
		return predicate, nil
	case "hdr":
		value, ok := rule.Value.(bool)
		if !ok {
			return "", fmt.Errorf("hdr requires a boolean value")
		}
		qb.args = append(qb.args, value)
		predicate := fmt.Sprintf("%s.hdr = $%d", alias, qb.argIdx)
		qb.argIdx++
		return predicate, nil
	case "dolby_vision":
		value, ok := rule.Value.(bool)
		if !ok {
			return "", fmt.Errorf("dolby_vision requires a boolean value")
		}
		predicate := fmt.Sprintf(`EXISTS (
			SELECT 1
			FROM jsonb_array_elements(COALESCE(%s.video_tracks, '[]'::jsonb)) AS vt
			WHERE NULLIF(BTRIM(vt->>'dolby_vision'), '') IS NOT NULL
		)`, alias)
		if !value {
			return "NOT (" + predicate + ")", nil
		}
		return predicate, nil
	case "bitrate":
		return qb.buildMediaFileInlineComparisonPredicate(alias+".bitrate", rule)
	case "audio_language":
		value, ok := rule.Value.(string)
		if !ok {
			return "", fmt.Errorf("audio_language requires a string value")
		}
		// audio_language_codes is the STORED generated text[] derived from
		// audio_tracks JSONB (migration 104). GIN containment beats per-row
		// jsonb_array_elements unnest (audit 2026-05-01 §2.5b).
		qb.args = append(qb.args, strings.ToLower(value))
		predicate := fmt.Sprintf("%s.audio_language_codes @> ARRAY[$%d]::text[]", alias, qb.argIdx)
		qb.argIdx++
		return predicate, nil
	case "subtitle_language":
		value, ok := rule.Value.(string)
		if !ok {
			return "", fmt.Errorf("subtitle_language requires a string value")
		}
		// subtitle_language_codes (migration 104) covers embedded
		// subtitle_tracks only. external_subtitles remains a JSONB unnest
		// fallback to preserve OR-check behavior (audit 2026-05-01 §2.5b).
		qb.args = append(qb.args, strings.ToLower(value))
		predicate := fmt.Sprintf(`(
			%s.subtitle_language_codes @> ARRAY[$%d]::text[]
			OR EXISTS (
				SELECT 1
				FROM jsonb_array_elements(COALESCE(%s.external_subtitles, '[]'::jsonb)) AS track
				WHERE LOWER(COALESCE(track->>'language', '')) = $%d
			)
		)`, alias, qb.argIdx, alias, qb.argIdx)
		qb.argIdx++
		return predicate, nil
	default:
		return "", fmt.Errorf("field %q does not support same-file filtering", rule.Field)
	}
}

func (qb *QueryBuilder) buildMediaFileInlineComparisonPredicate(column string, rule QueryRule) (string, error) {
	switch rule.Op {
	case "gt", "gte", "lt", "lte":
		operator := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[rule.Op]
		qb.args = append(qb.args, rule.Value)
		predicate := fmt.Sprintf("%s %s $%d", column, operator, qb.argIdx)
		qb.argIdx++
		return predicate, nil
	case "between":
		values, err := toBetweenValues(rule.Value)
		if err != nil {
			return "", fmt.Errorf("between requires [min, max] array: %w", err)
		}
		qb.args = append(qb.args, values[0], values[1])
		predicate := fmt.Sprintf("%s >= $%d AND %s <= $%d", column, qb.argIdx, column, qb.argIdx+1)
		qb.argIdx += 2
		return predicate, nil
	default:
		return "", fmt.Errorf("unsupported operator %q for media file comparison", rule.Op)
	}
}

func isSameFileTechnicalRule(rule QueryRule) bool {
	if rule.Op == "is_not" {
		return false
	}
	switch rule.Field {
	case "resolution", "hdr", "dolby_vision", "bitrate", "audio_language", "subtitle_language":
		return true
	default:
		return false
	}
}

func (qb *QueryBuilder) mediaFileLibraryScopeClause(alias string) string {
	if len(qb.libraryIDs) == 0 {
		return "TRUE"
	}
	placeholders, args := qb.consumeIntArgs(qb.libraryIDs)
	qb.args = append(qb.args, args...)
	return fmt.Sprintf("%s.media_folder_id IN (%s)", alias, strings.Join(placeholders, ", "))
}

func (qb *QueryBuilder) mediaFileJoinCondition(mediaFileAlias string) string {
	return catalogMediaFileJoinConditionForScope(qb.mediaScope, mediaFileAlias, qb.alias)
}

func (qb *QueryBuilder) mediaFileGroupExpr(mediaFileAlias string) string {
	return catalogMediaFileGroupExprForScope(qb.mediaScope, mediaFileAlias)
}

func (qb *QueryBuilder) libraryContentExpr() string {
	return catalogLibraryContentExprForScope(qb.mediaScope, qb.alias)
}

func normalizedResolutionExpr(column string) string {
	return fmt.Sprintf(`CASE LOWER(NULLIF(BTRIM(%s), ''))
		WHEN '4k' THEN '2160p'
		WHEN 'uhd' THEN '2160p'
		ELSE LOWER(NULLIF(BTRIM(%s), ''))
	END`, column, column)
}

func normalizeResolutionValue(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "4k", "uhd":
		return "2160p"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func (qb *QueryBuilder) ensureUserScope(field string) error {
	if qb.userID == 0 || strings.TrimSpace(qb.profileID) == "" {
		return fmt.Errorf("field %q requires user scope", field)
	}
	return nil
}

func (qb *QueryBuilder) buildWatchedClause(rule QueryRule) (string, error) {
	if err := qb.ensureUserScope(rule.Field); err != nil {
		return "", err
	}
	value, ok := rule.Value.(bool)
	if !ok {
		return "", fmt.Errorf("watched requires a boolean value")
	}
	clause := qb.userStateCompletionClause()
	if !value {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

func (qb *QueryBuilder) buildFavoriteClause(rule QueryRule) (string, error) {
	if err := qb.ensureUserScope(rule.Field); err != nil {
		return "", err
	}
	value, ok := rule.Value.(bool)
	if !ok {
		return "", fmt.Errorf("favorited requires a boolean value")
	}
	qb.args = append(qb.args, qb.userID, qb.profileID)
	clause := fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM user_favorites uf
		WHERE uf.user_id = $%d
		  AND uf.profile_id = $%d
		  AND uf.media_item_id = %s.content_id
	)`, qb.argIdx, qb.argIdx+1, qb.alias)
	qb.argIdx += 2
	if !value {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

func (qb *QueryBuilder) buildWatchlistClause(rule QueryRule) (string, error) {
	if err := qb.ensureUserScope(rule.Field); err != nil {
		return "", err
	}
	value, ok := rule.Value.(bool)
	if !ok {
		return "", fmt.Errorf("in_watchlist requires a boolean value")
	}
	qb.args = append(qb.args, qb.userID, qb.profileID)
	clause := fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM user_watchlist uw
		WHERE uw.user_id = $%d
		  AND uw.profile_id = $%d
		  AND uw.media_item_id = %s.content_id
	)`, qb.argIdx, qb.argIdx+1, qb.alias)
	qb.argIdx += 2
	if !value {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

func (qb *QueryBuilder) buildInProgressClause(rule QueryRule) (string, error) {
	if err := qb.ensureUserScope(rule.Field); err != nil {
		return "", err
	}
	value, ok := rule.Value.(bool)
	if !ok {
		return "", fmt.Errorf("in_progress requires a boolean value")
	}
	qb.args = append(qb.args, qb.userID, qb.profileID)
	clause := fmt.Sprintf(`EXISTS (
		SELECT 1
		FROM user_watch_progress uwp
		WHERE uwp.user_id = $%d
		  AND uwp.profile_id = $%d
		  AND uwp.media_item_id = %s.content_id
		  AND uwp.completed = FALSE
		  AND uwp.position_seconds > 0
		  AND NOT EXISTS (
			SELECT 1
			FROM user_history_hidden_items hhi
			WHERE hhi.user_id = $%d
			  AND hhi.profile_id = $%d
			  AND hhi.media_item_id = uwp.media_item_id
			  AND uwp.updated_at <= hhi.hidden_before
		  )
	)`, qb.argIdx, qb.argIdx+1, qb.alias, qb.argIdx, qb.argIdx+1)
	qb.argIdx += 2
	if !value {
		return "NOT (" + clause + ")", nil
	}
	return clause, nil
}

func (qb *QueryBuilder) buildLastWatchedClause(rule QueryRule) (string, error) {
	if err := qb.ensureUserScope(rule.Field); err != nil {
		return "", err
	}
	expr := qb.lastWatchedExpr(rule.Field)

	switch rule.Op {
	case "gt":
		return qb.buildTimestampComparisonClause(expr, ">", rule.Value), nil
	case "gte":
		return qb.buildTimestampComparisonClause(expr, ">=", rule.Value), nil
	case "lt":
		return qb.buildTimestampComparisonClause(expr, "<", rule.Value), nil
	case "lte":
		return qb.buildTimestampComparisonClause(expr, "<=", rule.Value), nil
	case "between":
		values, err := toBetweenValues(rule.Value)
		if err != nil {
			return "", fmt.Errorf("between requires [min, max] array: %w", err)
		}
		qb.args = append(qb.args, values[0], values[1])
		clause := fmt.Sprintf("%s >= $%d::timestamptz AND %s <= $%d::timestamptz", expr, qb.argIdx, expr, qb.argIdx+1)
		qb.argIdx += 2
		return clause, nil
	case "in_last":
		duration, ok := rule.Value.(string)
		if !ok {
			return "", fmt.Errorf("in_last requires a duration string like '30d'")
		}
		interval, err := parseDuration(duration)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s >= NOW() - INTERVAL '%s'", expr, interval), nil
	default:
		return "", fmt.Errorf("unsupported operator %q for last_watched", rule.Op)
	}
}

func (qb *QueryBuilder) userStateCompletionClause() string {
	qb.args = append(qb.args, qb.userID, qb.profileID)
	clause := fmt.Sprintf(`(
		EXISTS (
			SELECT 1
			FROM user_watch_progress uwp
			WHERE uwp.user_id = $%d
			  AND uwp.profile_id = $%d
			  AND uwp.media_item_id = %s.content_id
			  AND uwp.completed = TRUE
			  AND NOT EXISTS (
				SELECT 1
				FROM user_history_hidden_items hhi
				WHERE hhi.user_id = $%d
				  AND hhi.profile_id = $%d
				  AND hhi.media_item_id = uwp.media_item_id
				  AND uwp.updated_at <= hhi.hidden_before
			  )
		)
		OR EXISTS (
			SELECT 1
			FROM user_watch_history uwh
			WHERE uwh.user_id = $%d
			  AND uwh.profile_id = $%d
			  AND uwh.media_item_id = %s.content_id
			  AND uwh.completed = TRUE
			  AND NOT EXISTS (
				SELECT 1
				FROM user_history_hidden_items hhi
				WHERE hhi.user_id = $%d
				  AND hhi.profile_id = $%d
				  AND hhi.media_item_id = uwh.media_item_id
				  AND uwh.watched_at <= hhi.hidden_before
			  )
		)
	)`,
		qb.argIdx,
		qb.argIdx+1,
		qb.alias,
		qb.argIdx,
		qb.argIdx+1,
		qb.argIdx,
		qb.argIdx+1,
		qb.alias,
		qb.argIdx,
		qb.argIdx+1,
	)
	qb.argIdx += 2
	return clause
}

// lastWatchedExpr returns the SQL expression the executor binds to the
// per-user history aggregate. It signals the executor (via
// requireUserHistoryCTE) that the user_last_watched CTE and a LEFT JOIN
// aliased uhist on uhist.media_item_id = mi.content_id must be injected.
// The previous implementation emitted two correlated MAX subqueries with
// nested NOT EXISTS guards which fired ~2N times per filter row (audit
// 2026-05-01 §3.1 Pattern B). The CTE aggregates user_watch_history and
// user_watch_progress (completed) once per request and the JOIN reuses the
// result for every candidate media item. We wrap the join column in
// COALESCE(..., '-infinity'::timestamptz) so never-watched items behave the
// same way under the comparison operators (lt/lte/between/in_last) as the
// previous GREATEST(COALESCE(...), COALESCE(...)) form.
func (qb *QueryBuilder) lastWatchedExpr(_ string) string {
	qb.requireUserHistoryCTE = true
	return "COALESCE(uhist.last_watched, '-infinity'::timestamptz)"
}

func (qb *QueryBuilder) buildTimestampComparisonClause(column, op string, value any) string {
	qb.args = append(qb.args, value)
	clause := fmt.Sprintf("%s %s $%d::timestamptz", column, op, qb.argIdx)
	qb.argIdx++
	return clause
}

func (qb *QueryBuilder) normalizedTitleExpr() string {
	return fmt.Sprintf(
		"LOWER(COALESCE(NULLIF(BTRIM(%s.sort_title), ''), %s.title))",
		qb.alias,
		qb.alias,
	)
}

func (qb *QueryBuilder) releaseDateSortExpr() string {
	if isEpisodeCatalogScope(qb.mediaScope) {
		return fmt.Sprintf("%s.episode_air_date", qb.alias)
	}
	return queryColumnSQL(qb.alias, querySortDefs["release_date"].columnSQL)
}

func (qb *QueryBuilder) orderByExpr(expr, dir string, nullsLast bool, titleExpr string) string {
	clause := fmt.Sprintf("ORDER BY %s %s", expr, dir)
	if nullsLast {
		clause += " NULLS LAST"
	}
	clause += fmt.Sprintf(", %s ASC, %s.content_id ASC", titleExpr, qb.alias)
	return clause
}

func (qb *QueryBuilder) addedAtSortPlan() (string, []string, []any) {
	if len(qb.libraryIDs) == 0 {
		return fmt.Sprintf("%s.created_at", qb.alias), nil, nil
	}

	placeholders, args := qb.consumeIntArgs(qb.libraryIDs)
	if isEpisodeCatalogScope(qb.mediaScope) {
		joinSQL := fmt.Sprintf(
			`LEFT JOIN (
				SELECT el.episode_id AS content_id, MIN(el.first_seen_at) AS added_at
				FROM episode_libraries el
				WHERE el.media_folder_id IN (%s)
				GROUP BY el.episode_id
			) sort_added ON sort_added.content_id = %s.content_id`,
			strings.Join(placeholders, ", "),
			qb.alias,
		)
		return "sort_added.added_at", []string{joinSQL}, args
	}

	joinSQL := fmt.Sprintf(
		`LEFT JOIN (
			SELECT mil.content_id, MIN(mil.first_seen_at) AS added_at
			FROM media_item_libraries mil
			WHERE mil.media_folder_id IN (%s)
			GROUP BY mil.content_id
		) sort_added ON sort_added.content_id = %s`,
		strings.Join(placeholders, ", "),
		qb.libraryContentExpr(),
	)
	return "sort_added.added_at", []string{joinSQL}, args
}

func (qb *QueryBuilder) contentRatingRankExpr() string {
	cases := make([]string, 0, len(access.RatingRankEntries()))
	for _, entry := range access.RatingRankEntries() {
		cases = append(
			cases,
			fmt.Sprintf(
				"WHEN UPPER(NULLIF(BTRIM(%s.content_rating), '')) = '%s' THEN %d",
				qb.alias,
				entry.Rating,
				entry.Rank,
			),
		)
	}
	return fmt.Sprintf(
		"CASE %s ELSE 2147483647 END",
		strings.Join(cases, " "),
	)
}

func (qb *QueryBuilder) contentRatingLabelExpr() string {
	return fmt.Sprintf(
		"LOWER(COALESCE(NULLIF(BTRIM(%s.content_rating), ''), '~~~~'))",
		qb.alias,
	)
}

func (qb *QueryBuilder) resolutionRankExpr() string {
	return `MAX(
		CASE UPPER(CASE LOWER(NULLIF(BTRIM(mf.resolution), ''))
			WHEN '4k' THEN '2160p'
			WHEN 'uhd' THEN '2160p'
			ELSE LOWER(NULLIF(BTRIM(mf.resolution), ''))
		END)
			WHEN '480P' THEN 1
			WHEN '720P' THEN 2
			WHEN '1080P' THEN 3
			WHEN '2160P' THEN 4
			WHEN '4320P' THEN 5
			ELSE NULL
		END
	)`
}

func (qb *QueryBuilder) mediaFileSortJoin(columnAlias, aggregateExpr string) (string, []any) {
	whereParts := []string{"mf.missing_since IS NULL"}
	var args []any
	if len(qb.libraryIDs) > 0 {
		placeholders, scopeArgs := qb.consumeIntArgs(qb.libraryIDs)
		whereParts = append(whereParts, fmt.Sprintf("mf.media_folder_id IN (%s)", strings.Join(placeholders, ", ")))
		args = append(args, scopeArgs...)
	}
	groupExpr := qb.mediaFileGroupExpr("mf")

	joinSQL := fmt.Sprintf(
		`LEFT JOIN (
			SELECT %s AS content_id, %s AS %s
			FROM media_files mf
			WHERE %s
			GROUP BY %s
		) sort_files ON sort_files.content_id = %s.content_id`,
		groupExpr,
		aggregateExpr,
		columnAlias,
		strings.Join(whereParts, " AND "),
		groupExpr,
		qb.alias,
	)
	return joinSQL, args
}

func (qb *QueryBuilder) consumeIntArgs(values []int) ([]string, []any) {
	placeholders := make([]string, len(values))
	args := make([]any, 0, len(values))
	for i, value := range values {
		placeholders[i] = "$" + strconv.Itoa(qb.argIdx+i)
		args = append(args, value)
	}
	qb.argIdx += len(values)
	return placeholders, args
}

func (qb *QueryBuilder) progressSortPlan() (string, []string, []any) {
	joinSQL := fmt.Sprintf(
		`LEFT JOIN (
			SELECT uwp.media_item_id AS content_id,
				CASE
					WHEN uwp.completed = FALSE
					  AND uwp.position_seconds > 0
					  AND COALESCE(uwp.duration_seconds, 0) > 0
					  AND (hhi.media_item_id IS NULL OR uwp.updated_at > hhi.hidden_before)
					THEN uwp.position_seconds::double precision / NULLIF(uwp.duration_seconds, 0)
					ELSE NULL
				END AS progress_ratio
			FROM user_watch_progress uwp
			LEFT JOIN user_history_hidden_items hhi
				ON hhi.user_id = $%d
			   AND hhi.profile_id = $%d
			   AND hhi.media_item_id = uwp.media_item_id
			WHERE uwp.user_id = $%d
			  AND uwp.profile_id = $%d
		) sort_progress ON sort_progress.content_id = %s.content_id`,
		qb.argIdx,
		qb.argIdx+1,
		qb.argIdx,
		qb.argIdx+1,
		qb.alias,
	)
	return "sort_progress.progress_ratio", []string{joinSQL}, []any{qb.userID, qb.profileID}
}

func (qb *QueryBuilder) dateViewedSortPlan() (string, []string, []any) {
	historyJoin := fmt.Sprintf(
		`LEFT JOIN (
			SELECT uwh.media_item_id AS content_id, MAX(uwh.watched_at) AS viewed_at
			FROM user_watch_history uwh
			LEFT JOIN user_history_hidden_items hhi
				ON hhi.user_id = $%d
			   AND hhi.profile_id = $%d
			   AND hhi.media_item_id = uwh.media_item_id
			WHERE uwh.user_id = $%d
			  AND uwh.profile_id = $%d
			  AND uwh.completed = TRUE
			  AND (hhi.media_item_id IS NULL OR uwh.watched_at > hhi.hidden_before)
			GROUP BY uwh.media_item_id
		) sort_history_viewed ON sort_history_viewed.content_id = %s.content_id`,
		qb.argIdx,
		qb.argIdx+1,
		qb.argIdx,
		qb.argIdx+1,
		qb.alias,
	)
	progressJoin := fmt.Sprintf(
		`LEFT JOIN (
			SELECT uwp.media_item_id AS content_id,
				CASE
					WHEN uwp.completed = TRUE
					  AND (hhi.media_item_id IS NULL OR uwp.updated_at > hhi.hidden_before)
					THEN uwp.updated_at
					ELSE NULL
				END AS viewed_at
			FROM user_watch_progress uwp
			LEFT JOIN user_history_hidden_items hhi
				ON hhi.user_id = $%d
			   AND hhi.profile_id = $%d
			   AND hhi.media_item_id = uwp.media_item_id
			WHERE uwp.user_id = $%d
			  AND uwp.profile_id = $%d
		) sort_progress_viewed ON sort_progress_viewed.content_id = %s.content_id`,
		qb.argIdx,
		qb.argIdx+1,
		qb.argIdx,
		qb.argIdx+1,
		qb.alias,
	)
	expr := `GREATEST(
		COALESCE(sort_history_viewed.viewed_at, '-infinity'::timestamptz),
		COALESCE(sort_progress_viewed.viewed_at, '-infinity'::timestamptz)
	)`
	return expr, []string{historyJoin, progressJoin}, []any{qb.userID, qb.profileID}
}

func (qb *QueryBuilder) playsSortPlan() (string, []string, []any) {
	historyJoin := fmt.Sprintf(
		`LEFT JOIN (
			SELECT uwh.media_item_id AS content_id, COUNT(*) AS play_count
			FROM user_watch_history uwh
			LEFT JOIN user_history_hidden_items hhi
				ON hhi.user_id = $%d
			   AND hhi.profile_id = $%d
			   AND hhi.media_item_id = uwh.media_item_id
			WHERE uwh.user_id = $%d
			  AND uwh.profile_id = $%d
			  AND uwh.completed = TRUE
			  AND (hhi.media_item_id IS NULL OR uwh.watched_at > hhi.hidden_before)
			GROUP BY uwh.media_item_id
		) sort_history_plays ON sort_history_plays.content_id = %s.content_id`,
		qb.argIdx,
		qb.argIdx+1,
		qb.argIdx,
		qb.argIdx+1,
		qb.alias,
	)
	progressJoin := fmt.Sprintf(
		`LEFT JOIN (
			SELECT uwp.media_item_id AS content_id,
				CASE
					WHEN uwp.completed = TRUE
					  AND (hhi.media_item_id IS NULL OR uwp.updated_at > hhi.hidden_before)
					THEN 1
					ELSE 0
				END AS completed_play_count
			FROM user_watch_progress uwp
			LEFT JOIN user_history_hidden_items hhi
				ON hhi.user_id = $%d
			   AND hhi.profile_id = $%d
			   AND hhi.media_item_id = uwp.media_item_id
			WHERE uwp.user_id = $%d
			  AND uwp.profile_id = $%d
		) sort_progress_plays ON sort_progress_plays.content_id = %s.content_id`,
		qb.argIdx,
		qb.argIdx+1,
		qb.argIdx,
		qb.argIdx+1,
		qb.alias,
	)
	expr := `NULLIF(GREATEST(
		COALESCE(sort_history_plays.play_count, 0),
		COALESCE(sort_progress_plays.completed_play_count, 0)
	), 0)`
	return expr, []string{historyJoin, progressJoin}, []any{qb.userID, qb.profileID}
}

func (qb *QueryBuilder) lastAirDateSortPlan() (string, []string) {
	expr := fmt.Sprintf("%s.last_air_date_at", qb.alias)
	return expr, nil // denormalized column — no JOIN required
}

func (qb *QueryBuilder) buildEqualityClause(column string, isArray bool, value any, negate bool) string {
	qb.args = append(qb.args, value)
	if isArray {
		clause := fmt.Sprintf("%s @> ARRAY[$%d]::text[]", column, qb.argIdx)
		qb.argIdx++
		if negate {
			return "NOT (" + clause + ")"
		}
		return clause
	}

	op := "="
	if negate {
		op = "!="
	}
	clause := fmt.Sprintf("%s %s $%d", column, op, qb.argIdx)
	qb.argIdx++
	return clause
}

func (qb *QueryBuilder) buildComparisonClause(column, op string, value any) string {
	qb.args = append(qb.args, value)
	clause := fmt.Sprintf("%s %s $%d", column, op, qb.argIdx)
	qb.argIdx++
	return clause
}

func toBetweenValues(v any) ([2]any, error) {
	switch val := v.(type) {
	case []any:
		if len(val) != 2 {
			return [2]any{}, fmt.Errorf("expected 2 elements, got %d", len(val))
		}
		return [2]any{val[0], val[1]}, nil
	case json.RawMessage:
		var arr []any
		if err := json.Unmarshal(val, &arr); err != nil {
			return [2]any{}, err
		}
		if len(arr) != 2 {
			return [2]any{}, fmt.Errorf("expected 2 elements, got %d", len(arr))
		}
		return [2]any{arr[0], arr[1]}, nil
	default:
		return [2]any{}, fmt.Errorf("unsupported between value type: %T", v)
	}
}

func parseDuration(s string) (string, error) {
	if len(s) < 2 {
		return "", fmt.Errorf("invalid duration: %q", s)
	}

	value := s[:len(s)-1]
	unit := s[len(s)-1]

	switch unit {
	case 'h':
		return value + " hours", nil
	case 'd':
		return value + " days", nil
	case 'w':
		return value + " weeks", nil
	case 'm':
		return value + " months", nil
	default:
		return "", fmt.Errorf("unsupported duration unit %q", unit)
	}
}
