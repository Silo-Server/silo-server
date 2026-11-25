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
	// "Played" counts plays inside the window, but "not played yet" is about
	// today: a window that ended in the past looks up, to now, the first play
	// of the titles it added that had none in it.
	if dbNow.After(win.To) {
		if err := addInsightsLibraryLaterFirstPlays(ctx, tx, win, dbNow, q, titles); err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(titles))
	plays := make([]int64, 0, len(titles))
	firstPlays := make([]time.Time, 0, len(titles))
	for id, tp := range titles {
		ids = append(ids, id)
		plays = append(plays, tp.plays)
		firstPlays = append(firstPlays, tp.firstPlayAt)
	}
	catalog := insightsLibraryCatalogArgs{ids: ids, plays: plays, firstPlays: firstPlays, win: win, q: q}
	if err := queryInsightsLibraryCoverage(ctx, tx, catalog, out); err != nil {
		return nil, err
	}
	if err := queryInsightsLibraryFirstPlays(ctx, tx, catalog, out); err != nil {
		return nil, err
	}
	if err := queryInsightsLibraryUnplayed(ctx, tx, catalog, out); err != nil {
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
	var args []any
	with, counted := newInsightsPlaysScope(win, scopeQuery).viewingRowsSQL(&args)
	rows, err := tx.Query(ctx, with+fmt.Sprintf(`,
		counted AS (
			SELECT vr.started_at, %[1]s AS is_play,
				COALESCE(vr.series_id, vr.media_item_id) AS title_id, %[2]s AS library_id
			FROM vr WHERE %[3]s
		)
		SELECT title_id, COALESCE(library_id, 0), COUNT(*)::bigint, MIN(started_at)
		FROM counted
		WHERE is_play AND title_id <> ''
		GROUP BY title_id, library_id
	`, insightsIsPlaySQL("vr"), insightsLibraryOfRowSQL("vr"), counted), args...)
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

// addInsightsLibraryLaterFirstPlays finds, for a window that ended before
// now, the first play up to now of each title it added that had no play in
// it, and adds those titles to titles with no window plays. Only those
// titles' sessions are grouped, so a narrow window long ago does not regroup
// all the history since; the window's own counts stay as the bounded pass
// read them. The grouping starts at the window's start, not its end, so a
// viewing that began in the window and qualified only once resumed after it
// still counts, from when it began.
func addInsightsLibraryLaterFirstPlays(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, now time.Time, q AdminInsightsWindowQuery, titles map[string]insightsTitlePlays) error {
	played := make([]string, 0, len(titles))
	for id := range titles {
		played = append(played, id)
	}
	// The added titles (same rule as insightsLibraryAddedCTE) still waiting
	// at the window's end, and the items whose sessions play them: a movie
	// itself, a series' episodes.
	rows, err := tx.Query(ctx, `
		WITH added AS (
			SELECT mil.content_id
			FROM media_item_libraries mil
			JOIN media_items mi ON mi.content_id = mil.content_id
			WHERE mi.type IN ('movie', 'series') AND ($3::int = 0 OR mil.media_folder_id = $3::int)
			GROUP BY mil.content_id
			HAVING MIN(mil.first_seen_at) >= $1 AND MIN(mil.first_seen_at) < $2
		),
		waiting AS (SELECT content_id FROM added WHERE content_id <> ALL($4::text[]))
		SELECT content_id FROM waiting
		UNION ALL
		SELECT ep.content_id FROM episodes ep JOIN waiting w ON w.content_id = ep.series_id
	`, win.From, win.To, max(q.LibraryID, 0), played)
	if err != nil {
		return fmt.Errorf("querying insights library titles still waiting: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("scanning insights library titles still waiting: %w", err)
	}
	if len(items) == 0 {
		return nil
	}
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

// insightsLibraryCatalogArgs carries the played titles into the catalog
// statements as parallel arrays ($1 ids, $2 plays in the window, $3 first
// play, which for a title added in a past window may come after it), with the
// window ($4 from, $5 to) and scope ($6 library, $7 account, $8 profile).
type insightsLibraryCatalogArgs struct {
	ids        []string
	plays      []int64
	firstPlays []time.Time
	win        resolvedInsightsWindow
	q          AdminInsightsWindowQuery
}

// args returns the first n parameters: Postgres rejects a statement that
// leaves one of its parameters unused.
func (a insightsLibraryCatalogArgs) args(n int) []any {
	all := []any{a.ids, a.plays, a.firstPlays, a.win.From, a.win.To, max(a.q.LibraryID, 0), max(a.q.UserID, 0), strings.TrimSpace(a.q.ProfileID)}
	return all[:n]
}

const insightsLibraryPlayedCTE = `
	tp AS (
		SELECT * FROM unnest($1::text[], $2::bigint[], $3::timestamptz[]) AS t(title_id, plays, first_play_at)
	)`

// queryInsightsLibraryCoverage counts each library's movies and series, and
// how many were played in the window or never started (no session to now).
func queryInsightsLibraryCoverage(ctx context.Context, tx pgx.Tx, a insightsLibraryCatalogArgs, out *AdminInsightsLibrary) error {
	rows, err := tx.Query(ctx, `
		WITH `+insightsLibraryPlayedCTE+`,
		started AS (
			SELECT DISTINCT COALESCE(ep.series_id, h.media_item_id) AS title_id
			FROM admin_playback_history h
			LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
			WHERE h.media_item_id <> ''
			  AND ($7::int = 0 OR h.user_id = $7::int)
			  AND ($8::text = '' OR h.profile_id = $8::text)
		),
		lt AS (
			SELECT mil.media_folder_id AS library_id, mil.content_id, mil.first_seen_at
			FROM media_item_libraries mil
			JOIN media_items mi ON mi.content_id = mil.content_id
			WHERE mi.type IN ('movie', 'series') AND mil.first_seen_at < $5
			  AND ($6::int = 0 OR mil.media_folder_id = $6::int)
		)
		SELECT GROUPING(lt.library_id)::int, COALESCE(lt.library_id, 0), COALESCE(MIN(lib.name), ''),
			COUNT(DISTINCT lt.content_id)::bigint,
			COUNT(DISTINCT lt.content_id) FILTER (WHERE tp.plays > 0)::bigint,
			COUNT(DISTINCT lt.content_id) FILTER (WHERE s.title_id IS NULL)::bigint,
			COUNT(DISTINCT lt.content_id) FILTER (WHERE lt.first_seen_at >= $4)::bigint
		FROM lt
		LEFT JOIN tp ON tp.title_id = lt.content_id
		LEFT JOIN started s ON s.title_id = lt.content_id
		LEFT JOIN media_folders lib ON lib.id = lt.library_id
		GROUP BY GROUPING SETS ((lt.library_id), ())
	`, a.args(8)...)
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
	slices.SortFunc(out.Libraries, func(x, y AdminInsightsLibraryCoverage) int {
		if c := cmp.Compare(y.Titles, x.Titles); c != 0 {
			return c
		}
		return cmp.Compare(x.LibraryID, y.LibraryID)
	})
	return nil
}

// insightsLibraryAddedCTE extends insightsLibraryPlayedCTE with the titles
// added in the window ($4, $5) in scope ($6) and how long each waited for its
// first play: NULL while it has none.
const insightsLibraryAddedCTE = insightsLibraryPlayedCTE + `,
	added AS (
		SELECT mil.content_id, MIN(mil.first_seen_at) AS added_at,
			(ARRAY_AGG(mil.media_folder_id ORDER BY mil.first_seen_at, mil.media_folder_id))[1] AS library_id
		FROM media_item_libraries mil
		JOIN media_items mi ON mi.content_id = mil.content_id
		WHERE mi.type IN ('movie', 'series') AND ($6::int = 0 OR mil.media_folder_id = $6::int)
		GROUP BY mil.content_id
		HAVING MIN(mil.first_seen_at) >= $4 AND MIN(mil.first_seen_at) < $5
	),
	waited AS (
		-- GREATEST skips NULLs, so an unplayed title keeps its NULL wait
		-- only through the CASE.
		SELECT a.*, CASE WHEN tp.first_play_at IS NOT NULL
			THEN GREATEST(EXTRACT(EPOCH FROM tp.first_play_at - a.added_at), 0) END AS wait_seconds
		FROM added a LEFT JOIN tp ON tp.title_id = a.content_id
	)`

// queryInsightsLibraryFirstPlays buckets the titles added in the window by how
// long each waited for its first play, and reads the median wait.
func queryInsightsLibraryFirstPlays(ctx context.Context, tx pgx.Tx, a insightsLibraryCatalogArgs, out *AdminInsightsLibrary) error {
	rows, err := tx.Query(ctx, `
		WITH `+insightsLibraryAddedCTE+`
		SELECT GROUPING(bucket)::int, COALESCE(bucket, ''), COUNT(*)::bigint,
			percentile_cont(0.5) WITHIN GROUP (ORDER BY wait_seconds) FILTER (WHERE wait_seconds IS NOT NULL)
		FROM (
			SELECT wait_seconds, CASE
				WHEN wait_seconds IS NULL THEN '`+insightsFirstPlayNotYet+`'
				WHEN wait_seconds < 86400 THEN '`+insightsFirstPlayDay+`'
				WHEN wait_seconds < 7 * 86400 THEN '`+insightsFirstPlayWeek+`'
				WHEN wait_seconds < 30 * 86400 THEN '`+insightsFirstPlayMonth+`'
				ELSE '`+insightsFirstPlayLater+`' END AS bucket
			FROM waited
		) w
		GROUP BY GROUPING SETS ((bucket), ())
	`, a.args(6)...)
	if err != nil {
		return fmt.Errorf("querying insights library first plays: %w", err)
	}
	defer rows.Close()
	waits := make(map[string]int64)
	for rows.Next() {
		var grouping int
		var bucket string
		var count int64
		var medianSeconds *float64
		if err := rows.Scan(&grouping, &bucket, &count, &medianSeconds); err != nil {
			return fmt.Errorf("scanning insights library first plays: %w", err)
		}
		if grouping == 0 {
			waits[bucket] = count
			continue
		}
		out.Summary.AddedTitles = count
		if medianSeconds != nil {
			out.Summary.MedianDaysToFirstPlay = new(math.Round(*medianSeconds/86400*10) / 10)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out.Summary.AddedPlayed = out.Summary.AddedTitles - waits[insightsFirstPlayNotYet]
	for _, key := range []string{insightsFirstPlayDay, insightsFirstPlayWeek, insightsFirstPlayMonth, insightsFirstPlayLater, insightsFirstPlayNotYet} {
		out.TimeToFirstPlay = append(out.TimeToFirstPlay, AdminInsightsLabeledCount{Key: key, Sessions: waits[key]})
	}
	return nil
}

// queryInsightsLibraryUnplayed lists the newest titles added in the window
// that still have no play.
func queryInsightsLibraryUnplayed(ctx context.Context, tx pgx.Tx, a insightsLibraryCatalogArgs, out *AdminInsightsLibrary) error {
	rows, err := tx.Query(ctx, `
		WITH `+insightsLibraryAddedCTE+`
		SELECT w.content_id, mi.type, COALESCE(mi.title, ''), mi.year, w.added_at, w.library_id, COALESCE(lib.name, '')
		FROM waited w
		JOIN media_items mi ON mi.content_id = w.content_id
		LEFT JOIN media_folders lib ON lib.id = w.library_id
		WHERE w.wait_seconds IS NULL
		ORDER BY w.added_at DESC, w.content_id
		LIMIT `+strconv.Itoa(insightsLibraryUnplayedLimit), a.args(6)...)
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
