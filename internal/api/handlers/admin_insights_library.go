package handlers

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The Library tab reads the catalog against the window's plays: how much of
// each library was played, what was never started, which genres, decades and
// ratings people watch, and how soon new titles get their first play.
//
// Titles are movies and series; a series counts as played when any of its
// episodes has a play. Coverage is per title, so a title in two libraries is
// played in both when either file played; the library filter picks which
// libraries are listed. The genre, decade and rating breakdowns count plays,
// which the library filter narrows by the play's library as everywhere else
// in Insights.
//
// "Never started" means no playback session at all since history began, so a
// four-second false start counts as started. It reads sessions, not plays,
// because answering it from plays would run the viewing grouping over all of
// history.
//
// A title's added date is when the library first saw it
// (media_item_libraries.first_seen_at); across libraries the earliest one.

const (
	insightsLibraryGenresLimit   = 20
	insightsLibraryRatingsLimit  = 15
	insightsLibraryUnplayedLimit = 20

	insightsFirstPlayDay    = "day"
	insightsFirstPlayWeek   = "week"
	insightsFirstPlayMonth  = "month"
	insightsFirstPlayLater  = "later"
	insightsFirstPlayNotYet = "not_yet"
)

type insightsTitlePlays struct {
	plays       int64
	firstPlayAt time.Time
}

type insightsAddedTitle struct {
	contentID string
	addedAt   time.Time
	libraryID int
}

func queryAdminInsightsLibrary(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsWindowQuery) (*AdminInsightsLibrary, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if err := validateInsightsScope(q); err != nil {
		return nil, err
	}
	ctx, tx, done, err := beginAdminHistoryRead(ctx, pool, insightsReadWorkMem)
	if err != nil {
		return nil, fmt.Errorf("beginning insights library read: %w", err)
	}
	defer done()

	oldestHist, _, _, _, dbNow, err := queryInsightsGlobalBounds(ctx, tx)
	if err != nil {
		return nil, err
	}
	win, err := resolveInsightsWindow(dbNow, q.Range, q.FromDate, q.ToDate, q.TZ)
	if err != nil {
		return nil, err
	}
	resolveAllTimeBounds(&win, oldestHist)

	out := &AdminInsightsLibrary{
		From: win.From, To: win.To, TZ: win.TZ, OldestHistoryAt: oldestHist,
		Libraries: make([]AdminInsightsLibraryCoverage, 0), Genres: make([]AdminInsightsLabeledPlays, 0),
		Decades: make([]AdminInsightsLabeledPlays, 0), ContentRatings: make([]AdminInsightsLabeledPlays, 0),
		TimeToFirstPlay: make([]AdminInsightsLabeledCount, 0), RecentlyAddedUnplayed: make([]AdminInsightsUnplayedTitle, 0),
	}

	titles, libraryPlays, err := queryInsightsLibraryTitlePlays(ctx, tx, win, q)
	if err != nil {
		return nil, err
	}
	added, err := queryInsightsLibraryAdded(ctx, tx, win, q.LibraryID)
	if err != nil {
		return nil, err
	}
	// "Played" counts plays inside the window, but "not played yet" is about
	// today: a window that ended in the past looks up, to now, the first play
	// of the titles it added that had none in it.
	if dbNow.After(win.To) {
		if err := addInsightsLibraryLaterFirstPlays(ctx, tx, win, dbNow, q, added, titles); err != nil {
			return nil, err
		}
	}
	if err := queryInsightsLibraryCoverage(ctx, tx, win, q, titles, out); err != nil {
		return nil, err
	}
	populateInsightsLibraryFirstPlays(added, titles, out)
	if err := queryInsightsLibraryUnplayed(ctx, tx, added, titles, out); err != nil {
		return nil, err
	}
	if err := queryInsightsLibraryBreakdowns(ctx, tx, libraryPlays, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights library read: %w", err)
	}
	return out, nil
}

