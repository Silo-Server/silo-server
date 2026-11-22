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

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	insightsPlayDirectSQL    = `h.play_method IN ('direct', 'direct_play')`
	insightsPlayRemuxSQL     = `h.play_method IN ('remux', 'direct_stream')`
	insightsPlayTranscodeSQL = `h.play_method = 'transcode'`

	insightsSortWatchTime     = "watch_time"
	insightsSortPlays         = "plays"
	insightsSortDirectPct     = "direct_pct"
	insightsSortCompletionPct = "completion_pct"
	insightsSortLastSeen      = "last_seen"
	insightsHWSoftwareKey     = "software"
)

func roundInsightsRate(num, den int64) float64 {
	if den <= 0 {
		return 0
	}
	return math.Round(float64(num)/float64(den)*10000) / 10000
}

func buildInsightsHistoryWhere(win resolvedInsightsWindow, libraryID, userID int) (whereSQL string, args []any) {
	var conds []string
	if !win.AllTime {
		args = append(args, win.From)
		conds = append(conds, fmt.Sprintf("h.started_at >= $%d", len(args)))
	}
	args = append(args, win.To)
	conds = append(conds, fmt.Sprintf("h.started_at < $%d", len(args)))
	if userID > 0 {
		args = append(args, userID)
		conds = append(conds, fmt.Sprintf("h.user_id = $%d", len(args)))
	}
	if libraryID > 0 {
		args = append(args, libraryID)
		p := len(args)
		conds = append(conds, fmt.Sprintf(`(mf.media_folder_id = $%d OR (mf.id IS NULL AND EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = COALESCE(ep.series_id, h.media_item_id) AND mil.media_folder_id = $%d)))`, p, p))
	}
	return strings.Join(conds, " AND "), args
}

func insightsLibraryJoinSQL(libraryID int) string {
	if libraryID <= 0 {
		return ""
	}
	return `
		LEFT JOIN media_files mf ON mf.id = h.media_file_id
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id`
}

func queryInsightsGlobalBounds(ctx context.Context, tx pgx.Tx) (oldestHistory, oldestMeasured, oldestRouteFacts, oldestMetric *time.Time, dbNow time.Time, err error) {
	var oldestDailyDay, oldestRawSample *time.Time
	err = tx.QueryRow(ctx, `
		SELECT
			(SELECT MIN(started_at) FROM admin_playback_history),
			(SELECT MIN(started_at) FROM admin_playback_history WHERE measured),
			(SELECT MIN(started_at) FROM admin_playback_history WHERE route_facts_recorded),
			(SELECT MIN(day)::timestamptz FROM dashboard_metric_daily),
			(SELECT MIN(bucket) FROM dashboard_metric_samples),
			now()
	`).Scan(&oldestHistory, &oldestMeasured, &oldestRouteFacts, &oldestDailyDay, &oldestRawSample, &dbNow)
	if err != nil {
		return nil, nil, nil, nil, time.Time{}, fmt.Errorf("querying insights coverage bounds: %w", err)
	}
	oldestMetric = earlierTimePtr(oldestDailyDay, oldestRawSample)
	return normalizeTimePtrUTC(oldestHistory), normalizeTimePtrUTC(oldestMeasured), normalizeTimePtrUTC(oldestRouteFacts), normalizeTimePtrUTC(oldestMetric), dbNow.UTC(), nil
}

func earlierTimePtr(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.Before(*a) {
		return b
	}
	return a
}

func normalizeTimePtrUTC(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func resolveAllTimeBounds(win *resolvedInsightsWindow, earliest *time.Time) {
	if !win.AllTime {
		return
	}
	if earliest != nil && earliest.Before(win.To) {
		localStart := earliest.In(win.Loc)
		win.From = time.Date(localStart.Year(), localStart.Month(), localStart.Day(), 0, 0, 0, 0, win.Loc).UTC()
		win.BucketUnit = pickInsightsBucketUnit(win.To.Sub(win.From))
		return
	}
	nowLocal := win.To.In(win.Loc)
	win.From = time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, win.Loc).AddDate(0, 0, -29).UTC()
	win.BucketUnit = InsightsBucketDay
}

const insightsCombinedDailyMetricsCTE = `
	WITH daily_archive AS (
		SELECT
			day,
			peak_streams::bigint AS peak_streams,
			peak_direct::bigint AS peak_direct,
			peak_remux::bigint AS peak_remux,
			peak_transcode::bigint AS peak_transcode,
			peak_streams_direct::bigint AS peak_streams_direct,
			peak_streams_remux::bigint AS peak_streams_remux,
			peak_streams_transcode::bigint AS peak_streams_transcode,
			peak_egress_kbps,
			peak_download_kbps,
			egress_bytes,
			download_egress_bytes,
			api_egress_bytes,
			api_peak_egress_kbps,
			peak_streams_at,
			peak_egress_at
		FROM dashboard_metric_daily
		WHERE day >= $1::date AND day <= $2::date
	),
	raw_minutes AS (
		SELECT
			(s.bucket AT TIME ZONE 'UTC')::date AS day,
			s.bucket,
			MAX(s.streams_total)     FILTER (WHERE s.source = 'shared') AS streams_total,
			MAX(s.streams_direct)    FILTER (WHERE s.source = 'shared') AS streams_direct,
			MAX(s.streams_remux)     FILTER (WHERE s.source = 'shared') AS streams_remux,
			MAX(s.streams_transcode) FILTER (WHERE s.source = 'shared') AS streams_transcode,
			COALESCE(SUM(s.egress_kbps), 0)::bigint AS egress_kbps,
			COALESCE(SUM(s.download_egress_kbps), 0)::bigint AS download_egress_kbps,
			COALESCE(SUM(s.egress_kbps) FILTER (WHERE s.source LIKE 'proc:%'), 0)::bigint AS api_egress_kbps
		FROM dashboard_metric_samples s
		WHERE (s.bucket AT TIME ZONE 'UTC')::date >= $1::date
		  AND (s.bucket AT TIME ZONE 'UTC')::date <= $2::date
		  AND NOT EXISTS (
			SELECT 1 FROM dashboard_metric_daily d WHERE d.day = (s.bucket AT TIME ZONE 'UTC')::date
		  )
		GROUP BY 1, 2
	),
	raw_stream_peaks AS (
		SELECT DISTINCT ON (day)
			day,
			COALESCE(streams_direct, 0)::bigint    AS peak_streams_direct,
			COALESCE(streams_remux, 0)::bigint     AS peak_streams_remux,
			COALESCE(streams_transcode, 0)::bigint AS peak_streams_transcode,
			bucket AS peak_streams_at
		FROM raw_minutes
		WHERE streams_total IS NOT NULL
		ORDER BY day, streams_total DESC, bucket ASC
	),
	raw_egress_peaks AS (
		SELECT DISTINCT ON (day)
			day,
			bucket AS peak_egress_at
		FROM raw_minutes
		ORDER BY day, egress_kbps DESC, bucket ASC
	),
	live_daily AS (
		SELECT
			m.day,
			COALESCE(MAX(m.streams_total), 0)::bigint AS peak_streams,
			COALESCE(MAX(m.streams_direct), 0)::bigint AS peak_direct,
			COALESCE(MAX(m.streams_remux), 0)::bigint AS peak_remux,
			COALESCE(MAX(m.streams_transcode), 0)::bigint AS peak_transcode,
			COALESCE(sp.peak_streams_direct, 0)::bigint AS peak_streams_direct,
			COALESCE(sp.peak_streams_remux, 0)::bigint AS peak_streams_remux,
			COALESCE(sp.peak_streams_transcode, 0)::bigint AS peak_streams_transcode,
			COALESCE(MAX(m.egress_kbps), 0)::bigint AS peak_egress_kbps,
			COALESCE(MAX(m.download_egress_kbps), 0)::bigint AS peak_download_kbps,
			(COALESCE(SUM(m.egress_kbps), 0) * $3)::bigint AS egress_bytes,
			(COALESCE(SUM(m.download_egress_kbps), 0) * $3)::bigint AS download_egress_bytes,
			(COALESCE(SUM(m.api_egress_kbps), 0) * $3)::bigint AS api_egress_bytes,
			COALESCE(MAX(m.api_egress_kbps), 0)::bigint AS api_peak_egress_kbps,
			sp.peak_streams_at,
			ep.peak_egress_at
		FROM raw_minutes m
		LEFT JOIN raw_stream_peaks sp ON sp.day = m.day
		LEFT JOIN raw_egress_peaks ep ON ep.day = m.day
		GROUP BY m.day, sp.peak_streams_direct, sp.peak_streams_remux, sp.peak_streams_transcode, sp.peak_streams_at, ep.peak_egress_at
	),
	combined_daily AS (
		SELECT * FROM daily_archive
		UNION ALL
		SELECT * FROM live_daily
	)`

