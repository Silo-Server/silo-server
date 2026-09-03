package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	recentTVTypeEpisode = "episode"
	recentTVTypeSeries  = "series"
)

// RecentTVTarget is one card-producing TV availability event. By default,
// separate scan runs remain separate even when two multi-episode runs target
// the same show; RecentTVQuery.UniqueTargets can collapse them for keyed rows.
type RecentTVTarget struct {
	ContentID string
	Type      string
	AddedAt   time.Time
	// PlayContentID is a profile-INDEPENDENT anchor hint: the episode this
	// availability event is about. It is deliberately not filtered by the
	// viewing profile's playback-quality ceiling, because List results are
	// stored in the process-global resolved-list cache whose key excludes
	// MaxPlaybackQuality (see AccessFilter.WriteAccessScopeCacheKey) — a
	// quality-dependent value here would leak one profile's ceiling to
	// another. Feed it to PlayableTargetResolver as
	// PlayableTargetInput.PreferredContentID; that post-cache, profile-aware
	// pass is the only authority on the final play target.
	PlayContentID string
}

// RecentTVQuery describes one page of Plex-style recently-added TV events.
type RecentTVQuery struct {
	LibraryIDs    []int
	Access        AccessFilter
	NamePrefix    string
	SnapshotAt    *time.Time
	Limit         int
	Offset        int
	SkipTotal     bool
	UniqueTargets bool // collapse repeated target content IDs, keeping the newest event
}

// RecentTVRepository resolves scan-batched episode availability into episode
// or series card targets.
type RecentTVRepository struct {
	pool *pgxpool.Pool
}

func NewRecentTVRepository(pool *pgxpool.Pool) *RecentTVRepository {
	return &RecentTVRepository{pool: pool}
}

// recentTVFilterTypeScope maps a section's configured filter_type onto TV event
// grouping. An empty filter type leaves the decision to the library types;
// "series" opts in explicitly; every other configured type ("movie",
// "episode", "season", …) must keep the plain recently-added query, which is
// the only path that applies the type filter itself.
func recentTVFilterTypeScope(filterType string) (explicitSeries bool, tvEligible bool) {
	switch strings.ToLower(strings.TrimSpace(filterType)) {
	case "":
		return false, true
	case recentTVTypeSeries:
		return true, true
	default:
		return false, false
	}
}

// ResolveRecentTVLibraryIDs decides whether a recently-added section is
// exclusively TV-targeted and returns the effective visible TV libraries.
// filterType is the section's configured filter_type: an explicit "series"
// scope may include mixed libraries, an empty scope requires every requested
// library to be a dedicated series library, and any other type is not TV
// scoped at all because event grouping cannot honor it.
func ResolveRecentTVLibraryIDs(
	ctx context.Context,
	pool *pgxpool.Pool,
	requested []int,
	filterType string,
	access AccessFilter,
) ([]int, bool, error) {
	explicitSeries, tvEligible := recentTVFilterTypeScope(filterType)
	if !tvEligible {
		return nil, false, nil
	}
	if pool == nil {
		return nil, false, nil
	}
	requested = uniquePositiveInts(requested)
	if len(requested) == 0 && !explicitSeries {
		return nil, false, nil
	}

	conditions := []string{"enabled = true"}
	args := []any{}
	if len(requested) > 0 {
		conditions = append(conditions, "id = ANY($1)")
		args = append(args, requested)
	}
	rows, err := pool.Query(ctx, `
		SELECT id, type
		FROM media_folders
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY id ASC
	`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("listing TV recent libraries: %w", err)
	}
	defer rows.Close()

	byID := make(map[int]string)
	for rows.Next() {
		var id int
		var libraryType string
		if err := rows.Scan(&id, &libraryType); err != nil {
			return nil, false, fmt.Errorf("scanning TV recent library: %w", err)
		}
		byID[id] = strings.ToLower(strings.TrimSpace(libraryType))
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterating TV recent libraries: %w", err)
	}

	if !explicitSeries {
		if len(byID) != len(requested) {
			return nil, false, nil
		}
		for _, id := range requested {
			if byID[id] != recentTVTypeSeries {
				return nil, false, nil
			}
		}
	}

	ids := make([]int, 0, len(byID))
	for id, libraryType := range byID {
		if libraryType == recentTVTypeSeries || (explicitSeries && libraryType == "mixed") {
			ids = append(ids, id)
		}
	}
	ids = intersectOptionalInts(ids, access.AllowedLibraryIDs)
	ids = subtractInts(ids, access.DisabledLibraryIDs)
	if len(ids) == 0 {
		return []int{}, true, nil
	}
	return sortedUniqueInts(ids), true, nil
}