// queryInsightsLibraryTitlePlays groups the window's plays by title. It
// returns every played title (any library) and, separately, the plays the
// library filter keeps.
func queryInsightsLibraryTitlePlays(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, q AdminInsightsWindowQuery) (map[string]insightsTitlePlays, map[string]int64, error) {
	scopeQuery := q
	scopeQuery.LibraryID = 0
	scope := newInsightsPlaysScope(win, scopeQuery)
	scope.needFileLibrary = q.LibraryID > 0
	var args []any
	with, counted := scope.viewingRowsSQL(&args)
	// Without a library filter every play counts, so skip attributing rows
	// to a library (and the media_files join it needs).
	libraryExpr := "0::int"
	if q.LibraryID > 0 {
		libraryExpr = insightsLibraryOfRowSQL("vr")
	}
	query := with + fmt.Sprintf(`,
		counted AS (
			SELECT vr.started_at, %[1]s AS is_play,
				COALESCE(vr.series_id, vr.media_item_id) AS title_id, %[2]s AS library_id
			FROM vr WHERE %[3]s
		)
		SELECT title_id, COALESCE(library_id, 0), COUNT(*)::bigint, MIN(started_at)
		FROM counted
		WHERE is_play AND title_id <> ''
		GROUP BY title_id, library_id
	`, insightsIsPlaySQL("vr"), libraryExpr, counted)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("querying insights library plays: %w", err)
	}
	defer rows.Close()
	titles := make(map[string]insightsTitlePlays)
	libraryPlays := make(map[string]int64)
	for rows.Next() {
		var id string
		var libraryID int
		var plays int64
		var first time.Time
		if err := rows.Scan(&id, &libraryID, &plays, &first); err != nil {
			return nil, nil, fmt.Errorf("scanning insights library plays: %w", err)
		}
		tp := titles[id]
		tp.plays += plays
		if tp.firstPlayAt.IsZero() || first.Before(tp.firstPlayAt) {
			tp.firstPlayAt = first
		}
		titles[id] = tp
		if q.LibraryID <= 0 || libraryID == q.LibraryID {
			libraryPlays[id] += plays
		}
	}
	return titles, libraryPlays, rows.Err()
}

// queryInsightsLibraryAdded reads the movies and series added in the window
// once per request, to reuse across later-first-play lookup, wait-time
// bucketing, and the recently-added unplayed list.
//
// When unfiltered, pre-filtering candidate content_ids on first_seen_at lets
// Postgres range-scan the first_seen_at index before resolving each candidate's
// earliest membership across all libraries (a title's MIN(first_seen_at) can
// only fall in [from, to) if at least one of its rows does).
func queryInsightsLibraryAdded(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, libraryID int) ([]insightsAddedTitle, error) {
	var rows pgx.Rows
	var err error
	if libraryID > 0 {
		// (content_id, media_folder_id) is the primary key of media_item_libraries,
		// so a single library holds at most one row per title.
		rows, err = tx.Query(ctx, `
			SELECT mil.content_id, mil.first_seen_at, mil.media_folder_id
			FROM media_item_libraries mil
			JOIN media_items mi ON mi.content_id = mil.content_id
			WHERE mil.media_folder_id = $3
			  AND mil.first_seen_at >= $1 AND mil.first_seen_at < $2
			  AND mi.type IN ('movie', 'series')
		`, win.From, win.To, libraryID)
	} else {
		rows, err = tx.Query(ctx, `
			WITH candidates AS (
				SELECT DISTINCT mil.content_id
				FROM media_item_libraries mil
				WHERE mil.first_seen_at >= $1 AND mil.first_seen_at < $2
			)
			SELECT mil.content_id, MIN(mil.first_seen_at) AS added_at,
				(ARRAY_AGG(mil.media_folder_id ORDER BY mil.first_seen_at, mil.media_folder_id))[1] AS library_id
			FROM candidates c
			JOIN media_items mi ON mi.content_id = c.content_id
			JOIN media_item_libraries mil ON mil.content_id = c.content_id
			WHERE mi.type IN ('movie', 'series')
			GROUP BY mil.content_id
			HAVING MIN(mil.first_seen_at) >= $1 AND MIN(mil.first_seen_at) < $2
		`, win.From, win.To)
	}
	if err != nil {
		return nil, fmt.Errorf("querying insights library added titles: %w", err)
	}
	defer rows.Close()
	added := make([]insightsAddedTitle, 0)
	for rows.Next() {
		var a insightsAddedTitle
		if err := rows.Scan(&a.contentID, &a.addedAt, &a.libraryID); err != nil {
			return nil, fmt.Errorf("scanning insights library added title: %w", err)
		}
		added = append(added, a)
	}
	return added, rows.Err()
}