func utcDaySpanForWindow(win resolvedInsightsWindow) (fromDay, toDay string) {
	fromDay = win.From.UTC().Format("2006-01-02")
	inclusiveTo := win.To.Add(-time.Nanosecond).UTC()
	if inclusiveTo.Before(win.From.UTC()) {
		inclusiveTo = win.From.UTC()
	}
	return fromDay, inclusiveTo.Format("2006-01-02")
}

// ---- Activity --------------------------------------------------------------

func queryAdminInsightsActivity(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsWindowQuery) (*AdminInsightsActivity, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning insights activity read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	oldestHist, oldestMeas, oldestRoute, oldestMetric, dbNow, err := queryInsightsGlobalBounds(ctx, tx)
	if err != nil {
		return nil, err
	}
	win, err := resolveInsightsWindow(dbNow, q.Range, q.FromDate, q.ToDate, q.TZ)
	if err != nil {
		return nil, err
	}
	resolveAllTimeBounds(&win, earlierTimePtr(oldestHist, oldestMetric))
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, q.LibraryID, q.UserID)

	out := &AdminInsightsActivity{
		From: win.From, To: win.To, TZ: win.TZ, BucketUnit: win.BucketUnit,
		Coverage: AdminInsightsCoverage{
			OldestHistoryAt: oldestHist, OldestMeasuredAt: oldestMeas,
			OldestRouteFactsAt: oldestRoute, OldestMetricAt: oldestMetric,
		},
		Buckets: make([]AdminInsightsActivityBucket, 0), Heatmap: make([]AdminInsightsHeatmapCell, 0),
		PeakStreams: make([]AdminInsightsPeakStreamsPoint, 0),
	}
	if err := queryInsightsActivitySummary(ctx, tx, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsActivityBuckets(ctx, tx, win, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsActivityHeatmap(ctx, tx, win, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsActivityPeakStreams(ctx, tx, win, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights activity read: %w", err)
	}
	return out, nil
}

func queryInsightsActivitySummary(ctx context.Context, tx pgx.Tx, whereSQL string, whereArgs []any, out *AdminInsightsActivity) error {
	watchSQL := adminPlaybackWatchTimeSQL("h")
	var watchSecF, measuredWatchSecF float64
	err := tx.QueryRow(ctx, `
		SELECT
			COUNT(*)::bigint,
			COUNT(*) FILTER (WHERE ep.content_id IS NULL AND mi.type = 'movie')::bigint,
			COUNT(*) FILTER (WHERE ep.content_id IS NOT NULL)::bigint,
			COUNT(*) FILTER (WHERE ep.content_id IS NULL AND COALESCE(mi.type, '') <> 'movie')::bigint,
			COALESCE(SUM(`+watchSQL+`), 0)::double precision,
			COALESCE(SUM(CASE WHEN h.measured THEN h.played_seconds ELSE 0 END), 0)::double precision,
			COUNT(*) FILTER (WHERE h.measured)::bigint,
			COUNT(*) FILTER (WHERE h.route_facts_recorded)::bigint,
			COUNT(*) FILTER (WHERE h.completed)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayDirectSQL+`)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayRemuxSQL+`)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+`)::bigint,
			COUNT(DISTINCT h.user_id)::bigint,
			COUNT(DISTINCT (h.user_id::text || ':' || h.profile_id))::bigint
		FROM admin_playback_history h
		LEFT JOIN media_items mi ON mi.content_id = h.media_item_id
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
		LEFT JOIN media_files mf ON mf.id = h.media_file_id
		WHERE `+whereSQL, whereArgs...).Scan(
		&out.Summary.Plays, &out.Summary.Movies, &out.Summary.Episodes, &out.Summary.OtherMedia,
		&watchSecF, &measuredWatchSecF, &out.Summary.MeasuredPlays, &out.Coverage.RouteFactsPlays,
		&out.Summary.CompletedPlays, &out.Summary.DirectPlays, &out.Summary.RemuxPlays, &out.Summary.TranscodePlays,
		&out.Summary.UniqueAccounts, &out.Summary.UniqueProfiles,
	)
	if err != nil {
		return fmt.Errorf("querying insights activity summary: %w", err)
	}
	out.Summary.WatchSeconds = int64(math.Round(watchSecF))
	out.Summary.MeasuredWatchSeconds = int64(math.Round(measuredWatchSecF))
	out.Summary.CompletionRate = roundInsightsRate(out.Summary.CompletedPlays, out.Summary.Plays)
	out.Coverage.TotalPlays = out.Summary.Plays
	out.Coverage.MeasuredPlays = out.Summary.MeasuredPlays
	return nil
}

func queryInsightsActivityBuckets(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, whereSQL string, whereArgs []any, out *AdminInsightsActivity) error {
	watchSQL := adminPlaybackWatchTimeSQL("h")
	args := append(append([]any(nil), whereArgs...), win.BucketUnit, win.TZ)
	unitParam, tzParam := len(args)-1, len(args)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		WITH local_rows AS (
			SELECT
				date_trunc($%d, h.started_at AT TIME ZONE $%d) AS local_bucket,
				h.play_method,
				(%s) AS watch_sec,
				(ep.content_id IS NOT NULL) AS is_episode,
				(ep.content_id IS NULL AND mi.type = 'movie') AS is_movie
			FROM admin_playback_history h
			LEFT JOIN media_items mi ON mi.content_id = h.media_item_id
			LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
			LEFT JOIN media_files mf ON mf.id = h.media_file_id
			WHERE %s
		)
		SELECT
			(local_bucket AT TIME ZONE $%d) AS bucket_start,
			to_char(local_bucket, 'YYYY-MM-DD') AS label_date,
			COUNT(*)::bigint AS plays,
			COALESCE(SUM(watch_sec), 0)::double precision AS watch_seconds,
			COUNT(*) FILTER (WHERE play_method IN ('direct', 'direct_play'))::bigint AS direct,
			COUNT(*) FILTER (WHERE play_method IN ('remux', 'direct_stream'))::bigint AS remux,
			COUNT(*) FILTER (WHERE play_method = 'transcode')::bigint AS transcode,
			COUNT(*) FILTER (WHERE is_movie)::bigint AS movies,
			COUNT(*) FILTER (WHERE is_episode)::bigint AS episodes,
			COUNT(*) FILTER (WHERE NOT is_movie AND NOT is_episode)::bigint AS other_media
		FROM local_rows
		GROUP BY local_bucket
		ORDER BY local_bucket ASC
	`, unitParam, tzParam, watchSQL, whereSQL, tzParam), args...)
	if err != nil {
		return fmt.Errorf("querying insights activity buckets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var b AdminInsightsActivityBucket
		var ws float64
		if err := rows.Scan(&b.BucketStart, &b.LabelDate, &b.Plays, &ws, &b.Direct, &b.Remux, &b.Transcode, &b.Movies, &b.Episodes, &b.OtherMedia); err != nil {
			return fmt.Errorf("scanning insights activity bucket: %w", err)
		}
		b.BucketStart = b.BucketStart.UTC()
		b.WatchSeconds = int64(math.Round(ws))
		out.Buckets = append(out.Buckets, b)
	}
	return rows.Err()
}

