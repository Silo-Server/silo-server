package handlers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	insightsTitleViewersCap  = 100
	insightsTitleEpisodesCap = 500
	// insightsTitlesKindLibrary marks library rows in the leaderboard query;
	// title rows carry itemTypeMovie or itemTypeSeries.
	insightsTitlesKindLibrary = "library"
	// insightsTitlesListLeastFinished marks the least-finished ranking's rows.
	insightsTitlesListLeastFinished = "least_finished"
	// insightsLeastFinishedMinPlays keeps a title with one or two abandoned
	// plays off the least-finished list, where its rate says little.
	insightsLeastFinishedMinPlays = 3
)

// ---- Titles leaderboard ----------------------------------------------------

// queryAdminInsightsTitles ranks the window's movies, series and libraries,
// and lists the least-finished titles. Episodes roll up to their series. Only titles and libraries with at least
// one play rank; viewers are distinct profiles with a play. A library row
// takes plays by the library of each play's first session and watch time by
// each session's library (insightsLibraryOfRowSQL).
func queryAdminInsightsTitles(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsTitlesQuery) (*AdminInsightsTitles, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if err := validateInsightsScope(q.AdminInsightsWindowQuery); err != nil {
		return nil, err
	}
	ctx, tx, done, err := beginAdminHistoryRead(ctx, pool, insightsReadWorkMem)
	if err != nil {
		return nil, fmt.Errorf("beginning insights titles read: %w", err)
	}
	defer done()

	win, err := resolveInsightsPlaysWindow(ctx, tx, q.AdminInsightsWindowQuery)
	if err != nil {
		return nil, err
	}
	sortKey := normalizeInsightsTitlesSort(q.Sort)
	out := &AdminInsightsTitles{
		From: win.From, To: win.To, TZ: win.TZ, Sort: sortKey,
		Movies: make([]AdminInsightsTitleRow, 0), Shows: make([]AdminInsightsTitleRow, 0),
		Libraries: make([]AdminInsightsLibraryRow, 0), LeastFinished: make([]AdminInsightsTitleRow, 0),
	}

	if err := queryInsightsTitlesRanking(ctx, tx, newInsightsPlaysScope(win, q.AdminInsightsWindowQuery), sortKey, clampInsightsTitlesLimit(q.Limit), out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights titles read: %w", err)
	}
	return out, nil
}

// resolveInsightsPlaysWindow resolves the request's window on the database
// clock; all time starts at the oldest history row.
func resolveInsightsPlaysWindow(ctx context.Context, tx pgx.Tx, q AdminInsightsWindowQuery) (resolvedInsightsWindow, error) {
	oldestHist, _, _, _, dbNow, err := queryInsightsGlobalBounds(ctx, tx)
	if err != nil {
		return resolvedInsightsWindow{}, err
	}
	win, err := resolveInsightsWindow(dbNow, q.Range, q.FromDate, q.ToDate, q.TZ)
	if err != nil {
		return resolvedInsightsWindow{}, err
	}
	resolveAllTimeBounds(&win, oldestHist)
	return win, nil
}