// List returns one page after event grouping. UniqueTargets collapses repeated
// target content IDs before counting and pagination; otherwise each scan event
// remains independently addressable.
func (r *RecentTVRepository) List(ctx context.Context, q RecentTVQuery) ([]RecentTVTarget, int, bool, error) {
	if r == nil || r.pool == nil || len(q.LibraryIDs) == 0 {
		return []RecentTVTarget{}, 0, false, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	if q.Offset < 0 {
		q.Offset = 0
	}

	args := []any{sortedUniqueInts(q.LibraryIDs)}
	argIdx := 2
	// episodeRowConditions scope the shared available_episode_rows CTE and may
	// only reference el/e/mf_event: series-level access conditions belong in
	// eventConditions so the series_without_episode_events anti-join still sees
	// every series that has any present episode file (see the CTE comment).
	episodeRowConditions := []string{
		"el.media_folder_id = ANY($1)",
		`EXISTS (
			SELECT 1 FROM media_files mf_event
			WHERE mf_event.episode_id = el.episode_id
			  AND mf_event.media_folder_id = el.media_folder_id
			  AND mf_event.missing_since IS NULL
		)`,
	}
	eventConditions := []string{}
	seriesConditions := []string{"mil.media_folder_id = ANY($1)", "mi.type = 'series'"}

	if q.SnapshotAt != nil {
		episodeRowConditions = append(episodeRowConditions, fmt.Sprintf("el.first_seen_at <= $%d", argIdx))
		seriesConditions = append(seriesConditions, fmt.Sprintf("mil.first_seen_at <= $%d", argIdx))
		args = append(args, *q.SnapshotAt)
		argIdx++
	}

	access := q.Access
	access.NamePrefix = ""
	appendLibraryAccessConditions("si.content_id", access, &eventConditions, &args, &argIdx)
	applyAccessFilter("si", AccessFilter{
		MaxContentRating:   access.MaxContentRating,
		ExcludedMediaTypes: access.ExcludedMediaTypes,
	}, &eventConditions, &args, &argIdx)
	appendAllowedContentCondition("si.content_id", access.AllowedContentIDs, &eventConditions, &args, &argIdx)

	appendLibraryAccessConditions("mi.content_id", access, &seriesConditions, &args, &argIdx)
	applyAccessFilter("mi", AccessFilter{
		MaxContentRating:   access.MaxContentRating,
		ExcludedMediaTypes: access.ExcludedMediaTypes,
	}, &seriesConditions, &args, &argIdx)
	appendAllowedContentCondition("mi.content_id", access.AllowedContentIDs, &seriesConditions, &args, &argIdx)

	if prefix := strings.TrimSpace(q.NamePrefix); prefix != "" {
		pattern := likePrefixPattern(strings.ToLower(prefix))
		eventConditions = append(eventConditions, fmt.Sprintf(
			"(LOWER(COALESCE(NULLIF(BTRIM(si.sort_title), ''), si.title)) LIKE $%d ESCAPE '\\' OR LOWER(aer.episode_title) LIKE $%d ESCAPE '\\')",
			argIdx, argIdx,
		))
		seriesConditions = append(seriesConditions, fmt.Sprintf(
			"LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) LIKE $%d ESCAPE '\\'",
			argIdx,
		))
		args = append(args, pattern)
		argIdx++
	}
	eventWhere := "TRUE"
	if len(eventConditions) > 0 {
		eventWhere = strings.Join(eventConditions, " AND ")
	}

	fetchLimit := limit
	if q.SkipTotal {
		fetchLimit++
	}
	limitIdx := argIdx
	offsetIdx := argIdx + 1
	args = append(args, fetchLimit, q.Offset)

	fromClause := "FROM totals\n\t\tLEFT JOIN page ON true"
	totalColumn := ", totals.total_count"
	if q.SkipTotal {
		fromClause = "FROM page"
		totalColumn = ""
	}

	// Two query shapes with identical results. The hot no-prefix path finds
	// and ranks every event with a narrow MAX-only aggregate that PostgreSQL
	// fuses into a parallel hash aggregate, then computes the expensive
	// per-event columns (episode counts, anchor episodes) for the requested
	// page only. A name prefix filters events by per-episode title, which
	// changes which rows belong to each event and cannot move past the
	// aggregation, so that path keeps the single-pass shape.
	var sqlText string
	if strings.TrimSpace(q.NamePrefix) == "" {
		if q.UniqueTargets {
			totalsCTE := `,
			totals AS (
				SELECT COUNT(*)::int AS total_count FROM unique_target_keys
			)`
			if q.SkipTotal {
				totalsCTE = ""
			}
			sqlText = buildUniqueRecentTVNoPrefixQuery(
				strings.Join(episodeRowConditions, " AND "),
				eventWhere,
				strings.Join(seriesConditions, " AND "),
				limitIdx,
				offsetIdx,
				totalsCTE,
				totalColumn,
				fromClause,
			)
		} else {
			totalsCTE := `,
		totals AS (
			SELECT ((SELECT COUNT(*) FROM event_keys) + (SELECT COUNT(*) FROM series_without_episode_events))::int AS total_count
		)`
			if q.SkipTotal {
				totalsCTE = ""
			}
			sqlText = fmt.Sprintf(`
		WITH raw_event_keys AS MATERIALIZED (
			-- Narrow first pass: one row per (series, scan run) availability
			-- event carrying only its added_at. It deliberately applies only
			-- folder/snapshot/file conditions: the
			-- series_without_episode_events anti-join must see every series
			-- with any present episode file, not only series the caller may
			-- surface. Series-level access conditions apply in event_keys.
			SELECT e.series_id,
			       el.first_seen_scan_run_id AS scan_run_id,
			       MAX(el.first_seen_at) AS added_at
			FROM episode_libraries el
			JOIN episodes e ON e.content_id = el.episode_id
			WHERE %[1]s
			GROUP BY e.series_id, el.first_seen_scan_run_id
		),
		event_keys AS (
			SELECT rek.series_id, rek.scan_run_id, rek.added_at
			FROM raw_event_keys rek
			JOIN media_items si ON si.content_id = rek.series_id
			WHERE %[2]s
		),
		series_without_episode_events AS MATERIALIZED (
			SELECT mi.content_id AS series_id,
			       MAX(mil.first_seen_at) AS added_at
			FROM media_item_libraries mil
			JOIN media_items mi ON mi.content_id = mil.content_id
			WHERE %[3]s
			  AND NOT EXISTS (
				SELECT 1 FROM raw_event_keys rek WHERE rek.series_id = mi.content_id
			  )
			GROUP BY mi.content_id
		),
		page_keys AS MATERIALIZED (
			-- rank() (not row_number) keeps added_at ties together, so the
			-- fully tiebroken ORDER BY on the page below still sees every
			-- event that could land on it.
			SELECT series_id, scan_run_id, added_at
			FROM (
				SELECT series_id, scan_run_id, added_at,
				       rank() OVER (ORDER BY added_at DESC) AS added_rank
				FROM (
					SELECT series_id, scan_run_id, added_at FROM event_keys
					UNION ALL
					SELECT series_id, NULL::text, added_at FROM series_without_episode_events
				) keys
			) ranked
			WHERE added_rank <= $%[4]d::bigint + $%[5]d::bigint
		),
		episode_events AS (
			SELECT pk.series_id, pk.scan_run_id, agg.added_at, agg.episode_count, agg.episode_id, agg.anchor_season_number
			FROM page_keys pk
			CROSS JOIN LATERAL (
				SELECT MAX(el.first_seen_at) AS added_at,
				       COUNT(DISTINCT el.episode_id) AS episode_count,
				       (array_agg(el.episode_id ORDER BY el.first_seen_at DESC, e.season_number DESC, e.episode_number DESC, el.episode_id ASC))[1] AS episode_id,
				       (array_agg(e.season_number ORDER BY el.first_seen_at DESC, e.season_number DESC, e.episode_number DESC, el.episode_id ASC))[1] AS anchor_season_number
				FROM episodes e
				JOIN episode_libraries el
				  ON el.episode_id = e.content_id
				 AND el.first_seen_scan_run_id IS NOT DISTINCT FROM pk.scan_run_id
				WHERE e.series_id = pk.series_id
				  AND %[1]s
			) agg
			-- An aggregate over zero rows still returns one row; this keeps
			-- the series_without_episode_events keys (no availability rows)
			-- out of the episode-event branch.
			WHERE agg.episode_count > 0
		),
		all_events AS (
			SELECT CASE WHEN scan_run_id IS NOT NULL AND episode_count = 1 THEN episode_id ELSE series_id END AS target_id,
			       CASE WHEN scan_run_id IS NOT NULL AND episode_count = 1 THEN 'episode'::text ELSE 'series'::text END AS target_type,
			       added_at,
			       COALESCE(scan_run_id, '') AS event_id,
			       series_id,
			       anchor_season_number,
			       CASE WHEN scan_run_id IS NOT NULL AND episode_count = 1 THEN episode_id END AS single_episode_id
			FROM episode_events
			UNION ALL
			SELECT swe.series_id, 'series'::text, swe.added_at, ''::text, swe.series_id, NULL::integer, NULL::text
			FROM series_without_episode_events swe
			JOIN page_keys pk ON pk.series_id = swe.series_id AND pk.scan_run_id IS NULL
		)%[6]s,
		page AS (
			SELECT target_id, target_type, added_at, event_id, series_id, anchor_season_number, single_episode_id
			FROM all_events
			ORDER BY added_at DESC, target_type ASC, target_id ASC, event_id ASC
			LIMIT $%[4]d OFFSET $%[5]d
		)
			`, strings.Join(episodeRowConditions, " AND "), eventWhere, strings.Join(seriesConditions, " AND "), limitIdx, offsetIdx, totalsCTE)
			sqlText += buildRecentTVResultQuery(totalColumn, fromClause)
		}
	} else {
		totalsCTE := `,
		totals AS (
			SELECT COUNT(*)::int AS total_count FROM filtered
		)`
		if q.SkipTotal {
			totalsCTE = ""
		}
		filteredSQL := `SELECT target_id, target_type, added_at, event_id, series_id, anchor_season_number, single_episode_id
			FROM all_events`
		if q.UniqueTargets {
			filteredSQL = `SELECT DISTINCT ON (target_id)
				target_id, target_type, added_at, event_id, series_id, anchor_season_number, single_episode_id
			FROM all_events
			ORDER BY target_id, added_at DESC, target_type ASC, event_id ASC`
		}
		sqlText = fmt.Sprintf(`
		WITH available_episode_rows AS MATERIALIZED (
			-- One shared pass over the availability rows for the requested
			-- libraries. It deliberately carries only folder/snapshot/file
			-- conditions: the series_without_episode_events anti-join below
			-- must see every series with any present episode file, not only
			-- series the caller may surface, so a series never turns into a
			-- bare "series added" event just because its episodes were
			-- filtered for this caller. Series-level access and name-prefix
			-- conditions apply in episode_events instead.
			SELECT el.episode_id,
			       el.first_seen_at,
			       el.first_seen_scan_run_id AS scan_run_id,
			       e.series_id,
			       e.season_number,
			       e.episode_number,
			       e.title AS episode_title
			FROM episode_libraries el
			JOIN episodes e ON e.content_id = el.episode_id
			WHERE %s
		),
		episode_events AS (
			SELECT aer.series_id,
			       aer.scan_run_id,
			       MAX(aer.first_seen_at) AS added_at,
			       COUNT(DISTINCT aer.episode_id) AS episode_count,
			       (array_agg(aer.episode_id ORDER BY aer.first_seen_at DESC, aer.season_number DESC, aer.episode_number DESC, aer.episode_id ASC))[1] AS episode_id,
			       (array_agg(aer.season_number ORDER BY aer.first_seen_at DESC, aer.season_number DESC, aer.episode_number DESC, aer.episode_id ASC))[1] AS anchor_season_number
			FROM available_episode_rows aer
			JOIN media_items si ON si.content_id = aer.series_id
			WHERE %s
			GROUP BY aer.series_id, aer.scan_run_id
		),
		series_without_episode_events AS (
			SELECT mi.content_id AS series_id,
			       NULL::text AS scan_run_id,
			       MAX(mil.first_seen_at) AS added_at,
			       0::bigint AS episode_count,
			       NULL::text AS episode_id,
			       NULL::integer AS anchor_season_number
			FROM media_item_libraries mil
			JOIN media_items mi ON mi.content_id = mil.content_id
			WHERE %s
			  AND NOT EXISTS (
				SELECT 1
				FROM available_episode_rows aer
				WHERE aer.series_id = mi.content_id
			  )
			GROUP BY mi.content_id
		),
		all_events AS (
			SELECT CASE WHEN scan_run_id IS NOT NULL AND episode_count = 1 THEN episode_id ELSE series_id END AS target_id,
			       CASE WHEN scan_run_id IS NOT NULL AND episode_count = 1 THEN 'episode'::text ELSE 'series'::text END AS target_type,
			       added_at,
			       COALESCE(scan_run_id, '') AS event_id,
			       series_id,
			       anchor_season_number,
			       CASE WHEN scan_run_id IS NOT NULL AND episode_count = 1 THEN episode_id END AS single_episode_id
			FROM episode_events
			UNION ALL
			SELECT series_id, 'series'::text, added_at, ''::text, series_id, NULL::integer, NULL::text
			FROM series_without_episode_events
		),
		filtered AS (
			%s
		)%s,
		page AS (
			SELECT target_id, target_type, added_at, event_id, series_id, anchor_season_number, single_episode_id
			FROM filtered
			ORDER BY added_at DESC, target_type ASC, target_id ASC, event_id ASC
			LIMIT $%d OFFSET $%d
		)
		`, strings.Join(episodeRowConditions, " AND "), eventWhere, strings.Join(seriesConditions, " AND "), filteredSQL, totalsCTE, limitIdx, offsetIdx)
		sqlText += buildRecentTVResultQuery(totalColumn, fromClause)
	}

	rows, err := r.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, 0, false, fmt.Errorf("listing recently-added TV events: %w", err)
	}
	defer rows.Close()

	targets := make([]RecentTVTarget, 0, limit)
	total := 0
	for rows.Next() {
		var contentID, targetType, playContentID *string
		var addedAt *time.Time
		scanArgs := []any{&contentID, &targetType, &addedAt, &playContentID}
		if !q.SkipTotal {
			scanArgs = append(scanArgs, &total)
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, 0, false, fmt.Errorf("scanning recently-added TV event: %w", err)
		}
		if contentID != nil && targetType != nil && addedAt != nil {
			target := RecentTVTarget{ContentID: *contentID, Type: *targetType, AddedAt: *addedAt}
			if playContentID != nil {
				target.PlayContentID = *playContentID
			}
			targets = append(targets, target)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, fmt.Errorf("iterating recently-added TV events: %w", err)
	}
	if q.SkipTotal {
		hasMore := len(targets) > limit
		if hasMore {
			targets = targets[:limit]
		}
		return targets, 0, hasMore, nil
	}
	return targets, total, q.Offset+len(targets) < total, nil
}

// buildUniqueRecentTVNoPrefixQuery keeps the no-prefix path's expensive anchor
// aggregation page-bounded while selecting and counting unique card targets.
// MIN/MAX is enough to distinguish a one-episode scan event from a multi-
// episode event even when one episode has availability rows in several folders.
func buildUniqueRecentTVNoPrefixQuery(
	episodeConditions string,
	eventConditions string,
	seriesConditions string,
	limitIdx int,
	offsetIdx int,
	totalsCTE string,
	totalColumn string,
	fromClause string,
) string {
	return fmt.Sprintf(`
	WITH raw_event_keys AS MATERIALIZED (
		-- Keep this first pass narrow: target identity needs only scalar
		-- aggregates. Episode counts and playback anchors remain page-bound.
		SELECT e.series_id,
		       el.first_seen_scan_run_id AS scan_run_id,
		       MAX(el.first_seen_at) AS added_at,
		       MIN(el.episode_id) AS min_episode_id,
		       MAX(el.episode_id) AS max_episode_id
		FROM episode_libraries el
		JOIN episodes e ON e.content_id = el.episode_id
		WHERE %[1]s
		GROUP BY e.series_id, el.first_seen_scan_run_id
	),
	series_without_episode_events AS MATERIALIZED (
		SELECT mi.content_id AS series_id,
		       MAX(mil.first_seen_at) AS added_at
		FROM media_item_libraries mil
		JOIN media_items mi ON mi.content_id = mil.content_id
		WHERE %[3]s
		  AND NOT EXISTS (
			SELECT 1 FROM raw_event_keys rek WHERE rek.series_id = mi.content_id
		  )
		GROUP BY mi.content_id
	),
	target_keys AS (
		SELECT CASE
		         WHEN rek.scan_run_id IS NOT NULL AND rek.min_episode_id = rek.max_episode_id THEN rek.min_episode_id
		         ELSE rek.series_id
		       END AS target_id,
		       CASE
		         WHEN rek.scan_run_id IS NOT NULL AND rek.min_episode_id = rek.max_episode_id THEN 'episode'::text
		         ELSE 'series'::text
		       END AS target_type,
		       rek.added_at,
		       COALESCE(rek.scan_run_id, '') AS event_id,
		       rek.series_id,
		       rek.scan_run_id
		FROM raw_event_keys rek
		JOIN media_items si ON si.content_id = rek.series_id
		WHERE %[2]s
		UNION ALL
		SELECT series_id, 'series'::text, added_at, ''::text, series_id, NULL::text
		FROM series_without_episode_events
	),
	unique_target_keys AS MATERIALIZED (
		SELECT DISTINCT ON (target_id)
		       target_id, target_type, added_at, event_id, series_id, scan_run_id
		FROM target_keys
		ORDER BY target_id, added_at DESC, target_type ASC, event_id ASC
	)%[6]s,
	page_keys AS MATERIALIZED (
		SELECT target_id, target_type, added_at, event_id, series_id, scan_run_id
		FROM unique_target_keys
		ORDER BY added_at DESC, target_type ASC, target_id ASC, event_id ASC
		LIMIT $%[4]d OFFSET $%[5]d
	),
	page AS (
		SELECT pk.target_id,
		       pk.target_type,
		       pk.added_at,
		       pk.event_id,
		       pk.series_id,
		       anchor.anchor_season_number,
		       CASE WHEN pk.target_type = 'episode' THEN pk.target_id END AS single_episode_id
		FROM page_keys pk
		LEFT JOIN LATERAL (
			SELECT (array_agg(e.season_number ORDER BY el.first_seen_at DESC, e.season_number DESC, e.episode_number DESC, el.episode_id ASC))[1] AS anchor_season_number
			FROM episodes e
			JOIN episode_libraries el
			  ON el.episode_id = e.content_id
			 AND el.first_seen_scan_run_id IS NOT DISTINCT FROM pk.scan_run_id
			WHERE pk.target_type = 'series'
			  AND e.series_id = pk.series_id
			  AND %[1]s
		) anchor ON true
	)
	`, episodeConditions, eventConditions, seriesConditions, limitIdx, offsetIdx, totalsCTE) + buildRecentTVResultQuery(totalColumn, fromClause)
}

// buildRecentTVResultQuery renders the common result projection after each
// query shape has produced the same page columns.
func buildRecentTVResultQuery(totalColumn, fromClause string) string {
	return fmt.Sprintf(`
	SELECT page.target_id, page.target_type, page.added_at,
	       COALESCE(page.single_episode_id, play_target.content_id) AS play_content_id%s
	%s
	-- Anchor hint only: profile-independent by design, so this page can be
	-- shared through the process-global resolved-list cache. Playback quality
	-- is enforced later by PlayableTargetResolver, which re-checks this hint
	-- before using it (see RecentTVTarget.PlayContentID).
	LEFT JOIN LATERAL (
		SELECT e_play.content_id
		FROM episodes e_play
		WHERE page.target_type = 'series'
		  AND e_play.series_id = page.series_id
		  AND e_play.season_number = page.anchor_season_number
		  AND EXISTS (
			SELECT 1
			FROM episode_libraries el_play
			WHERE el_play.episode_id = e_play.content_id
			  AND el_play.media_folder_id = ANY($1)
			  AND EXISTS (
				SELECT 1 FROM media_files mf_play
				WHERE mf_play.episode_id = el_play.episode_id
				  AND mf_play.media_folder_id = el_play.media_folder_id
				  AND mf_play.missing_since IS NULL
			  )
		  )
		ORDER BY e_play.episode_number ASC, e_play.content_id ASC
		LIMIT 1
	) play_target ON true
	ORDER BY page.added_at DESC, page.target_type ASC, page.target_id ASC, page.event_id ASC
	`, totalColumn, fromClause)
}

func appendAllowedContentCondition(column string, allowed []string, conditions *[]string, args *[]any, argIdx *int) {
	if allowed == nil {
		return
	}
	if len(allowed) == 0 {
		*conditions = append(*conditions, "1 = 0")
		return
	}
	*conditions = append(*conditions, fmt.Sprintf("%s = ANY($%d)", column, *argIdx))
	*args = append(*args, allowed)
	*argIdx++
}

func uniquePositiveInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
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

func sortedUniqueInts(values []int) []int {
	values = uniquePositiveInts(values)
	slices.Sort(values)
	return values
}

// intersectOptionalInts is intersectInts with the access-layer convention that
// a nil allow-list means unrestricted rather than "allow nothing".
func intersectOptionalInts(values, allowed []int) []int {
	if allowed == nil {
		return values
	}
	return intersectInts(values, allowed)
}

func subtractInts(values, denied []int) []int {
	if len(denied) == 0 {
		return values
	}
	deniedSet := make(map[int]struct{}, len(denied))
	for _, value := range denied {
		deniedSet[value] = struct{}{}
	}
	result := make([]int, 0, len(values))
	for _, value := range values {
		if _, denied := deniedSet[value]; !denied {
			result = append(result, value)
		}
	}
	return result
}