func queryInsightsActivityHeatmap(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsActivity) error {
	watchSQL := adminPlaybackWatchTimeSQL("h")
	args := append(append([]any(nil), whereArgs...), win.TZ)
	tzParam := len(args)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT
			(EXTRACT(ISODOW FROM (h.started_at AT TIME ZONE $%d))::int - 1) AS dow,
			EXTRACT(HOUR FROM (h.started_at AT TIME ZONE $%d))::int AS hour,
			COUNT(*)::bigint AS plays,
			COALESCE(SUM(%s), 0)::double precision AS watch_seconds
		FROM admin_playback_history h
		%s
		WHERE %s
		GROUP BY 1, 2
		ORDER BY 1, 2
	`, tzParam, tzParam, watchSQL, insightsLibraryJoinSQL(libraryID), whereSQL), args...)
	if err != nil {
		return fmt.Errorf("querying insights heatmap: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cell AdminInsightsHeatmapCell
		var ws float64
		if err := rows.Scan(&cell.DayOfWeek, &cell.Hour, &cell.Plays, &ws); err != nil {
			return fmt.Errorf("scanning insights heatmap cell: %w", err)
		}
		cell.WatchSeconds = int64(math.Round(ws))
		out.Heatmap = append(out.Heatmap, cell)
	}
	return rows.Err()
}

func queryInsightsActivityPeakStreams(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, out *AdminInsightsActivity) error {
	fromDay, toDay := utcDaySpanForWindow(win)
	rows, err := tx.Query(ctx, insightsCombinedDailyMetricsCTE+`
		SELECT
			to_char(day, 'YYYY-MM-DD') AS day_str,
			peak_streams, peak_direct, peak_remux, peak_transcode,
			peak_streams_direct, peak_streams_remux, peak_streams_transcode,
			peak_streams_at
		FROM combined_daily
		ORDER BY day ASC
	`, fromDay, toDay, int64(bytesPerKbpsMinute))
	if err != nil {
		return fmt.Errorf("querying insights peak streams: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pt AdminInsightsPeakStreamsPoint
		if err := rows.Scan(
			&pt.Date, &pt.PeakStreams, &pt.PeakDirect, &pt.PeakRemux, &pt.PeakTranscode,
			&pt.PeakStreamsDirect, &pt.PeakStreamsRemux, &pt.PeakStreamsTranscode, &pt.PeakStreamsAt,
		); err != nil {
			return fmt.Errorf("scanning insights peak streams: %w", err)
		}
		pt.PeakStreamsAt = normalizeTimePtrUTC(pt.PeakStreamsAt)
		if pt.PeakStreams > out.Summary.PeakConcurrentStreams ||
			(pt.PeakStreams == out.Summary.PeakConcurrentStreams && pt.PeakStreams > 0 && out.Summary.PeakConcurrentAt == nil) {
			out.Summary.PeakConcurrentStreams = pt.PeakStreams
			out.Summary.PeakConcurrentAt = pt.PeakStreamsAt
		}
		out.PeakStreams = append(out.PeakStreams, pt)
	}
	return rows.Err()
}

// ---- People ----------------------------------------------------------------

func queryAdminInsightsPeople(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning insights people read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	oldestHist, oldestMeas, oldestRoute, oldestMetric, dbNow, err := queryInsightsGlobalBounds(ctx, tx)
	if err != nil {
		return nil, err
	}
	win, err := resolveInsightsWindow(dbNow, q.Range, q.FromDate, q.ToDate, q.TZ)
	if err != nil {
		return nil, err
	}
	resolveAllTimeBounds(&win, oldestHist)
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, q.LibraryID, 0)

	out := &AdminInsightsPeople{
		From: win.From, To: win.To, TZ: win.TZ,
		Coverage: AdminInsightsCoverage{
			OldestHistoryAt: oldestHist, OldestMeasuredAt: oldestMeas,
			OldestRouteFactsAt: oldestRoute, OldestMetricAt: oldestMetric,
		},
		Accounts: make([]AdminInsightsPersonAccount, 0),
	}

	liveUsers, liveProfiles, err := queryInsightsLiveUsers(ctx, tx)
	if err != nil {
		return nil, err
	}
	byUser, err := queryInsightsPeopleProfiles(ctx, tx, whereSQL, whereArgs, liveUsers, liveProfiles, out)
	if err != nil {
		return nil, err
	}
	if err := queryInsightsPeopleTopClients(ctx, tx, q.LibraryID, whereSQL, whereArgs, byUser); err != nil {
		return nil, err
	}

	allAccounts := finalizeInsightsPeopleAccounts(byUser, q.Search, out)
	sortInsightsAccounts(allAccounts, q.Sort, q.Dir)
	limit, offset := clampInsightsPeopleLimit(q.Limit), max(q.Offset, 0)
	if offset < len(allAccounts) {
		out.Accounts = allAccounts[offset:min(offset+limit, len(allAccounts))]
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights people read: %w", err)
	}
	return out, nil
}

func queryInsightsLiveUsers(ctx context.Context, tx pgx.Tx) (map[int]bool, map[string]bool, error) {
	liveUsers := make(map[int]bool)
	liveProfiles := make(map[string]bool)
	rows, err := tx.Query(ctx, `SELECT DISTINCT user_id, COALESCE(profile_id, '') FROM playback_sessions_sync`)
	if err != nil {
		return nil, nil, fmt.Errorf("querying live playback accounts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid int
		var pid string
		if err := rows.Scan(&uid, &pid); err != nil {
			return nil, nil, err
		}
		liveUsers[uid] = true
		liveProfiles[strconv.Itoa(uid)+":"+pid] = true
	}
	return liveUsers, liveProfiles, rows.Err()
}

func insightsPeopleProfilesSQL(whereSQL string) string {
	watchSQL := adminPlaybackWatchTimeSQL("h")
	return `
		WITH grouped AS (
			SELECT
				h.user_id, h.profile_id,
				COUNT(*)::bigint AS plays,
				COUNT(*) FILTER (WHERE ep.content_id IS NULL AND mi.type = 'movie')::bigint AS movies,
				COUNT(*) FILTER (WHERE ep.content_id IS NOT NULL)::bigint AS episodes,
				COALESCE(SUM(` + watchSQL + `), 0)::double precision AS watch_seconds,
				COUNT(*) FILTER (WHERE h.measured)::bigint AS measured_plays,
				COUNT(*) FILTER (WHERE h.route_facts_recorded)::bigint AS route_facts_plays,
				COUNT(*) FILTER (WHERE ` + insightsPlayDirectSQL + `)::bigint AS direct_plays,
				COUNT(*) FILTER (WHERE ` + insightsPlayRemuxSQL + `)::bigint AS remux_plays,
				COUNT(*) FILTER (WHERE ` + insightsPlayTranscodeSQL + `)::bigint AS transcode_plays,
				COUNT(*) FILTER (WHERE h.completed)::bigint AS completed_plays,
				MAX(h.ended_at) AS last_seen_at
			FROM admin_playback_history h
			LEFT JOIN media_items mi ON mi.content_id = h.media_item_id
			LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
			LEFT JOIN media_files mf ON mf.id = h.media_file_id
			WHERE ` + whereSQL + `
			GROUP BY h.user_id, h.profile_id
		)
		SELECT
			g.user_id, COALESCE(u.username, ''), COALESCE(u.role, ''),
			g.profile_id, COALESCE(pn.profile_name, g.profile_id),
			g.plays, g.movies, g.episodes, g.watch_seconds,
			g.measured_plays, g.route_facts_plays,
			g.direct_plays, g.remux_plays, g.transcode_plays, g.completed_plays,
			g.last_seen_at
		FROM grouped g
		LEFT JOIN users u ON u.id = g.user_id
		LEFT JOIN LATERAL (
			SELECT p.profile_name
			FROM admin_playback_history p
			WHERE p.user_id = g.user_id AND p.profile_id = g.profile_id AND p.profile_name <> ''
			ORDER BY p.ended_at DESC
			LIMIT 1
		) pn ON TRUE
	`
}

func queryInsightsPeopleProfiles(
	ctx context.Context,
	tx pgx.Tx,
	whereSQL string,
	whereArgs []any,
	liveUsers map[int]bool,
	liveProfiles map[string]bool,
	out *AdminInsightsPeople,
) (map[int]*AdminInsightsPersonAccount, error) {
	rows, err := tx.Query(ctx, insightsPeopleProfilesSQL(whereSQL), whereArgs...)
	if err != nil {
		return nil, fmt.Errorf("querying insights people: %w", err)
	}
	defer rows.Close()

	byUser := make(map[int]*AdminInsightsPersonAccount)
	for rows.Next() {
		var uid int
		var username, role, profileID, profileName string
		var plays, movies, episodes, measuredPlays, routePlays, direct, remux, transcode, completed int64
		var watchSecF float64
		var lastSeen *time.Time
		if err := rows.Scan(
			&uid, &username, &role, &profileID, &profileName,
			&plays, &movies, &episodes, &watchSecF, &measuredPlays, &routePlays,
			&direct, &remux, &transcode, &completed, &lastSeen,
		); err != nil {
			return nil, fmt.Errorf("scanning insights person profile: %w", err)
		}
		lastSeen = normalizeTimePtrUTC(lastSeen)
		watchSec := int64(math.Round(watchSecF))

		out.Coverage.TotalPlays += plays
		out.Coverage.MeasuredPlays += measuredPlays
		out.Coverage.RouteFactsPlays += routePlays
		out.ActiveProfiles++

		acc, ok := byUser[uid]
		if !ok {
			if username == "" {
				username = fmt.Sprintf("User %d", uid)
			}
			acc = &AdminInsightsPersonAccount{
				UserID: uid, Username: username, Role: role, IsLive: liveUsers[uid],
				Profiles: make([]AdminInsightsPersonProfile, 0, 2),
			}
			byUser[uid] = acc
		}
		acc.Plays += plays
		acc.Movies += movies
		acc.Episodes += episodes
		acc.WatchSeconds += watchSec
		acc.MeasuredPlays += measuredPlays
		acc.DirectPlays += direct
		acc.RemuxPlays += remux
		acc.TranscodePlays += transcode
		acc.CompletedPlays += completed
		if lastSeen != nil && (acc.LastSeenAt == nil || lastSeen.After(*acc.LastSeenAt)) {
			acc.LastSeenAt = lastSeen
		}
		acc.Profiles = append(acc.Profiles, AdminInsightsPersonProfile{
			ProfileID: profileID, ProfileName: profileName, IsLive: liveProfiles[strconv.Itoa(uid)+":"+profileID],
			Plays: plays, WatchSeconds: watchSec, MeasuredPlays: measuredPlays,
			DirectPlays: direct, RemuxPlays: remux, TranscodePlays: transcode,
			CompletedPlays: completed, CompletionRate: roundInsightsRate(completed, plays), LastSeenAt: lastSeen,
		})
	}
	return byUser, rows.Err()
}

func queryInsightsPeopleTopClients(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, byUser map[int]*AdminInsightsPersonAccount) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (h.user_id)
			h.user_id, BTRIM(h.client_name) AS client_name
		FROM admin_playback_history h
		`+insightsLibraryJoinSQL(libraryID)+`
		WHERE `+whereSQL+`
		  AND NULLIF(BTRIM(COALESCE(h.client_name, '')), '') IS NOT NULL
		GROUP BY h.user_id, BTRIM(h.client_name)
		ORDER BY h.user_id, COUNT(*) DESC, BTRIM(h.client_name) ASC
	`, whereArgs...)
	if err != nil {
		return fmt.Errorf("querying insights top clients per account: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid int
		var clientName string
		if err := rows.Scan(&uid, &clientName); err != nil {
			return err
		}
		if acc, ok := byUser[uid]; ok && clientName != "" {
			c := clientName
			acc.TopClient = &c
		}
	}
	return rows.Err()
}

