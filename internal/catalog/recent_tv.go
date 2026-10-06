package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	recentTVTypeEpisode = "episode"
	recentTVTypeSeries  = "series"

	// recentTVArrivalGap is how long a show's arrivals stay one availability
	// event: an episode first seen within this gap of the show's previous
	// arrival joins that event, so a chain of imports (Sonarr sends one per
	// episode) becomes one series card instead of one card per episode.
	recentTVArrivalGap = 2 * time.Hour
)

// RecentTVTarget is one card-producing TV availability event: a run of a
// show's episode arrivals with no gap longer than recentTVArrivalGap. An event
// with one episode targets that episode; a larger event targets the series. By
// default, separate events remain separate even when two multi-episode events
// target the same show; RecentTVQuery.UniqueTargets can collapse them for keyed
// rows.
type RecentTVTarget struct {
	ContentID string
	EventID   string
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
	CursorPaging  bool
	After         *QueryCursor
	Seek          *int // explicit zero-based jump; ordinary continuation never uses OFFSET
}

// RecentTVRepository resolves arrival-grouped episode availability into
// episode or series card targets.
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
// target content IDs before counting and pagination; otherwise each arrival
// event remains independently addressable.
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

	if q.CursorPaging {
		q.Offset = 0
		if q.Seek != nil {
			if *q.Seek < 0 || *q.Seek > 10000000 {
				return nil, 0, false, fmt.Errorf("jump index must be between 0 and 10000000")
			}
			q.After = nil
			if *q.Seek > 0 {
				boundaryQuery := q
				boundaryQuery.CursorPaging, boundaryQuery.Seek = false, nil
				boundaryQuery.Limit, boundaryQuery.Offset = 1, *q.Seek-1
				boundary, total, _, err := r.List(ctx, boundaryQuery)
				if err != nil {
					return nil, 0, false, err
				}
				if len(boundary) == 0 {
					return []RecentTVTarget{}, total, false, nil
				}
				q.After = recentTVCursor(boundary[0], *q.Seek)
			}
		}
	}

	c := buildRecentTVConditions(q)
	args, argIdx := c.args, c.argIdx
	episodeRowConditions, eventWhere, eventKeyFilter, seriesConditions := c.episodeRows, c.eventWhere, c.eventKeyFilter, c.series

	pageWhere := ""
	if q.CursorPaging {
		predicate, cursorArgs, err := cursorSeekSQL(recentTVCursorTerms(), q.After, argIdx)
		if err != nil {
			return nil, 0, false, err
		}
		if predicate != "" {
			pageWhere = "WHERE " + predicate
		}
		args = append(args, cursorArgs...)
		argIdx += len(cursorArgs)
	}
	fetchLimit := limit
	if q.SkipTotal || q.CursorPaging {
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
	// and ranks every event from narrow (series, arrival, episode) rows, then
	// computes the expensive per-event columns (episode counts, anchor
	// episodes) for the requested page only. A name prefix filters events by
	// per-episode title, which changes which rows count toward each event and
	// cannot move past the aggregation, so that path keeps the single-pass
	// shape.
	var sqlText string
	if strings.TrimSpace(q.NamePrefix) == "" {
		if q.UniqueTargets || q.CursorPaging {
			totalsCTE := `,
			totals AS (
				SELECT COUNT(*)::int AS total_count FROM selected_target_keys
			)`
			if q.SkipTotal {
				totalsCTE = ""
			}
			sqlText = buildRecentTVNoPrefixQuery(
				recentTVKeysQuery{
					episodeConditions: episodeRowConditions,
					arrivalRowsSQL:    recentTVArrivalRowsSQL(episodeRowConditions),
					eventKeyFilter:    eventKeyFilter,
					seriesWithoutEpisodesSQL: recentTVSeriesWithoutEpisodesSQL(seriesConditions,
						"SELECT 1 FROM raw_event_keys rek WHERE rek.series_id = mi.content_id"),
					limitIdx:      limitIdx,
					offsetIdx:     offsetIdx,
					totalsCTE:     totalsCTE,
					totalColumn:   totalColumn,
					fromClause:    fromClause,
					uniqueTargets: q.UniqueTargets,
					pageWhere:     pageWhere,
				},
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
			-- Narrow first pass: one row per (series, arrival event) carrying
			-- only its bounds. It deliberately applies only
			-- folder/snapshot/file conditions: the
			-- series_without_episode_events anti-join must see every series
			-- with any present episode file, not only series the caller may
			-- surface. Series-level access conditions apply in event_keys.
			SELECT series_id, event_start, MAX(first_seen_at) AS added_at
			FROM (%[7]s) arrival_events
			GROUP BY series_id, event_start
		),
		event_keys AS (
			SELECT rek.series_id, rek.event_start, rek.added_at
			FROM raw_event_keys rek
			%[2]s
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
			SELECT series_id, event_start, added_at
			FROM (
				SELECT series_id, event_start, added_at,
				       rank() OVER (ORDER BY added_at DESC) AS added_rank
				FROM (
					SELECT series_id, event_start, added_at FROM event_keys
					UNION ALL
					SELECT series_id, NULL::timestamptz, added_at FROM series_without_episode_events
				) keys
			) ranked
			WHERE added_rank <= $%[4]d::bigint + $%[5]d::bigint
		),
		episode_events AS (
			SELECT pk.series_id, pk.event_start, agg.added_at, agg.episode_count, agg.episode_id, agg.anchor_season_number
			FROM page_keys pk
			CROSS JOIN LATERAL (
				-- An event's arrivals are exactly the series' rows inside its
				-- time bounds: events of one series never overlap.
				SELECT MAX(el.first_seen_at) AS added_at,
				       COUNT(DISTINCT el.episode_id) AS episode_count,
				       (array_agg(el.episode_id ORDER BY el.first_seen_at DESC, e.season_number DESC, e.episode_number DESC, el.episode_id ASC))[1] AS episode_id,
				       (array_agg(e.season_number ORDER BY el.first_seen_at DESC, e.season_number DESC, e.episode_number DESC, el.episode_id ASC))[1] AS anchor_season_number
				FROM episodes e
				JOIN episode_libraries el
				  ON el.episode_id = e.content_id
				 AND el.first_seen_at BETWEEN pk.event_start AND pk.added_at
				WHERE e.series_id = pk.series_id
				  AND %[1]s
			) agg
			-- An aggregate over zero rows still returns one row; this keeps
			-- the series_without_episode_events keys (no availability rows)
			-- out of the episode-event branch.
			WHERE agg.episode_count > 0
		),
		all_events AS (
			SELECT CASE WHEN episode_count = 1 THEN episode_id ELSE series_id END AS target_id,
			       CASE WHEN episode_count = 1 THEN 'episode'::text ELSE 'series'::text END AS target_type,
			       added_at,
			       event_start,
			       series_id,
			       anchor_season_number,
			       CASE WHEN episode_count = 1 THEN episode_id END AS single_episode_id
			FROM episode_events
			UNION ALL
			SELECT swe.series_id, 'series'::text, swe.added_at, NULL::timestamptz, swe.series_id, NULL::integer, NULL::text
			FROM series_without_episode_events swe
			JOIN page_keys pk ON pk.series_id = swe.series_id AND pk.event_start IS NULL
		)%[6]s,
		page AS (
			SELECT target_id, target_type, added_at, event_start, series_id, anchor_season_number, single_episode_id
			FROM all_events
			ORDER BY added_at DESC, target_type ASC, target_id ASC, event_start ASC NULLS FIRST
			LIMIT $%[4]d OFFSET $%[5]d
		)
			`, episodeRowConditions, eventKeyFilter, seriesConditions, limitIdx, offsetIdx, totalsCTE,
				recentTVArrivalEventsSQL(recentTVArrivalRowsSQL(episodeRowConditions)))
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
		filteredSQL := `SELECT target_id, target_type, added_at, event_start, series_id, anchor_season_number, single_episode_id
			FROM all_events`
		if q.UniqueTargets {
			filteredSQL = `SELECT DISTINCT ON (target_id)
				target_id, target_type, added_at, event_start, series_id, anchor_season_number, single_episode_id
			FROM all_events
			ORDER BY target_id, added_at DESC, target_type ASC, event_start ASC NULLS FIRST`
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
			-- conditions apply in episode_events instead, after arrival
			-- events are formed, so a name prefix never splits an event.
			%s
		),
		episode_events AS (
			SELECT aer.series_id,
			       aer.event_start,
			       MAX(aer.first_seen_at) AS added_at,
			       COUNT(DISTINCT aer.episode_id) AS episode_count,
			       (array_agg(aer.episode_id ORDER BY aer.first_seen_at DESC, aer.season_number DESC, aer.episode_number DESC, aer.episode_id ASC))[1] AS episode_id,
			       (array_agg(aer.season_number ORDER BY aer.first_seen_at DESC, aer.season_number DESC, aer.episode_number DESC, aer.episode_id ASC))[1] AS anchor_season_number
			FROM available_episode_rows aer
			JOIN media_items si ON si.content_id = aer.series_id
			WHERE %s
			GROUP BY aer.series_id, aer.event_start
		),
		series_without_episode_events AS (
			SELECT mi.content_id AS series_id,
			       NULL::timestamptz AS event_start,
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
			SELECT CASE WHEN episode_count = 1 THEN episode_id ELSE series_id END AS target_id,
			       CASE WHEN episode_count = 1 THEN 'episode'::text ELSE 'series'::text END AS target_type,
			       added_at,
			       event_start,
			       series_id,
			       anchor_season_number,
			       CASE WHEN episode_count = 1 THEN episode_id END AS single_episode_id
			FROM episode_events
			UNION ALL
			SELECT series_id, 'series'::text, added_at, NULL::timestamptz, series_id, NULL::integer, NULL::text
			FROM series_without_episode_events
		),
		filtered AS (
			%s
		)%s,
		page AS (
			SELECT target_id, target_type, added_at, event_start, series_id, anchor_season_number, single_episode_id
			FROM filtered
			%s
			ORDER BY added_at DESC, target_type ASC, target_id ASC, event_start ASC NULLS FIRST
			LIMIT $%d OFFSET $%d
		)
		`, recentTVArrivalEventsSQL(`SELECT el.episode_id,
			       el.first_seen_at,
			       e.series_id,
			       e.season_number,
			       e.episode_number,
			       e.title AS episode_title
			FROM episode_libraries el
			JOIN episodes e ON e.content_id = el.episode_id
			WHERE `+episodeRowConditions),
			eventWhere, seriesConditions, filteredSQL, totalsCTE, pageWhere, limitIdx, offsetIdx)
		sqlText += buildRecentTVResultQuery(totalColumn, fromClause)
	}

	targets, total, err := queryRecentTVTargets(ctx, r.pool, sqlText, args, !q.SkipTotal, limit)
	if err != nil {
		return nil, 0, false, err
	}
	if q.SkipTotal || q.CursorPaging {
		hasMore := len(targets) > limit
		if hasMore {
			targets = targets[:limit]
		}
		return targets, total, hasMore, nil
	}
	return targets, total, q.Offset+len(targets) < total, nil
}