// addInsightsLibraryLaterFirstPlays finds, for a window that ended before
// now, the first play up to now of each title it added that had no play in
// it, and adds those titles to titles with no window plays. Only those
// titles' sessions are grouped, so a narrow window long ago does not regroup
// all the history since; the window's own counts stay as the bounded pass
// read them. The grouping starts at the window's start, not its end, so a
// viewing that began in the window and qualified only once resumed after it
// still counts, from when it began.
func addInsightsLibraryLaterFirstPlays(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, now time.Time, q AdminInsightsWindowQuery, added []insightsAddedTitle, titles map[string]insightsTitlePlays) error {
	waiting := make([]string, 0)
	for _, a := range added {
		if _, ok := titles[a.contentID]; !ok {
			waiting = append(waiting, a.contentID)
		}
	}
	if len(waiting) == 0 {
		return nil
	}
	// The items whose sessions play waiting titles: movies themselves plus the
	// episodes of waiting series.
	rows, err := tx.Query(ctx, `SELECT ep.content_id FROM episodes ep WHERE ep.series_id = ANY($1::text[])`, waiting)
	if err != nil {
		return fmt.Errorf("querying insights library waiting episodes: %w", err)
	}
	epIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("scanning insights library waiting episodes: %w", err)
	}
	items := append(waiting, epIDs...)

	toNow := win
	toNow.To = now
	scopeQuery := q
	scopeQuery.LibraryID = 0
	scope := newInsightsPlaysScope(toNow, scopeQuery)
	scope.itemIDs = items
	var args []any
	with, counted := scope.viewingRowsSQL(&args)
	firstRows, err := tx.Query(ctx, with+fmt.Sprintf(`
		SELECT COALESCE(vr.series_id, vr.media_item_id), MIN(vr.started_at)
		FROM vr WHERE %[1]s AND %[2]s
		GROUP BY 1
	`, counted, insightsIsPlaySQL("vr")), args...)
	if err != nil {
		return fmt.Errorf("querying insights library later first plays: %w", err)
	}
	defer firstRows.Close()
	for firstRows.Next() {
		var id string
		var first time.Time
		if err := firstRows.Scan(&id, &first); err != nil {
			return fmt.Errorf("scanning insights library later first play: %w", err)
		}
		titles[id] = insightsTitlePlays{firstPlayAt: first}
	}
	return firstRows.Err()
}