func accountMatchesSearch(acc *AdminInsightsPersonAccount, searchLower string) bool {
	if searchLower == "" {
		return true
	}
	if strings.Contains(strings.ToLower(acc.Username), searchLower) {
		return true
	}
	for _, p := range acc.Profiles {
		if strings.Contains(strings.ToLower(p.ProfileName), searchLower) {
			return true
		}
	}
	return false
}

func finalizeInsightsPeopleAccounts(byUser map[int]*AdminInsightsPersonAccount, search string, out *AdminInsightsPeople) []AdminInsightsPersonAccount {
	searchLower := strings.ToLower(strings.TrimSpace(search))
	allAccounts := make([]AdminInsightsPersonAccount, 0, len(byUser))
	for _, acc := range byUser {
		acc.MeasuredShare = roundInsightsRate(acc.MeasuredPlays, acc.Plays)
		acc.DirectRate = roundInsightsRate(acc.DirectPlays, acc.Plays)
		acc.CompletionRate = roundInsightsRate(acc.CompletedPlays, acc.Plays)
		slices.SortFunc(acc.Profiles, func(a, b AdminInsightsPersonProfile) int {
			if c := cmp.Compare(b.WatchSeconds, a.WatchSeconds); c != 0 {
				return c
			}
			if c := cmp.Compare(b.Plays, a.Plays); c != 0 {
				return c
			}
			return cmp.Compare(a.ProfileName, b.ProfileName)
		})
		out.ActiveAccounts++
		if acc.IsLive {
			out.LiveAccounts++
		}
		if accountMatchesSearch(acc, searchLower) {
			allAccounts = append(allAccounts, *acc)
		}
	}
	out.TotalAccounts = int64(len(allAccounts))
	return allAccounts
}