// ListNewest returns the newest unique card targets, the home row's
// UniqueTargets page, without grouping the whole library. It reports whether
// more targets exist instead of counting them. Only LibraryIDs, Access,
// SnapshotAt, and Limit apply.
//
// It picks a cutoff, the Nth newest availability row, and groups the complete
// history of every show with an arrival at or after it. Every event whose
// newest arrival is at or after the cutoff belongs to such a show, so those
// events are exact; older events of other shows are not needed to rank them.
// When the cutoff yields too few targets it moves back, and when the window
// grows past recentTVNewestMaxWindow (typically just after a bulk import) it
// falls back to the full List query.
func (r *RecentTVRepository) ListNewest(ctx context.Context, q RecentTVQuery) ([]RecentTVTarget, bool, error) {
	if r == nil || r.pool == nil || len(q.LibraryIDs) == 0 {
		return []RecentTVTarget{}, false, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	q = RecentTVQuery{LibraryIDs: sortedUniqueInts(q.LibraryIDs), Access: q.Access, SnapshotAt: q.SnapshotAt, Limit: limit}

	targets, found, err := r.listNewestBounded(ctx, q)
	if err != nil || found {
		if len(targets) > limit {
			return targets[:limit], true, err
		}
		return targets, false, err
	}
	q.UniqueTargets, q.SkipTotal = true, true
	targets, _, hasMore, err := r.List(ctx, q)
	return targets, hasMore, err
}

const (
	recentTVNewestWindowGrowth = 4
	recentTVNewestMaxWindow    = 8192
)

// listNewestBounded runs the bounded windows of ListNewest. found reports
// that a window produced more than q.Limit targets (targets holds q.Limit+1);
// otherwise the caller must fall back to the full query.
func (r *RecentTVRepository) listNewestBounded(ctx context.Context, q RecentTVQuery) (targets []RecentTVTarget, found bool, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, false, fmt.Errorf("beginning recently-added TV tx: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	// The planner prices the per-show lateral lookups far above their real
	// cost, which can cross jit_above_cost; compiling then costs several times
	// more than executing the few-millisecond query.
	if _, err := tx.Exec(ctx, "SET LOCAL jit = off"); err != nil {
		return nil, false, fmt.Errorf("disabling JIT for recently-added TV: %w", err)
	}

	for window := (q.Limit + 1) * recentTVNewestWindowGrowth; window <= recentTVNewestMaxWindow; window *= recentTVNewestWindowGrowth {
		var cutoff time.Time
		err := tx.QueryRow(ctx, `
			SELECT recent.first_seen_at
			FROM unnest($1::int[]) AS folder(id)
			CROSS JOIN LATERAL (
				SELECT el.first_seen_at
				FROM episode_libraries el
				WHERE el.media_folder_id = folder.id
				ORDER BY el.first_seen_at DESC
				LIMIT $2
			) recent
			ORDER BY recent.first_seen_at DESC
			OFFSET $2 - 1
			LIMIT 1
		`, q.LibraryIDs, window).Scan(&cutoff)
		if errors.Is(err, pgx.ErrNoRows) {
			// Fewer arrivals than the window: grouping everything is the
			// full query's job.
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("finding recently-added TV cutoff: %w", err)
		}
		sqlText, args := buildRecentTVNewestQuery(q, cutoff)
		targets, _, err := queryRecentTVTargets(ctx, tx, sqlText, args, false, q.Limit+1)
		if err != nil {
			return nil, false, err
		}
		if len(targets) > q.Limit {
			return targets, true, nil
		}
	}
	return nil, false, nil
}

// buildRecentTVNewestQuery renders one ListNewest window: unique targets whose
// newest arrival is at or after cutoff, grouped from the full availability
// history of the shows active since cutoff.
func buildRecentTVNewestQuery(q RecentTVQuery, cutoff time.Time) (string, []any) {
	c := buildRecentTVConditions(q)
	args := append(c.args, cutoff, q.Limit+1, 0)
	cutoffIdx, limitIdx, offsetIdx := c.argIdx, c.argIdx+1, c.argIdx+2
	// DISTINCT runs once over the few index rows past the cutoff; the lateral
	// then reads each active show's history through episodes(series_id).
	// Shows the caller cannot see are skipped here, not after grouping, so a
	// restricted profile's windows stay as cheap as an open one's.
	arrivalRows := fmt.Sprintf(`SELECT active.series_id, history.episode_id, history.first_seen_at
		FROM (
			SELECT DISTINCT e.series_id
			FROM episode_libraries el
			JOIN episodes e ON e.content_id = el.episode_id
			JOIN media_items si ON si.content_id = e.series_id
			WHERE el.media_folder_id = ANY($1)
			  AND el.first_seen_at >= $%[2]d
			  AND %[3]s
		) active
		CROSS JOIN LATERAL (
			SELECT el.episode_id, el.first_seen_at
			FROM episodes e
			JOIN episode_libraries el ON el.episode_id = e.content_id
			WHERE e.series_id = active.series_id
			  AND %[1]s
		) history`, c.episodeRows, cutoffIdx, c.eventWhere)
	recentSeries := fmt.Sprintf(`%s
		  AND mi.content_id IN (
			SELECT mil_recent.content_id
			FROM media_item_libraries mil_recent
			WHERE mil_recent.media_folder_id = ANY($1)
			  AND mil_recent.first_seen_at >= $%d
		  )`, c.series, cutoffIdx)
	return buildRecentTVNoPrefixQuery(recentTVKeysQuery{
		episodeConditions: c.episodeRows,
		arrivalRowsSQL:    arrivalRows,
		eventKeyFilter:    c.eventKeyFilter,
		// raw_event_keys covers active shows only, so the anti-join must
		// probe availability directly.
		seriesWithoutEpisodesSQL: recentTVSeriesWithoutEpisodesSQL(recentSeries,
			"SELECT 1 FROM episodes e JOIN episode_libraries el ON el.episode_id = e.content_id WHERE e.series_id = mi.content_id AND "+c.episodeRows),
		limitIdx:      limitIdx,
		offsetIdx:     offsetIdx,
		fromClause:    "FROM page",
		uniqueTargets: true,
		pageWhere:     fmt.Sprintf("WHERE added_at >= $%d", cutoffIdx),
	}), args
}

// recentTVConditions are the SQL fragments and bind arguments every
// recently-added TV query shape shares. $1 is always the requested library IDs.
type recentTVConditions struct {
	args   []any
	argIdx int // next free bind placeholder
	// episodeRows scope availability rows and may only reference el/e/mf_event:
	// series-level access conditions belong in eventWhere so the
	// series_without_episode_events anti-join still sees every series that has
	// any present episode file.
	episodeRows    string
	eventWhere     string // series-level conditions over si (and aer with a name prefix)
	eventKeyFilter string // join+WHERE applying eventWhere to raw_event_keys rek, or empty
	series         string // conditions over mil/mi for series without episode events
}

func buildRecentTVConditions(q RecentTVQuery) recentTVConditions {
	args := []any{sortedUniqueInts(q.LibraryIDs)}
	argIdx := 2
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
		MaturityLimits:     access.MaturityLimits,
		ExcludedMediaTypes: access.ExcludedMediaTypes,
	}, &eventConditions, &args, &argIdx)
	appendAllowedContentCondition("si.content_id", access.AllowedContentIDs, &eventConditions, &args, &argIdx)

	appendLibraryAccessConditions("mi.content_id", access, &seriesConditions, &args, &argIdx)
	applyAccessFilter("mi", AccessFilter{
		MaturityLimits:     access.MaturityLimits,
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
	c := recentTVConditions{
		args:        args,
		argIdx:      argIdx,
		episodeRows: strings.Join(episodeRowConditions, " AND "),
		eventWhere:  "TRUE",
		series:      strings.Join(seriesConditions, " AND "),
	}
	// eventKeyFilter scopes the no-prefix event keys. Without series-level
	// conditions it skips the media_items join: episodes.series_id is a
	// foreign key, so the join could not drop a key.
	if len(eventConditions) > 0 {
		c.eventWhere = strings.Join(eventConditions, " AND ")
		c.eventKeyFilter = "JOIN media_items si ON si.content_id = rek.series_id\n\t\t\tWHERE " + c.eventWhere
	}
	return c

}

// queryRecentTVTargets runs one rendered recently-added TV query and scans its
// result projection (see buildRecentTVResultQuery).
func queryRecentTVTargets(ctx context.Context, db recentTVQuerier, sqlText string, args []any, withTotal bool, capacity int) ([]RecentTVTarget, int, error) {
	rows, err := db.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing recently-added TV events: %w", err)
	}
	defer rows.Close()

	targets := make([]RecentTVTarget, 0, capacity)
	total := 0
	for rows.Next() {
		var contentID, targetType, playContentID, eventID *string
		var addedAt *time.Time
		scanArgs := []any{&contentID, &targetType, &addedAt, &playContentID, &eventID}
		if withTotal {
			scanArgs = append(scanArgs, &total)
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, 0, fmt.Errorf("scanning recently-added TV event: %w", err)
		}
		if contentID != nil && targetType != nil && addedAt != nil {
			target := RecentTVTarget{ContentID: *contentID, Type: *targetType, AddedAt: *addedAt}
			if eventID != nil {
				target.EventID = *eventID
			}
			if playContentID != nil {
				target.PlayContentID = *playContentID
			}
			targets = append(targets, target)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating recently-added TV events: %w", err)
	}
	return targets, total, nil
}

type recentTVQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// recentTVKeysQuery parameterizes buildRecentTVNoPrefixQuery.
type recentTVKeysQuery struct {
	episodeConditions string // availability row scope, also used by page anchors
	// arrivalRowsSQL yields the (series_id, episode_id, first_seen_at) rows
	// arrival events are grouped from.
	arrivalRowsSQL           string
	eventKeyFilter           string
	seriesWithoutEpisodesSQL string // body of series_without_episode_events
	limitIdx, offsetIdx      int
	totalsCTE                string
	totalColumn              string
	fromClause               string
	uniqueTargets            bool
	pageWhere                string
}

// buildRecentTVNoPrefixQuery keeps the no-prefix path's expensive anchor
// aggregation page-bounded while selecting and counting unique card targets.
// MIN/MAX is enough to distinguish a one-episode arrival event from a multi-
// episode event even when one episode has availability rows in several folders.
func buildRecentTVNoPrefixQuery(k recentTVKeysQuery) string {
	selection := "SELECT target_id, target_type, added_at, series_id, event_start FROM target_keys"
	if k.uniqueTargets {
		selection = `SELECT DISTINCT ON (target_id) target_id, target_type, added_at, series_id, event_start
		FROM target_keys ORDER BY target_id, added_at DESC, target_type ASC, event_start ASC NULLS FIRST`
	}
	return fmt.Sprintf(`
	WITH raw_event_keys AS MATERIALIZED (
		-- Keep this first pass narrow: target identity needs only scalar
		-- aggregates. Episode counts and playback anchors remain page-bound.
		SELECT series_id,
		       event_start,
		       MAX(first_seen_at) AS added_at,
		       MIN(episode_id) AS min_episode_id,
		       MAX(episode_id) AS max_episode_id
		FROM (%[9]s) arrival_events
		GROUP BY series_id, event_start
	),
	series_without_episode_events AS MATERIALIZED (
		%[3]s
	),
	target_keys AS (
		SELECT CASE
		         WHEN rek.min_episode_id = rek.max_episode_id THEN rek.min_episode_id
		         ELSE rek.series_id
		       END AS target_id,
		       CASE
		         WHEN rek.min_episode_id = rek.max_episode_id THEN 'episode'::text
		         ELSE 'series'::text
		       END AS target_type,
		       rek.added_at,
		       rek.series_id,
		       rek.event_start
		FROM raw_event_keys rek
		%[2]s
		UNION ALL
		SELECT series_id, 'series'::text, added_at, series_id, NULL::timestamptz
		FROM series_without_episode_events
	),
	selected_target_keys AS MATERIALIZED (
		%[7]s
	)%[6]s,
	page_keys AS MATERIALIZED (
		SELECT target_id, target_type, added_at, series_id, event_start
		FROM selected_target_keys
		%[8]s
		ORDER BY added_at DESC, target_type ASC, target_id ASC, event_start ASC NULLS FIRST
		LIMIT $%[4]d OFFSET $%[5]d
	),
	page AS (
		SELECT pk.target_id,
		       pk.target_type,
		       pk.added_at,
		       pk.event_start,
		       pk.series_id,
		       anchor.anchor_season_number,
		       CASE WHEN pk.target_type = 'episode' THEN pk.target_id END AS single_episode_id
		FROM page_keys pk
		LEFT JOIN LATERAL (
			-- An event's arrivals are exactly the series' rows inside its time
			-- bounds: events of one series never overlap.
			SELECT (array_agg(e.season_number ORDER BY el.first_seen_at DESC, e.season_number DESC, e.episode_number DESC, el.episode_id ASC))[1] AS anchor_season_number
			FROM episodes e
			JOIN episode_libraries el
			  ON el.episode_id = e.content_id
			 AND el.first_seen_at BETWEEN pk.event_start AND pk.added_at
			WHERE pk.target_type = 'series'
			  AND e.series_id = pk.series_id
			  AND %[1]s
		) anchor ON true
	)
	`, k.episodeConditions, k.eventKeyFilter, k.seriesWithoutEpisodesSQL, k.limitIdx, k.offsetIdx, k.totalsCTE, selection, k.pageWhere,
		recentTVArrivalEventsSQL(k.arrivalRowsSQL)) + buildRecentTVResultQuery(k.totalColumn, k.fromClause)
}

// recentTVSeriesWithoutEpisodesSQL selects series memberships that have no
// available episode at all, as bare series events. hasAvailableEpisodeSQL is a
// correlated subquery over mi that finds any available episode row.
func recentTVSeriesWithoutEpisodesSQL(seriesConditions, hasAvailableEpisodeSQL string) string {
	return `SELECT mi.content_id AS series_id,
		       MAX(mil.first_seen_at) AS added_at
		FROM media_item_libraries mil
		JOIN media_items mi ON mi.content_id = mil.content_id
		WHERE ` + seriesConditions + `
		  AND NOT EXISTS (` + hasAvailableEpisodeSQL + `)
		GROUP BY mi.content_id`
}

// recentTVArrivalRowsSQL selects the narrow availability rows arrival events
// are built from.
func recentTVArrivalRowsSQL(episodeConditions string) string {
	return `SELECT e.series_id, el.episode_id, el.first_seen_at
		FROM episode_libraries el
		JOIN episodes e ON e.content_id = el.episode_id
		WHERE ` + episodeConditions
}

// recentTVArrivalEventsSQL passes every column of rowsSQL through and adds
// event_start, the first arrival of the event each row belongs to. rowsSQL
// must expose series_id and first_seen_at. A row starts a new event when the
// series' previous arrival is more than recentTVArrivalGap older, so events
// are contiguous, non-overlapping time ranges per series. Both windows order
// by arrival time alone: LAG may pick any same-instant peer, but the default
// RANGE frame hands every peer the same event_start, so the result needs no
// tiebreaker (and the sort skips one text key).
// Grouping by arrival time instead of scan run keeps imports that each get
// their own scan run (one arr webhook per episode) in one event.
func recentTVArrivalEventsSQL(rowsSQL string) string {
	return fmt.Sprintf(`SELECT arrivals.*,
		       MAX(CASE WHEN arrivals.starts_event THEN arrivals.first_seen_at END) OVER (
		           PARTITION BY arrivals.series_id
		           ORDER BY arrivals.first_seen_at
		       ) AS event_start
		FROM (
			SELECT arrival_rows.*,
			       COALESCE(LAG(arrival_rows.first_seen_at) OVER (
			           PARTITION BY arrival_rows.series_id
			           ORDER BY arrival_rows.first_seen_at
			       ) < arrival_rows.first_seen_at - interval '%d seconds', true) AS starts_event
			FROM (%s) arrival_rows
		) arrivals`, int64(recentTVArrivalGap/time.Second), rowsSQL)
}

// recentTVEventIDSQL renders an arrival event's start as its stable cursor
// identity. Events without episode arrivals use the empty event ID. The
// fixed-width UTC format sorts like the timestamp, so relations order by the
// raw event_start (NULLS FIRST) and format only the rows they return.
func recentTVEventIDSQL(eventStart string) string {
	return fmt.Sprintf(`COALESCE(to_char(%s AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '')`, eventStart)
}

// buildRecentTVResultQuery renders the common result projection after each
// query shape has produced the same page columns.
func buildRecentTVResultQuery(totalColumn, fromClause string) string {
	return fmt.Sprintf(`
	SELECT page.target_id, page.target_type, page.added_at,
	       COALESCE(page.single_episode_id, play_target.content_id) AS play_content_id, %[3]s AS event_id%[1]s
	%[2]s
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
	ORDER BY page.added_at DESC, page.target_type ASC, page.target_id ASC, page.event_start ASC NULLS FIRST
	`, totalColumn, fromClause, recentTVEventIDSQL("page.event_start"))
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

// The tuple belongs to the final event relation, after grouping and optional
// target deduplication. A series without episode arrivals has the empty event
// ID.
func recentTVCursorTerms() []queryCursorTerm {
	return []queryCursorTerm{
		{expression: defaultSortField, kind: cursorKindTimestamp, descending: true},
		{expression: "target_type", kind: cursorKindText, nullsLast: true},
		{expression: "target_id", kind: cursorKindText, nullsLast: true},
		{expression: recentTVEventIDSQL("event_start"), kind: cursorKindText, nullsLast: true},
	}
}

func recentTVCursor(target RecentTVTarget, consumed int) *QueryCursor {
	return &QueryCursor{Consumed: consumed, Keys: []QueryCursorValue{
		{Kind: "timestamp", Value: new(target.AddedAt.UTC().Format(time.RFC3339Nano))},
		{Kind: cursorKindText, Value: new(target.Type)},
		{Kind: cursorKindText, Value: new(target.ContentID)},
		{Kind: cursorKindText, Value: new(target.EventID)},
	}}
}