func insightsLibraryCoverageSQL(win resolvedInsightsWindow, q AdminInsightsWindowQuery, playedIDs []string) (string, []any) {
	args := []any{playedIDs, win.From, win.To}
	param := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	startedConds := []string{"h.media_item_id <> ''"}
	if q.UserID > 0 {
		startedConds = append(startedConds, "h.user_id = "+param(q.UserID))
	}
	if profileID := strings.TrimSpace(q.ProfileID); profileID != "" {
		startedConds = append(startedConds, "h.profile_id = "+param(profileID))
	}

	ctes := `
		WITH played AS (
			SELECT id AS title_id FROM unnest($1::text[]) AS t(id)
		)`
	// A title was started when any session played it or one of its episodes.
	// Within one library, across every account, each title probes the
	// history's item index, which beats collecting the distinct items of the
	// whole history. Otherwise the started titles are collected, deduplicating
	// media_item_id before probing episodes: the whole catalog probes slower
	// than that, and one account's or profile's sessions are few.
	startedJoin := ""
	// The probes map sessions to titles as the collected set does: an
	// episode's sessions start its series (and only it), any other item's
	// sessions start the item itself. The LIMIT 1 lookup per episode keeps
	// the planner from hashing every session's episode instead.
	neverStarted := `NOT ((mi.content_id <> ''
			AND EXISTS (SELECT 1 FROM admin_playback_history h WHERE h.media_item_id = mi.content_id)
			AND NOT EXISTS (SELECT 1 FROM episodes e WHERE e.content_id = mi.content_id))
		OR EXISTS (SELECT 1 FROM episodes ep
			CROSS JOIN LATERAL (SELECT 1 FROM admin_playback_history h
				WHERE h.media_item_id = ep.content_id AND h.media_item_id <> '' LIMIT 1) played
			WHERE ep.series_id = mi.content_id))`
	if q.LibraryID <= 0 || len(startedConds) > 1 {
		ctes += `,
		started AS (
			SELECT DISTINCT COALESCE(ep.series_id, h.media_item_id) AS title_id
			FROM (
				SELECT DISTINCT h.media_item_id
				FROM admin_playback_history h
				WHERE ` + strings.Join(startedConds, " AND ") + `
			) h
			LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
		)`
		startedJoin = "LEFT JOIN started s ON s.title_id = mi.content_id"
		neverStarted = "s.title_id IS NULL"
	}

	if q.LibraryID > 0 {
		// Within one library, (content_id, media_folder_id) is unique, so per-
		// library counts are already distinct titles and equal the summary.
		libParam := param(q.LibraryID)
		return ctes + `
			SELECT 0, g.library_id, COALESCE(lib.name, ''), g.titles, g.played_titles, g.never_started, g.added_titles
			FROM (
				SELECT mil.media_folder_id AS library_id,
					COUNT(*)::bigint AS titles,
					COUNT(*) FILTER (WHERE p.title_id IS NOT NULL)::bigint AS played_titles,
					COUNT(*) FILTER (WHERE ` + neverStarted + `)::bigint AS never_started,
					COUNT(*) FILTER (WHERE mil.first_seen_at >= $2)::bigint AS added_titles
				FROM media_item_libraries mil
				JOIN media_items mi ON mi.content_id = mil.content_id
				LEFT JOIN played p ON p.title_id = mi.content_id
				` + startedJoin + `
				WHERE mi.type IN ('movie', 'series') AND mil.first_seen_at < $3
				  AND mil.media_folder_id = ` + libParam + `
				GROUP BY mil.media_folder_id
			) g
			LEFT JOIN media_folders lib ON lib.id = g.library_id`, args
	}

	// Per-library rows are unique by content_id so they hash-aggregate without
	// COUNT(DISTINCT) sorts; the cross-library summary deduplicates content_id
	// in a single hash-aggregate over the materialized slice.
	return ctes + `,
			lt AS MATERIALIZED (
				SELECT mil.media_folder_id AS library_id, mil.content_id,
					(mil.first_seen_at >= $2) AS is_added,
					(p.title_id IS NOT NULL) AS is_played,
					(` + neverStarted + `) AS is_never_started
				FROM media_item_libraries mil
				JOIN media_items mi ON mi.content_id = mil.content_id
				LEFT JOIN played p ON p.title_id = mi.content_id
				` + startedJoin + `
				WHERE mi.type IN ('movie', 'series') AND mil.first_seen_at < $3
			)
			SELECT 0, g.library_id, COALESCE(lib.name, ''), g.titles, g.played_titles, g.never_started, g.added_titles
			FROM (
				SELECT lt.library_id,
					COUNT(*)::bigint AS titles,
					COUNT(*) FILTER (WHERE lt.is_played)::bigint AS played_titles,
					COUNT(*) FILTER (WHERE lt.is_never_started)::bigint AS never_started,
					COUNT(*) FILTER (WHERE lt.is_added)::bigint AS added_titles
				FROM lt
				GROUP BY lt.library_id
			) g
			LEFT JOIN media_folders lib ON lib.id = g.library_id
			UNION ALL
			SELECT 1, 0, '',
				COUNT(*)::bigint,
				COUNT(*) FILTER (WHERE d.is_played)::bigint,
				COUNT(*) FILTER (WHERE d.is_never_started)::bigint,
				0::bigint
			FROM (
				SELECT DISTINCT content_id, is_played, is_never_started
				FROM lt
			) d`, args
}