func sortInsightsAccounts(accounts []AdminInsightsPersonAccount, sortRaw, dirRaw string) {
	sortField := strings.ToLower(strings.TrimSpace(sortRaw))
	if sortField == "" {
		sortField = insightsSortWatchTime
	}
	asc := strings.EqualFold(strings.TrimSpace(dirRaw), "asc")
	slices.SortFunc(accounts, func(a, b AdminInsightsPersonAccount) int {
		var c int
		switch sortField {
		case insightsSortPlays:
			c = cmp.Compare(a.Plays, b.Plays)
		case insightsSortDirectPct:
			c = cmp.Compare(a.DirectRate, b.DirectRate)
		case insightsSortCompletionPct:
			c = cmp.Compare(a.CompletionRate, b.CompletionRate)
		case insightsSortLastSeen:
			c = compareTimePtr(a.LastSeenAt, b.LastSeenAt)
		default:
			c = cmp.Compare(a.WatchSeconds, b.WatchSeconds)
		}
		if c != 0 {
			if asc {
				return c
			}
			return -c
		}
		if c := cmp.Compare(b.Plays, a.Plays); c != 0 {
			return c
		}
		return cmp.Compare(a.UserID, b.UserID)
	})
}

func compareTimePtr(a, b *time.Time) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	return a.Compare(*b)
}

// ---- Devices & Quality -----------------------------------------------------

func normalizeHWAccelKey(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "":
		return insightsUnknownKey
	case AdminTerminateDeliveryNone, insightsHWSoftwareKey, "cpu":
		return insightsHWSoftwareKey
	default:
		return v
	}
}