// queryInsightsTitlesRanking ranks titles and libraries in one statement, so
// the viewing grouping runs once per read.
func queryInsightsTitlesRanking(ctx context.Context, tx pgx.Tx, scope insightsPlaysScope, sortKey string, limit int, out *AdminInsightsTitles) error {
	var args []any
	with, counted := scope.viewingRowsSQL(&args)
	args = append(args, limit, insightsLeastFinishedMinPlays)
	limitParam, minPlaysParam := len(args)-1, len(args)
	rows, err := tx.Query(ctx, with+fmt.Sprintf(`,
		counted AS (
			SELECT vr.*, %[1]s AS is_play,
				CASE WHEN vr.is_episode THEN 'series' WHEN vr.item_type = 'movie' THEN 'movie' END AS kind,
				COALESCE(vr.series_id, vr.media_item_id) AS title_id,
				%[2]s AS library_id
			FROM vr WHERE %[3]s
		),
		totals AS (
			SELECT kind, title_id AS id,
				COUNT(*) FILTER (WHERE is_play)::bigint AS plays,
				COUNT(DISTINCT (user_id, profile_id)) FILTER (WHERE is_play)::bigint AS viewers,
				COUNT(*) FILTER (WHERE is_play AND viewing_completed)::bigint AS completed_plays,
				COUNT(DISTINCT media_item_id) FILTER (WHERE is_play AND is_episode)::bigint AS episodes,
				COALESCE(SUM(watch_sec), 0)::double precision AS watch_seconds,
				MAX(ended_at) AS last_played_at
			FROM counted WHERE kind IS NOT NULL
			GROUP BY kind, title_id
			UNION ALL
			SELECT 'library', library_id::text,
				COUNT(*) FILTER (WHERE is_play)::bigint,
				COUNT(DISTINCT (user_id, profile_id)) FILTER (WHERE is_play)::bigint,
				COUNT(*) FILTER (WHERE is_play AND viewing_completed)::bigint,
				0::bigint,
				COALESCE(SUM(watch_sec), 0)::double precision,
				MAX(ended_at)
			FROM counted WHERE library_id IS NOT NULL
			GROUP BY library_id
		),
		ranked AS (
			SELECT t.kind AS list, t.*, ROW_NUMBER() OVER (PARTITION BY t.kind ORDER BY %[4]s, t.id) AS rn
			FROM totals t WHERE t.plays > 0
			UNION ALL
			SELECT '%[6]s', t.*, ROW_NUMBER() OVER (
				ORDER BY t.completed_plays::double precision / t.plays, t.plays DESC, t.watch_seconds DESC, t.id) AS rn
			FROM totals t WHERE t.kind <> 'library' AND t.plays >= $%[7]d AND t.completed_plays < t.plays
		)
		SELECT r.list, r.kind, r.id, COALESCE(mi.title, lib.name, ''), mi.year,
			r.plays, r.viewers, r.completed_plays, r.episodes, r.watch_seconds, r.last_played_at
		FROM ranked r
		LEFT JOIN media_items mi ON r.kind <> 'library' AND mi.content_id = r.id
		LEFT JOIN media_folders lib ON r.kind = 'library' AND lib.id::text = r.id
		WHERE r.rn <= $%[5]d
		ORDER BY r.list, r.rn
	`, insightsIsPlaySQL("vr"), insightsLibraryOfRowSQL("vr"), counted, insightsTitlesOrderSQL(sortKey, "t"), limitParam,
		insightsTitlesListLeastFinished, minPlaysParam), args...)
	if err != nil {
		return fmt.Errorf("querying insights titles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var list, kind, id, title string
		var year *int
		var plays, viewers, completed, episodes int64
		var watchSec float64
		var lastPlayed *time.Time
		if err := rows.Scan(&list, &kind, &id, &title, &year, &plays, &viewers, &completed, &episodes, &watchSec, &lastPlayed); err != nil {
			return fmt.Errorf("scanning insights titles: %w", err)
		}
		watch := int64(math.Round(watchSec))
		if kind == insightsTitlesKindLibrary {
			libID, convErr := strconv.Atoi(id)
			if convErr != nil {
				continue
			}
			out.Libraries = append(out.Libraries, AdminInsightsLibraryRow{LibraryID: libID, Name: title, Plays: plays, Viewers: viewers, WatchSeconds: watch})
			continue
		}
		row := AdminInsightsTitleRow{
			ContentID: id, Kind: kind, Title: title, Year: year, Plays: plays, Viewers: viewers,
			CompletedPlays: completed, Episodes: episodes, WatchSeconds: watch, LastPlayedAt: normalizeTimePtrUTC(lastPlayed),
		}
		switch {
		case list == insightsTitlesListLeastFinished:
			out.LeastFinished = append(out.LeastFinished, row)
		case kind == itemTypeSeries:
			out.Shows = append(out.Shows, row)
		default:
			out.Movies = append(out.Movies, row)
		}
	}
	return rows.Err()
}

// insightsTitlesOrderSQL is the ranking order for a sort key over a totals
// alias; ties fall through the other metrics.
func insightsTitlesOrderSQL(sortKey, alias string) string {
	plays, watch, viewers := alias+".plays DESC", alias+".watch_seconds DESC", alias+".viewers DESC"
	switch sortKey {
	case InsightsTitlesSortWatchTime:
		return strings.Join([]string{watch, plays, viewers}, ", ")
	case InsightsTitlesSortViewers:
		return strings.Join([]string{viewers, plays, watch}, ", ")
	default:
		return strings.Join([]string{plays, watch, viewers}, ", ")
	}
}

// ---- Title detail ----------------------------------------------------------

type insightsTitleRef struct {
	kind            string
	title           string
	year            *int
	durationSeconds *float64
	itemIDs         []string
}

// resolveInsightsTitle reads a movie or series and the history item ids it
// covers: the movie itself, or every episode of the series. An episode id is
// a validation error (open its series); anything else unknown is not found.
func resolveInsightsTitle(ctx context.Context, tx pgx.Tx, contentID string) (insightsTitleRef, error) {
	var ref insightsTitleRef
	var itemType string
	var runtimeMinutes *int
	err := tx.QueryRow(ctx, `SELECT COALESCE(type, ''), COALESCE(title, ''), year, runtime FROM media_items WHERE content_id = $1`, contentID).
		Scan(&itemType, &ref.title, &ref.year, &runtimeMinutes)
	if errors.Is(err, pgx.ErrNoRows) {
		var isEpisode bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM episodes WHERE content_id = $1)`, contentID).Scan(&isEpisode); err != nil {
			return ref, fmt.Errorf("resolving insights title: %w", err)
		}
		if isEpisode {
			return ref, fmt.Errorf("%w: content_id is an episode; read its series", ErrInvalidInsightsQuery)
		}
		return ref, ErrInsightsTitleNotFound
	}
	if err != nil {
		return ref, fmt.Errorf("resolving insights title: %w", err)
	}
	switch itemType {
	case itemTypeMovie:
		ref.kind = itemTypeMovie
		ref.itemIDs = []string{contentID}
		var fileSeconds *float64
		if err := tx.QueryRow(ctx, `SELECT MAX(duration)::double precision FROM media_files WHERE content_id = $1 AND duration > 0`, contentID).Scan(&fileSeconds); err != nil {
			return ref, fmt.Errorf("reading insights title duration: %w", err)
		}
		switch {
		case fileSeconds != nil:
			ref.durationSeconds = fileSeconds
		case runtimeMinutes != nil && *runtimeMinutes > 0:
			ref.durationSeconds = new(float64(*runtimeMinutes * 60))
		}
	case itemTypeSeries:
		ref.kind = itemTypeSeries
		rows, err := tx.Query(ctx, `SELECT content_id FROM episodes WHERE series_id = $1`, contentID)
		if err != nil {
			return ref, fmt.Errorf("reading insights title episodes: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return ref, fmt.Errorf("reading insights title episodes: %w", err)
		}
		// Never nil: a series without episodes must scope to no items, and a
		// nil itemIDs reads every item.
		ref.itemIDs = append([]string{}, ids...)
	default:
		return ref, fmt.Errorf("%w: content_id is not a movie or series", ErrInvalidInsightsQuery)
	}
	return ref, nil
}

// queryAdminInsightsTitle reads one movie or series: totals, plays over time,
// the profiles that played it, and for a series its episodes. The scope is
// limited to the title's item ids, so the read uses the history item index.
func queryAdminInsightsTitle(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsTitleQuery) (*AdminInsightsTitle, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if err := validateInsightsScope(q.AdminInsightsWindowQuery); err != nil {
		return nil, err
	}
	contentID := strings.TrimSpace(q.ContentID)
	if contentID == "" {
		return nil, fmt.Errorf("%w: content_id is required", ErrInvalidInsightsQuery)
	}
	ctx, tx, done, err := beginAdminHistoryRead(ctx, pool, insightsReadWorkMem)
	if err != nil {
		return nil, fmt.Errorf("beginning insights title read: %w", err)
	}
	defer done()

	ref, err := resolveInsightsTitle(ctx, tx, contentID)
	if err != nil {
		return nil, err
	}
	win, err := resolveInsightsPlaysWindow(ctx, tx, q.AdminInsightsWindowQuery)
	if err != nil {
		return nil, err
	}
	out := &AdminInsightsTitle{
		From: win.From, To: win.To, TZ: win.TZ, BucketUnit: win.BucketUnit,
		ContentID: contentID, Kind: ref.kind, Title: ref.title, Year: ref.year, DurationSeconds: ref.durationSeconds,
		Buckets: make([]AdminInsightsPlaysBucket, 0), Viewers: make([]AdminInsightsTitleViewer, 0),
		Episodes: make([]AdminInsightsTitleEpisode, 0),
	}
	scope := newInsightsPlaysScope(win, q.AdminInsightsWindowQuery)
	scope.itemIDs = ref.itemIDs
	if err := queryInsightsTitleTotals(ctx, tx, win, scope, out); err != nil {
		return nil, err
	}
	if err := queryInsightsTitleViewers(ctx, tx, scope, out); err != nil {
		return nil, err
	}
	if ref.kind == itemTypeSeries {
		if err := queryInsightsTitleEpisodes(ctx, tx, scope, out); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights title read: %w", err)
	}
	return out, nil
}

func queryInsightsTitleTotals(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, scope insightsPlaysScope, out *AdminInsightsTitle) error {
	var args []any
	with, counted := scope.viewingRowsSQL(&args)
	args = append(args, win.BucketUnit, win.TZ)
	unitParam, tzParam := len(args)-1, len(args)
	rows, err := tx.Query(ctx, with+fmt.Sprintf(`,
		counted AS (
			SELECT vr.*, %[1]s AS is_play, date_trunc($%[2]d, vr.started_at AT TIME ZONE $%[3]d) AS local_bucket
			FROM vr WHERE %[4]s
		)
		SELECT
			GROUPING(local_bucket)::int,
			(local_bucket AT TIME ZONE $%[3]d), COALESCE(to_char(local_bucket, 'YYYY-MM-DD'), ''),
			COUNT(*) FILTER (WHERE is_play)::bigint,
			COUNT(*)::bigint,
			COUNT(DISTINCT (user_id, profile_id)) FILTER (WHERE is_play)::bigint,
			COUNT(*) FILTER (WHERE is_play AND viewing_completed)::bigint,
			COALESCE(SUM(watch_sec), 0)::double precision,
			COUNT(*) FILTER (WHERE is_play AND %[5]s)::bigint,
			COUNT(*) FILTER (WHERE is_play AND %[6]s)::bigint,
			COUNT(*) FILTER (WHERE is_play AND %[7]s)::bigint
		FROM counted
		GROUP BY GROUPING SETS ((), (local_bucket))
		ORDER BY 1 DESC, local_bucket
	`, insightsIsPlaySQL("vr"), unitParam, tzParam, counted,
		insightsDirectSQL("viewing_play_method"), insightsRemuxSQL("viewing_play_method"), insightsTranscodeSQL("viewing_play_method")), args...)
	if err != nil {
		return fmt.Errorf("querying insights title totals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var grouping int
		var bucketStart *time.Time
		var label string
		var plays, sessions, viewers, completed, direct, remux, transcode int64
		var watchSec float64
		if err := rows.Scan(&grouping, &bucketStart, &label, &plays, &sessions, &viewers, &completed, &watchSec, &direct, &remux, &transcode); err != nil {
			return fmt.Errorf("scanning insights title totals: %w", err)
		}
		watch := int64(math.Round(watchSec))
		if grouping == 0 && bucketStart != nil {
			out.Buckets = append(out.Buckets, AdminInsightsPlaysBucket{BucketStart: bucketStart.UTC(), LabelDate: label, Plays: plays, WatchSeconds: watch})
			continue
		}
		out.Summary = AdminInsightsTitleSummary{
			Plays: plays, Sessions: sessions, Viewers: viewers, CompletedPlays: completed, WatchSeconds: watch,
			DirectPlays: direct, RemuxPlays: remux, TranscodePlays: transcode,
		}
	}
	return rows.Err()
}

// queryInsightsTitleViewers lists the profiles with a play of the title, most
// recent first. last_played_at is the latest session end in the window; for a
// movie the furthest end position, for a series the episode of the latest play.
func queryInsightsTitleViewers(ctx context.Context, tx pgx.Tx, scope insightsPlaysScope, out *AdminInsightsTitle) error {
	var args []any
	with, counted := scope.viewingRowsSQL(&args)
	args = append(args, insightsTitleViewersCap+1)
	rows, err := tx.Query(ctx, with+fmt.Sprintf(`,
		counted AS (
			SELECT vr.*, %[1]s AS is_play FROM vr WHERE %[2]s
		),
		grouped AS (
			SELECT user_id, profile_id,
				COUNT(*) FILTER (WHERE is_play)::bigint AS plays,
				COUNT(*) FILTER (WHERE is_play AND viewing_completed)::bigint AS completed_plays,
				COALESCE(SUM(watch_sec), 0)::double precision AS watch_seconds,
				MAX(ended_at) AS last_played_at,
				MAX(watched_seconds) AS furthest_position,
				(ARRAY_AGG(media_item_id ORDER BY started_at DESC, session_id DESC) FILTER (WHERE is_play))[1] AS last_item_id
			FROM counted
			GROUP BY user_id, profile_id
			HAVING COUNT(*) FILTER (WHERE is_play) > 0
		)
		SELECT g.user_id, COALESCE(u.username, ''), g.profile_id, COALESCE(pn.profile_name, g.profile_id),
			g.plays, g.completed_plays, g.watch_seconds, g.last_played_at, g.furthest_position,
			COALESCE(g.last_item_id, ''), COALESCE(ep.season_number, 0), COALESCE(ep.episode_number, 0), COALESCE(ep.title, ''),
			(ep.content_id IS NOT NULL)
		FROM grouped g
		LEFT JOIN users u ON u.id = g.user_id
		LEFT JOIN episodes ep ON ep.content_id = g.last_item_id
		LEFT JOIN LATERAL (%[3]s) pn ON TRUE
		ORDER BY g.last_played_at DESC, g.user_id, g.profile_id
		LIMIT $%[4]d
	`, insightsIsPlaySQL("vr"), counted, insightsProfileNameSQL("g"), len(args)), args...)
	if err != nil {
		return fmt.Errorf("querying insights title viewers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v AdminInsightsTitleViewer
		var watchSec float64
		var furthest *float64
		var ep AdminInsightsEpisodeRef
		var isEpisode bool
		if err := rows.Scan(&v.UserID, &v.Username, &v.ProfileID, &v.ProfileName, &v.Plays, &v.CompletedPlays, &watchSec,
			&v.LastPlayedAt, &furthest, &ep.ContentID, &ep.SeasonNumber, &ep.EpisodeNumber, &ep.Title, &isEpisode); err != nil {
			return fmt.Errorf("scanning insights title viewer: %w", err)
		}
		if len(out.Viewers) == insightsTitleViewersCap {
			out.ViewersTruncated = true
			break
		}
		if v.Username == "" {
			v.Username = fmt.Sprintf("User %d", v.UserID)
		}
		v.WatchSeconds = int64(math.Round(watchSec))
		v.LastPlayedAt = normalizeTimePtrUTC(v.LastPlayedAt)
		if out.Kind == itemTypeMovie {
			v.FurthestPositionSeconds = furthest
		} else if isEpisode {
			v.LastEpisode = &ep
		}
		out.Viewers = append(out.Viewers, v)
	}
	return rows.Err()
}

// queryInsightsTitleEpisodes lists a series' episodes with a play in the
// window, in season and episode order.
func queryInsightsTitleEpisodes(ctx context.Context, tx pgx.Tx, scope insightsPlaysScope, out *AdminInsightsTitle) error {
	var args []any
	with, counted := scope.viewingRowsSQL(&args)
	args = append(args, insightsTitleEpisodesCap+1)
	rows, err := tx.Query(ctx, with+fmt.Sprintf(`,
		counted AS (
			SELECT vr.*, %[1]s AS is_play FROM vr WHERE %[2]s
		),
		grouped AS (
			SELECT media_item_id,
				COUNT(*) FILTER (WHERE is_play)::bigint AS plays,
				COUNT(DISTINCT (user_id, profile_id)) FILTER (WHERE is_play)::bigint AS viewers,
				COALESCE(SUM(watch_sec), 0)::double precision AS watch_seconds
			FROM counted
			GROUP BY media_item_id
			HAVING COUNT(*) FILTER (WHERE is_play) > 0
		)
		SELECT g.media_item_id, ep.season_number, ep.episode_number, COALESCE(ep.title, ''),
			g.plays, g.viewers, g.watch_seconds
		FROM grouped g
		JOIN episodes ep ON ep.content_id = g.media_item_id
		ORDER BY ep.season_number, ep.episode_number, g.media_item_id
		LIMIT $%[3]d
	`, insightsIsPlaySQL("vr"), counted, len(args)), args...)
	if err != nil {
		return fmt.Errorf("querying insights title episodes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e AdminInsightsTitleEpisode
		var watchSec float64
		if err := rows.Scan(&e.ContentID, &e.SeasonNumber, &e.EpisodeNumber, &e.Title, &e.Plays, &e.Viewers, &watchSec); err != nil {
			return fmt.Errorf("scanning insights title episode: %w", err)
		}
		if len(out.Episodes) == insightsTitleEpisodesCap {
			out.EpisodesTruncated = true
			break
		}
		e.WatchSeconds = int64(math.Round(watchSec))
		out.Episodes = append(out.Episodes, e)
	}
	return rows.Err()
}