// queryInsightsLibraryCoverage counts each library's movies and series, and
// how many were played in the window or never started (no session to now).
func queryInsightsLibraryCoverage(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, q AdminInsightsWindowQuery, titles map[string]insightsTitlePlays, out *AdminInsightsLibrary) error {
	playedIDs := make([]string, 0, len(titles))
	for id, tp := range titles {
		if tp.plays > 0 {
			playedIDs = append(playedIDs, id)
		}
	}
	query, args := insightsLibraryCoverageSQL(win, q, playedIDs)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("querying insights library coverage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var grouping int
		var row AdminInsightsLibraryCoverage
		if err := rows.Scan(&grouping, &row.LibraryID, &row.Name, &row.Titles, &row.PlayedTitles, &row.NeverStarted, &row.AddedTitles); err != nil {
			return fmt.Errorf("scanning insights library coverage: %w", err)
		}
		row.PlayedShare = roundInsightsRate(row.PlayedTitles, row.Titles)
		if grouping == 1 {
			out.Summary.Titles, out.Summary.PlayedTitles = row.Titles, row.PlayedTitles
			out.Summary.PlayedShare, out.Summary.NeverStarted = row.PlayedShare, row.NeverStarted
			continue
		}
		out.Libraries = append(out.Libraries, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if q.LibraryID > 0 && len(out.Libraries) == 1 {
		row := out.Libraries[0]
		out.Summary.Titles, out.Summary.PlayedTitles = row.Titles, row.PlayedTitles
		out.Summary.PlayedShare, out.Summary.NeverStarted = row.PlayedShare, row.NeverStarted
	}
	slices.SortFunc(out.Libraries, func(x, y AdminInsightsLibraryCoverage) int {
		if c := cmp.Compare(y.Titles, x.Titles); c != 0 {
			return c
		}
		return cmp.Compare(x.LibraryID, y.LibraryID)
	})
	return nil
}

// populateInsightsLibraryFirstPlays buckets the titles added in the window by
// how long each waited for its first play, and computes the median wait using
// the same linear interpolation as PostgreSQL's percentile_cont(0.5).
func populateInsightsLibraryFirstPlays(added []insightsAddedTitle, titles map[string]insightsTitlePlays, out *AdminInsightsLibrary) {
	waits := make(map[string]int64, 5)
	waitsPlayed := make([]float64, 0, len(added))
	for _, a := range added {
		tp, ok := titles[a.contentID]
		if !ok || tp.firstPlayAt.IsZero() {
			waits[insightsFirstPlayNotYet]++
			continue
		}
		waitSec := insightsWaitSeconds(a.addedAt, tp.firstPlayAt)
		waitsPlayed = append(waitsPlayed, waitSec)
		switch {
		case waitSec < 86400:
			waits[insightsFirstPlayDay]++
		case waitSec < 7*86400:
			waits[insightsFirstPlayWeek]++
		case waitSec < 30*86400:
			waits[insightsFirstPlayMonth]++
		default:
			waits[insightsFirstPlayLater]++
		}
	}
	out.Summary.AddedTitles = int64(len(added))
	out.Summary.AddedPlayed = out.Summary.AddedTitles - waits[insightsFirstPlayNotYet]
	if len(waitsPlayed) > 0 {
		slices.Sort(waitsPlayed)
		medianSec := insightsPercentileCont50(waitsPlayed)
		out.Summary.MedianDaysToFirstPlay = new(math.Round(medianSec/86400*10) / 10)
	}
	for _, key := range []string{insightsFirstPlayDay, insightsFirstPlayWeek, insightsFirstPlayMonth, insightsFirstPlayLater, insightsFirstPlayNotYet} {
		out.TimeToFirstPlay = append(out.TimeToFirstPlay, AdminInsightsLabeledCount{Key: key, Sessions: waits[key]})
	}
}

func insightsWaitSeconds(addedAt, firstPlayAt time.Time) float64 {
	if !firstPlayAt.After(addedAt) {
		return 0
	}
	// One division of the whole microsecond difference gives the float
	// PostgreSQL's EXTRACT(EPOCH ...) gave; adding seconds and fractions as
	// two floats can differ in the last bit and tip the shown median.
	return float64(firstPlayAt.Sub(addedAt).Microseconds()) / 1e6
}

// insightsPercentileCont50 matches PostgreSQL's percentile_cont(0.5) over a
// non-empty sorted slice, including its lo + (hi-lo)*0.5 rounding: (lo+hi)/2
// can differ in the last bit and tip the displayed median across a 0.1 day.
func insightsPercentileCont50(sorted []float64) float64 {
	n := len(sorted)
	mid := n / 2
	if n%2 == 1 {
		return sorted[mid]
	}
	lo, hi := sorted[mid-1], sorted[mid]
	return lo + (hi-lo)*0.5
}

// queryInsightsLibraryUnplayed lists the newest titles added in the window
// that still have no play.
func queryInsightsLibraryUnplayed(ctx context.Context, tx pgx.Tx, added []insightsAddedTitle, titles map[string]insightsTitlePlays, out *AdminInsightsLibrary) error {
	unplayed := make([]insightsAddedTitle, 0)
	for _, a := range added {
		if tp, ok := titles[a.contentID]; !ok || tp.firstPlayAt.IsZero() {
			unplayed = append(unplayed, a)
		}
	}
	if len(unplayed) == 0 {
		return nil
	}
	// Go compares strings byte by byte, the same order as content_id's
	// COLLATE "C", so the cut here and the ORDER BY below agree.
	slices.SortFunc(unplayed, func(x, y insightsAddedTitle) int {
		if c := y.addedAt.Compare(x.addedAt); c != 0 {
			return c
		}
		return cmp.Compare(x.contentID, y.contentID)
	})
	top := unplayed[:min(len(unplayed), insightsLibraryUnplayedLimit)]
	ids := make([]string, len(top))
	addedAts := make([]time.Time, len(top))
	libIDs := make([]int, len(top))
	for i, a := range top {
		ids[i] = a.contentID
		addedAts[i] = a.addedAt
		libIDs[i] = a.libraryID
	}
	rows, err := tx.Query(ctx, `
		SELECT u.content_id, mi.type, COALESCE(mi.title, ''), mi.year, u.added_at, u.library_id, COALESCE(lib.name, '')
		FROM unnest($1::text[], $2::timestamptz[], $3::int[]) AS u(content_id, added_at, library_id)
		JOIN media_items mi ON mi.content_id = u.content_id
		LEFT JOIN media_folders lib ON lib.id = u.library_id
		ORDER BY u.added_at DESC, u.content_id COLLATE "C"`, ids, addedAts, libIDs)
	if err != nil {
		return fmt.Errorf("querying insights library unplayed titles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t AdminInsightsUnplayedTitle
		if err := rows.Scan(&t.ContentID, &t.Kind, &t.Title, &t.Year, &t.AddedAt, &t.LibraryID, &t.LibraryName); err != nil {
			return fmt.Errorf("scanning insights library unplayed title: %w", err)
		}
		t.AddedAt = t.AddedAt.UTC()
		out.RecentlyAddedUnplayed = append(out.RecentlyAddedUnplayed, t)
	}
	return rows.Err()
}

// queryInsightsLibraryBreakdowns sums the window's plays by the played
// titles' genres, decades and content ratings. A title with three genres
// counts in each, so genre shares do not add up to the total. Only movies
// and series count, like coverage: history also holds plays of other item
// types, which the Library tab does not report on.
func queryInsightsLibraryBreakdowns(ctx context.Context, tx pgx.Tx, libraryPlays map[string]int64, out *AdminInsightsLibrary) error {
	if len(libraryPlays) == 0 {
		return nil
	}
	ids := make([]string, 0, len(libraryPlays))
	for id := range libraryPlays {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx, `
		SELECT content_id, COALESCE(genres, '{}'), year, COALESCE(BTRIM(content_rating), '')
		FROM media_items WHERE content_id = ANY($1::text[]) AND type IN ('movie', 'series')
	`, ids)
	if err != nil {
		return fmt.Errorf("querying insights library breakdowns: %w", err)
	}
	defer rows.Close()
	genres, decades, ratings := map[string]*AdminInsightsLabeledPlays{}, map[string]*AdminInsightsLabeledPlays{}, map[string]*AdminInsightsLabeledPlays{}
	add := func(m map[string]*AdminInsightsLabeledPlays, key string, plays int64) {
		row := m[key]
		if row == nil {
			row = &AdminInsightsLabeledPlays{Key: key}
			m[key] = row
		}
		row.Plays += plays
		row.Titles++
	}
	for rows.Next() {
		var id, rating string
		var titleGenres []string
		var year *int
		if err := rows.Scan(&id, &titleGenres, &year, &rating); err != nil {
			return fmt.Errorf("scanning insights library breakdowns: %w", err)
		}
		plays := libraryPlays[id]
		seen := make(map[string]bool, len(titleGenres))
		for _, g := range titleGenres {
			if g = strings.TrimSpace(g); g != "" && !seen[strings.ToLower(g)] {
				seen[strings.ToLower(g)] = true
				add(genres, g, plays)
			}
		}
		decade := insightsUnknownKey
		if year != nil && *year > 0 {
			decade = strconv.Itoa(*year / 10 * 10)
		}
		add(decades, decade, plays)
		if rating == "" {
			rating = insightsUnknownKey
		}
		add(ratings, rating, plays)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out.Genres = rankedLabeledPlays(genres, insightsLibraryGenresLimit)
	out.ContentRatings = rankedLabeledPlays(ratings, insightsLibraryRatingsLimit)
	out.Decades = make([]AdminInsightsLabeledPlays, 0, len(decades))
	for _, row := range decades {
		out.Decades = append(out.Decades, *row)
	}
	// Decades read oldest to newest, with unknown last.
	slices.SortFunc(out.Decades, func(x, y AdminInsightsLabeledPlays) int {
		if (x.Key == insightsUnknownKey) != (y.Key == insightsUnknownKey) {
			if x.Key == insightsUnknownKey {
				return 1
			}
			return -1
		}
		return cmp.Compare(x.Key, y.Key)
	})
	return nil
}

// rankedLabeledPlays orders keys by plays (unknown last) and keeps limit.
func rankedLabeledPlays(m map[string]*AdminInsightsLabeledPlays, limit int) []AdminInsightsLabeledPlays {
	out := make([]AdminInsightsLabeledPlays, 0, len(m))
	for _, row := range m {
		out = append(out, *row)
	}
	slices.SortFunc(out, func(x, y AdminInsightsLabeledPlays) int {
		if (x.Key == insightsUnknownKey) != (y.Key == insightsUnknownKey) {
			if x.Key == insightsUnknownKey {
				return 1
			}
			return -1
		}
		if c := cmp.Compare(y.Plays, x.Plays); c != 0 {
			return c
		}
		return cmp.Compare(x.Key, y.Key)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