func queryAdminInsightsDevices(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsDevicesQuery) (*AdminInsightsDevices, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning insights devices read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	oldestHist, oldestMeas, oldestRoute, oldestMetric, dbNow, err := queryInsightsGlobalBounds(ctx, tx)
	if err != nil {
		return nil, err
	}
	win, err := resolveInsightsWindow(dbNow, q.Range, q.FromDate, q.ToDate, q.TZ)
	if err != nil {
		return nil, err
	}
	resolveAllTimeBounds(&win, oldestHist)
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, q.LibraryID, q.UserID)

	out := &AdminInsightsDevices{
		From: win.From, To: win.To, TZ: win.TZ,
		Coverage: AdminInsightsCoverage{
			OldestHistoryAt: oldestHist, OldestMeasuredAt: oldestMeas,
			OldestRouteFactsAt: oldestRoute, OldestMetricAt: oldestMetric,
		},
		Codecs: make([]string, 0), Matrix: make([]AdminInsightsClientMatrixRow, 0),
		AdaptationReasons: make([]AdminInsightsAdaptationReason, 0),
		HWAccelBreakdown:  make([]AdminInsightsLabeledCount, 0), ToneMapBreakdown: make([]AdminInsightsLabeledCount, 0),
		SubtitleBurnInCodecs: make([]AdminInsightsLabeledCount, 0), TopTranscodingAccount: make([]AdminInsightsTopTranscodingAccount, 0),
	}

	if err := queryInsightsDevicesSummary(ctx, tx, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsClientCodecMatrix(ctx, tx, whereSQL, whereArgs, clampInsightsMinPlays(q.MinPlays), out); err != nil {
		return nil, err
	}
	if err := queryInsightsAdaptationReasons(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsDevicesBreakdowns(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsTopTranscodingAccounts(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights devices read: %w", err)
	}
	return out, nil
}

func queryInsightsDevicesSummary(ctx context.Context, tx pgx.Tx, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	err := tx.QueryRow(ctx, `
		SELECT
			COUNT(*)::bigint,
			COUNT(*) FILTER (WHERE h.measured)::bigint,
			COUNT(*) FILTER (WHERE h.route_facts_recorded)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayDirectSQL+`)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayRemuxSQL+`)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+`)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+` AND h.route_facts_recorded AND NULLIF(LOWER(BTRIM(COALESCE(h.transcode_hw_accel, ''))), '') IS NOT NULL AND LOWER(BTRIM(h.transcode_hw_accel)) NOT IN ('none', 'software', 'cpu'))::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+` AND h.route_facts_recorded AND LOWER(BTRIM(COALESCE(h.transcode_hw_accel, ''))) IN ('none', 'software', 'cpu'))::bigint,
			COUNT(*) FILTER (WHERE h.subtitle->>'delivery' = 'burn_in')::bigint,
			COUNT(*) FILTER (WHERE NULLIF(LOWER(BTRIM(COALESCE(h.tone_map_mode, ''))), '') IS NOT NULL)::bigint,
			COUNT(*) FILTER (WHERE LOWER(BTRIM(COALESCE(h.tone_map_mode, ''))) = 'hardware')::bigint,
			COUNT(*) FILTER (WHERE LOWER(BTRIM(COALESCE(h.tone_map_mode, ''))) = 'software')::bigint
		FROM admin_playback_history h
		LEFT JOIN media_files mf ON mf.id = h.media_file_id
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
		WHERE `+whereSQL, whereArgs...).Scan(
		&out.Summary.TotalPlays, &out.Coverage.MeasuredPlays, &out.Summary.RouteFactsPlays,
		&out.Summary.DirectPlays, &out.Summary.RemuxPlays, &out.Summary.TranscodePlays,
		&out.Summary.HWTranscodePlays, &out.Summary.SWTranscodePlays,
		&out.Summary.SubtitleBurnInPlays, &out.Summary.HDRToneMapPlays,
		&out.Summary.HDRToneMapHWPlays, &out.Summary.HDRToneMapSWPlays,
	)
	if err != nil {
		return fmt.Errorf("querying insights devices summary: %w", err)
	}
	out.Summary.DirectRate = roundInsightsRate(out.Summary.DirectPlays, out.Summary.TotalPlays)
	out.Coverage.TotalPlays = out.Summary.TotalPlays
	out.Coverage.RouteFactsPlays = out.Summary.RouteFactsPlays
	return nil
}

func queryInsightsClientCodecMatrix(ctx context.Context, tx pgx.Tx, whereSQL string, whereArgs []any, minPlays int, out *AdminInsightsDevices) error {
	rows, err := tx.Query(ctx, `
		SELECT
			COALESCE(NULLIF(BTRIM(h.client_name), ''), 'Unknown') AS client_name,
			COALESCE(NULLIF(BTRIM(h.device_platform), ''), 'Unknown') AS device_platform,
			LOWER(COALESCE(NULLIF(BTRIM(mf.codec_video), ''), 'unknown')) AS source_codec,
			BOOL_OR(COALESCE(h.compat_origin, FALSE)) AS compat_origin,
			COUNT(*)::bigint AS plays,
			COUNT(*) FILTER (WHERE `+insightsPlayDirectSQL+`)::bigint AS direct_plays,
			COUNT(*) FILTER (WHERE `+insightsPlayRemuxSQL+`)::bigint AS remux_plays,
			COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+`)::bigint AS transcode_plays
		FROM admin_playback_history h
		LEFT JOIN media_files mf ON mf.id = h.media_file_id
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
		WHERE `+whereSQL+`
		GROUP BY 1, 2, 3
	`, whereArgs...)
	if err != nil {
		return fmt.Errorf("querying insights client codec matrix: %w", err)
	}
	defer rows.Close()

	type matrixKey struct{ client, platform string }
	rowMap := make(map[matrixKey]*AdminInsightsClientMatrixRow)
	for rows.Next() {
		var clientName, platform, codec string
		var compatOrigin bool
		var plays, direct, remux, transcode int64
		if err := rows.Scan(&clientName, &platform, &codec, &compatOrigin, &plays, &direct, &remux, &transcode); err != nil {
			return fmt.Errorf("scanning insights client codec matrix: %w", err)
		}
		k := matrixKey{client: clientName, platform: platform}
		row, ok := rowMap[k]
		if !ok {
			row = &AdminInsightsClientMatrixRow{
				ClientName: clientName, DevicePlatform: platform,
				IsJellyfinClient: compatOrigin || isJellyfinEcosystemClient(clientName, ""),
				Cells:            make([]AdminInsightsClientCodecCell, 0, 4),
			}
			rowMap[k] = row
		} else if compatOrigin {
			row.IsJellyfinClient = true
		}
		row.TotalPlays += plays
		row.DirectPlays += direct
		row.RemuxPlays += remux
		row.TranscodePlays += transcode
		row.Cells = append(row.Cells, AdminInsightsClientCodecCell{
			SourceVideoCodec: codec, Plays: plays, DirectPlays: direct,
			RemuxPlays: remux, TranscodePlays: transcode, DirectRate: roundInsightsRate(direct, plays),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	codecTotals := make(map[string]int64)
	for _, row := range rowMap {
		if row.TotalPlays < int64(minPlays) {
			continue
		}
		row.DirectRate = roundInsightsRate(row.DirectPlays, row.TotalPlays)
		for _, cell := range row.Cells {
			codecTotals[cell.SourceVideoCodec] += cell.Plays
		}
		out.Matrix = append(out.Matrix, *row)
	}
	orderClientCodecMatrix(out, codecTotals)
	return nil
}

func orderClientCodecMatrix(out *AdminInsightsDevices, codecTotals map[string]int64) {
	slices.SortFunc(out.Matrix, func(a, b AdminInsightsClientMatrixRow) int {
		if c := cmp.Compare(b.TotalPlays, a.TotalPlays); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ClientName, b.ClientName); c != 0 {
			return c
		}
		return cmp.Compare(a.DevicePlatform, b.DevicePlatform)
	})
	for codec := range codecTotals {
		out.Codecs = append(out.Codecs, codec)
	}
	slices.SortFunc(out.Codecs, func(a, b string) int {
		if a == insightsUnknownKey && b != insightsUnknownKey {
			return 1
		}
		if b == insightsUnknownKey && a != insightsUnknownKey {
			return -1
		}
		if c := cmp.Compare(codecTotals[b], codecTotals[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	rank := make(map[string]int, len(out.Codecs))
	for i, c := range out.Codecs {
		rank[c] = i
	}
	for i := range out.Matrix {
		slices.SortFunc(out.Matrix[i].Cells, func(a, b AdminInsightsClientCodecCell) int {
			return cmp.Compare(rank[a.SourceVideoCodec], rank[b.SourceVideoCodec])
		})
	}
}

func queryInsightsAdaptationReasons(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	rows, err := tx.Query(ctx, `
		SELECT reason, COUNT(*)::bigint
		FROM admin_playback_history h
		`+insightsLibraryJoinSQL(libraryID)+`
		CROSS JOIN LATERAL unnest(h.adaptation_reasons) AS reason
		WHERE `+whereSQL+`
		  AND NOT (`+insightsPlayDirectSQL+`)
		GROUP BY reason
		ORDER BY 2 DESC, 1 ASC
	`, whereArgs...)
	if err != nil {
		return fmt.Errorf("querying insights adaptation reasons: %w", err)
	}
	defer rows.Close()
	reasonCounts := make(map[string]int64)
	for rows.Next() {
		var reason string
		var count int64
		if err := rows.Scan(&reason, &count); err != nil {
			return err
		}
		if norm := playback.NormalizeAdaptationReasons([]string{reason}); len(norm) > 0 {
			reasonCounts[norm[0]] += count
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	adaptedTotal := out.Summary.RemuxPlays + out.Summary.TranscodePlays
	for reason, count := range reasonCounts {
		out.AdaptationReasons = append(out.AdaptationReasons, AdminInsightsAdaptationReason{
			Reason: reason, Plays: count, ShareOfAdapted: roundInsightsRate(count, adaptedTotal),
		})
	}
	slices.SortFunc(out.AdaptationReasons, func(a, b AdminInsightsAdaptationReason) int {
		if c := cmp.Compare(b.Plays, a.Plays); c != 0 {
			return c
		}
		return cmp.Compare(a.Reason, b.Reason)
	})
	return nil
}

func queryLabeledCountMap(ctx context.Context, tx pgx.Tx, sql string, args []any, normalize func(string) string) ([]AdminInsightsLabeledCount, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if normalize != nil {
			k = normalize(k)
		}
		counts[k] += v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sortedLabeledCounts(counts), nil
}

func queryInsightsDevicesBreakdowns(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	joinSQL := insightsLibraryJoinSQL(libraryID)
	var err error
	out.HWAccelBreakdown, err = queryLabeledCountMap(ctx, tx, `
		SELECT COALESCE(h.transcode_hw_accel, ''), COUNT(*)::bigint
		FROM admin_playback_history h `+joinSQL+`
		WHERE `+whereSQL+` AND `+insightsPlayTranscodeSQL+` AND h.route_facts_recorded
		GROUP BY 1
	`, whereArgs, normalizeHWAccelKey)
	if err != nil {
		return fmt.Errorf("querying insights hw accel breakdown: %w", err)
	}
	out.ToneMapBreakdown, err = queryLabeledCountMap(ctx, tx, `
		SELECT LOWER(BTRIM(h.tone_map_mode)), COUNT(*)::bigint
		FROM admin_playback_history h `+joinSQL+`
		WHERE `+whereSQL+` AND NULLIF(BTRIM(COALESCE(h.tone_map_mode, '')), '') IS NOT NULL
		GROUP BY 1
	`, whereArgs, nil)
	if err != nil {
		return fmt.Errorf("querying insights tone map breakdown: %w", err)
	}
	out.SubtitleBurnInCodecs, err = queryLabeledCountMap(ctx, tx, `
		SELECT COALESCE(NULLIF(LOWER(BTRIM(h.subtitle->>'codec')), ''), 'unknown'), COUNT(*)::bigint
		FROM admin_playback_history h `+joinSQL+`
		WHERE `+whereSQL+` AND h.subtitle->>'delivery' = 'burn_in'
		GROUP BY 1
	`, whereArgs, nil)
	if err != nil {
		return fmt.Errorf("querying insights burn-in subtitle codecs: %w", err)
	}
	return nil
}

func queryInsightsTopTranscodingAccounts(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	joinSQL := insightsLibraryJoinSQL(libraryID)
	rows, err := tx.Query(ctx, `
		WITH per_user AS (
			SELECT
				h.user_id,
				COUNT(*)::bigint AS total_plays,
				COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+`)::bigint AS transcode_plays
			FROM admin_playback_history h `+joinSQL+`
			WHERE `+whereSQL+`
			GROUP BY h.user_id
			HAVING COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+`) > 0
			ORDER BY transcode_plays DESC, total_plays DESC, h.user_id ASC
			LIMIT 10
		),
		top_reasons AS (
			SELECT DISTINCT ON (h.user_id) h.user_id, reason
			FROM admin_playback_history h `+joinSQL+`
			CROSS JOIN LATERAL unnest(h.adaptation_reasons) AS reason
			WHERE `+whereSQL+` AND `+insightsPlayTranscodeSQL+`
			  AND h.user_id IN (SELECT user_id FROM per_user)
			GROUP BY h.user_id, reason
			ORDER BY h.user_id, COUNT(*) DESC, reason ASC
		)
		SELECT p.user_id, COALESCE(u.username, ''), p.transcode_plays, p.total_plays, tr.reason
		FROM per_user p
		LEFT JOIN users u ON u.id = p.user_id
		LEFT JOIN top_reasons tr ON tr.user_id = p.user_id
		ORDER BY p.transcode_plays DESC, p.total_plays DESC, p.user_id ASC
	`, whereArgs...)
	if err != nil {
		return fmt.Errorf("querying insights top transcoding accounts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item AdminInsightsTopTranscodingAccount
		var reason *string
		if err := rows.Scan(&item.UserID, &item.Username, &item.TranscodePlays, &item.TotalPlays, &reason); err != nil {
			return err
		}
		if item.Username == "" {
			item.Username = fmt.Sprintf("User %d", item.UserID)
		}
		item.TranscodeRate = roundInsightsRate(item.TranscodePlays, item.TotalPlays)
		if reason != nil {
			if norm := playback.NormalizeAdaptationReasons([]string{*reason}); len(norm) > 0 {
				r := norm[0]
				item.TopReason = &r
			}
		}
		out.TopTranscodingAccount = append(out.TopTranscodingAccount, item)
	}
	return rows.Err()
}

func sortedLabeledCounts(m map[string]int64) []AdminInsightsLabeledCount {
	res := make([]AdminInsightsLabeledCount, 0, len(m))
	for k, v := range m {
		res = append(res, AdminInsightsLabeledCount{Key: k, Plays: v})
	}
	slices.SortFunc(res, func(a, b AdminInsightsLabeledCount) int {
		if a.Key == insightsUnknownKey && b.Key != insightsUnknownKey {
			return 1
		}
		if b.Key == insightsUnknownKey && a.Key != insightsUnknownKey {
			return -1
		}
		if c := cmp.Compare(b.Plays, a.Plays); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return res
}

// ---- Bandwidth -------------------------------------------------------------

func queryAdminInsightsBandwidth(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsBandwidthQuery) (*AdminInsightsBandwidth, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning insights bandwidth read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	_, _, oldestRoute, oldestMetric, dbNow, err := queryInsightsGlobalBounds(ctx, tx)
	if err != nil {
		return nil, err
	}
	win, err := resolveInsightsWindow(dbNow, q.Range, q.FromDate, q.ToDate, q.TZ)
	if err != nil {
		return nil, err
	}
	resolveAllTimeBounds(&win, oldestMetric)

	out := &AdminInsightsBandwidth{
		From: win.From, To: win.To, TZ: win.TZ,
		OldestMetricAt: oldestMetric, OldestRouteFactsAt: oldestRoute,
		Sources: make([]AdminInsightsEgressSource, 0), Points: make([]AdminInsightsBandwidthPoint, 0),
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stream_nodes)`).Scan(&out.HasRemoteNodes); err != nil {
		return nil, fmt.Errorf("checking stream nodes: %w", err)
	}
	fromDay, toDay := utcDaySpanForWindow(win)
	pointIdx, apiBytes, apiPeak, err := queryInsightsBandwidthDaily(ctx, tx, fromDay, toDay, out)
	if err != nil {
		return nil, err
	}
	nodeSources, err := queryInsightsBandwidthNodes(ctx, tx, fromDay, toDay, pointIdx, out)
	if err != nil {
		return nil, err
	}
	if out.Summary.ActiveDays > 0 {
		out.Summary.AvgDailyBytes = out.Summary.TotalEgressBytes / out.Summary.ActiveDays
	}
	out.Sources = buildInsightsEgressSources(apiBytes, apiPeak, out.Summary.TotalEgressBytes, nodeSources)
	if err := queryInsightsStreamLocationShare(ctx, tx, win, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights bandwidth read: %w", err)
	}
	return out, nil
}

func queryInsightsBandwidthDaily(ctx context.Context, tx pgx.Tx, fromDay, toDay string, out *AdminInsightsBandwidth) (map[string]int, int64, int64, error) {
	rows, err := tx.Query(ctx, insightsCombinedDailyMetricsCTE+`
		SELECT
			to_char(day, 'YYYY-MM-DD') AS day_str,
			egress_bytes, download_egress_bytes, api_egress_bytes,
			peak_egress_kbps, peak_download_kbps, api_peak_egress_kbps, peak_egress_at
		FROM combined_daily
		ORDER BY day ASC
	`, fromDay, toDay, int64(bytesPerKbpsMinute))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("querying insights bandwidth daily: %w", err)
	}
	defer rows.Close()

	pointIdx := make(map[string]int)
	var apiTotalBytes, apiPeakKbps int64
	for rows.Next() {
		var pt AdminInsightsBandwidthPoint
		var apiPeak int64
		var peakAt *time.Time
		if err := rows.Scan(
			&pt.Date, &pt.EgressBytes, &pt.DownloadEgressBytes, &pt.APIEgressBytes,
			&pt.PeakEgressKbps, &pt.PeakDownloadKbps, &apiPeak, &peakAt,
		); err != nil {
			return nil, 0, 0, fmt.Errorf("scanning insights bandwidth day: %w", err)
		}
		peakAt = normalizeTimePtrUTC(peakAt)
		pt.PlaybackEgressBytes = max(pt.EgressBytes-pt.DownloadEgressBytes, 0)
		pt.BytesBySource = make(map[string]int64)
		pt.PeakKbpsBySource = make(map[string]int64)
		if pt.APIEgressBytes > 0 || apiPeak > 0 {
			pt.BytesBySource[egressSourceAPIKey] = pt.APIEgressBytes
			pt.PeakKbpsBySource[egressSourceAPIKey] = apiPeak
		}
		apiTotalBytes += pt.APIEgressBytes
		apiPeakKbps = max(apiPeakKbps, apiPeak)

		out.Summary.TotalEgressBytes += pt.EgressBytes
		out.Summary.DownloadEgressBytes += pt.DownloadEgressBytes
		out.Summary.PlaybackEgressBytes += pt.PlaybackEgressBytes
		out.Summary.APIEgressBytes += pt.APIEgressBytes
		out.Summary.ActiveDays++
		if pt.PeakEgressKbps > out.Summary.PeakEgressKbps ||
			(pt.PeakEgressKbps == out.Summary.PeakEgressKbps && pt.PeakEgressKbps > 0 && out.Summary.PeakEgressAt == nil) {
			out.Summary.PeakEgressKbps = pt.PeakEgressKbps
			out.Summary.PeakEgressAt = peakAt
		}
		out.Summary.PeakDownloadKbps = max(out.Summary.PeakDownloadKbps, pt.PeakDownloadKbps)
		pointIdx[pt.Date] = len(out.Points)
		out.Points = append(out.Points, pt)
	}
	return pointIdx, apiTotalBytes, apiPeakKbps, rows.Err()
}

func queryInsightsBandwidthNodes(ctx context.Context, tx pgx.Tx, fromDay, toDay string, pointIdx map[string]int, out *AdminInsightsBandwidth) (map[int]*AdminInsightsEgressSource, error) {
	rows, err := tx.Query(ctx, `
		WITH node_daily AS (
			SELECT day, node_id, egress_bytes, peak_egress_kbps
			FROM dashboard_node_egress_daily
			WHERE day >= $1::date AND day <= $2::date
			UNION ALL
			SELECT
				(s.bucket AT TIME ZONE 'UTC')::date AS day,
				s.node_id,
				(COALESCE(SUM(s.egress_kbps), 0) * $3)::bigint AS egress_bytes,
				COALESCE(MAX(s.egress_kbps), 0)::bigint AS peak_egress_kbps
			FROM dashboard_node_egress_samples s
			WHERE (s.bucket AT TIME ZONE 'UTC')::date >= $1::date
			  AND (s.bucket AT TIME ZONE 'UTC')::date <= $2::date
			  AND NOT EXISTS (
				SELECT 1 FROM dashboard_node_egress_daily d
				WHERE d.day = (s.bucket AT TIME ZONE 'UTC')::date AND d.node_id = s.node_id
			  )
			GROUP BY 1, 2
		)
		SELECT
			to_char(nd.day, 'YYYY-MM-DD') AS day_str,
			nd.node_id,
			COALESCE(NULLIF(BTRIM(sn.name), ''), 'Removed node ' || nd.node_id::text) AS node_name,
			sn.type,
			nd.egress_bytes,
			nd.peak_egress_kbps
		FROM node_daily nd
		LEFT JOIN stream_nodes sn ON sn.id = nd.node_id
		ORDER BY nd.day ASC, nd.node_id ASC
	`, fromDay, toDay, int64(bytesPerKbpsMinute))
	if err != nil {
		return nil, fmt.Errorf("querying insights node daily egress: %w", err)
	}
	defer rows.Close()

	nodeSources := make(map[int]*AdminInsightsEgressSource)
	for rows.Next() {
		var dayStr, nodeName string
		var nodeID int
		var nodeType *string
		var egressBytes, peakKbps int64
		if err := rows.Scan(&dayStr, &nodeID, &nodeName, &nodeType, &egressBytes, &peakKbps); err != nil {
			return nil, fmt.Errorf("scanning insights node daily egress: %w", err)
		}
		key := fmt.Sprintf("node:%d", nodeID)
		src, ok := nodeSources[nodeID]
		if !ok {
			src = &AdminInsightsEgressSource{
				Key: key, Kind: EgressSourceKindNode, NodeID: nodeID, Name: nodeName, NodeType: nodeType,
			}
			nodeSources[nodeID] = src
		}
		src.EgressBytes += egressBytes
		src.PeakEgressKbps = max(src.PeakEgressKbps, peakKbps)
		out.Summary.NodeEgressBytes += egressBytes
		out.HasRemoteNodes = true
		if idx, exists := pointIdx[dayStr]; exists {
			out.Points[idx].BytesBySource[key] = egressBytes
			out.Points[idx].PeakKbpsBySource[key] = peakKbps
		}
	}
	return nodeSources, rows.Err()
}

func buildInsightsEgressSources(apiBytes, apiPeak, totalBytes int64, nodeSources map[int]*AdminInsightsEgressSource) []AdminInsightsEgressSource {
	sources := make([]AdminInsightsEgressSource, 0, len(nodeSources)+1)
	if apiBytes > 0 || apiPeak > 0 || totalBytes > 0 {
		sources = append(sources, AdminInsightsEgressSource{
			Key: egressSourceAPIKey, Kind: EgressSourceKindAPI, Name: egressSourceAPIName,
			EgressBytes: apiBytes, PeakEgressKbps: apiPeak, ShareOfTotal: roundInsightsRate(apiBytes, totalBytes),
		})
	}
	nodes := make([]AdminInsightsEgressSource, 0, len(nodeSources))
	for _, src := range nodeSources {
		src.ShareOfTotal = roundInsightsRate(src.EgressBytes, totalBytes)
		nodes = append(nodes, *src)
	}
	slices.SortFunc(nodes, func(a, b AdminInsightsEgressSource) int {
		if c := cmp.Compare(b.EgressBytes, a.EgressBytes); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return append(sources, nodes...)
}

func queryInsightsStreamLocationShare(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, out *AdminInsightsBandwidth) error {
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, 0, 0)
	err := tx.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE h.stream_location = 'remote')::bigint,
			COUNT(*) FILTER (WHERE h.stream_location = 'local')::bigint,
			COUNT(*) FILTER (WHERE h.stream_location IS NULL OR h.stream_location NOT IN ('local', 'remote'))::bigint
		FROM admin_playback_history h
		WHERE `+whereSQL, whereArgs...).Scan(
		&out.Summary.RemotePlays, &out.Summary.LocalPlays, &out.Summary.UnknownLocationPlays,
	)
	if err != nil {
		return fmt.Errorf("querying insights bandwidth stream location share: %w", err)
	}
	return nil
}
