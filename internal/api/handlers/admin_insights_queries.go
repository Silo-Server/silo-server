package handlers

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/dashmetrics"
)

var (
	insightsPlayDirectSQL    = insightsDirectSQL("h.play_method")
	insightsPlayRemuxSQL     = insightsRemuxSQL("h.play_method")
	insightsPlayTranscodeSQL = insightsTranscodeSQL("h.play_method")
)

const (
	insightsSortWatchTime     = "watch_time"
	insightsSortPlays         = "plays"
	insightsSortDirectPct     = "direct_pct"
	insightsSortCompletionPct = "completion_pct"
	insightsSortLastSeen      = "last_seen"
	insightsHWSoftwareKey     = "software"
	insightsHWAutoKey         = "auto"
)

func roundInsightsRate(num, den int64) float64 {
	if den <= 0 {
		return 0
	}
	return math.Round(float64(num)/float64(den)*10000) / 10000
}

// buildInsightsHistoryWhere filters sessions (not plays) that started in the
// window, for the session-based reads: Devices, Bandwidth's location share and
// the People top client. Play counts use insightsPlaysScope instead.
func buildInsightsHistoryWhere(win resolvedInsightsWindow, libraryID, userID int, profileID string) (whereSQL string, args []any) {
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
	if profileID = strings.TrimSpace(profileID); profileID != "" {
		args = append(args, profileID)
		conds = append(conds, fmt.Sprintf("h.profile_id = $%d", len(args)))
	}
	if libraryID > 0 {
		args = append(args, libraryID)
		conds = append(conds, fmt.Sprintf("%s = $%d",
			insightsLibraryAttributionSQL("mf.media_folder_id", "mf.id", "COALESCE(ep.series_id, h.media_item_id)"), len(args)))
	}
	return strings.Join(conds, " AND "), args
}

func insightsLibraryJoinSQL(libraryID int) string {
	if libraryID <= 0 {
		return ""
	}
	return `
		LEFT JOIN media_files mf ON mf.id = COALESCE(h.effective_media_file_id, h.media_file_id)
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id`
}

func queryInsightsGlobalBounds(ctx context.Context, tx pgx.Tx) (oldestHistory, oldestMeasured, oldestRouteFacts, oldestMetric *time.Time, dbNow time.Time, err error) {
	var oldestDailyDay, oldestRawSample *time.Time
	err = tx.QueryRow(ctx, `
		SELECT
			(SELECT MIN(started_at) FROM admin_playback_history),
			(SELECT MIN(started_at) FROM admin_playback_history WHERE measured),
			(SELECT MIN(started_at) FROM admin_playback_history WHERE route_facts_recorded),
			(SELECT MIN(day)::timestamp AT TIME ZONE 'UTC' FROM dashboard_metric_daily),
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
		win.From = localDayStart(localStart.Year(), localStart.Month(), localStart.Day(), win.Loc).UTC()
		win.FromLocalDate = time.Date(localStart.Year(), localStart.Month(), localStart.Day(), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		win.BucketUnit = pickInsightsBucketUnit(win.To.Sub(win.From))
		return
	}
	nowLocal := win.To.In(win.Loc)
	startCal := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -29)
	win.From = localDayStart(startCal.Year(), startCal.Month(), startCal.Day(), win.Loc).UTC()
	win.FromLocalDate = startCal.Format("2006-01-02")
	win.BucketUnit = InsightsBucketDay
}

// insightsCombinedDailyMetricsCTE combines archived UTC days from
// dashboard_metric_daily with live minute samples from dashboard_metric_samples
// using dashmetrics.DailyMetricRollupCTEs so the rollup math matches the
// sampler exactly.
//
// Parameters:
//   - $1: UTC start calendar date (YYYY-MM-DD, inclusive)
//   - $2: UTC end calendar date (YYYY-MM-DD, inclusive)
//   - $3: exclusive UTC upper timestamp bound (caps today's live minutes at dbNow)
//   - $4: dashmetrics.BytesPerKbpsMinute
//
// Both the daily archive and unarchived minute samples are bounded by the same
// UTC calendar day range [$1, $2], so pruning minute samples after retention
// never changes the answer for a date range in any timezone.
var insightsCombinedDailyMetricsCTE = `
	WITH daily_archive AS (
		SELECT
			d.day,
			d.peak_streams::bigint AS peak_streams,
			d.peak_direct::bigint AS peak_direct,
			d.peak_remux::bigint AS peak_remux,
			d.peak_transcode::bigint AS peak_transcode,
			d.peak_streams_direct::bigint AS peak_streams_direct,
			d.peak_streams_remux::bigint AS peak_streams_remux,
			d.peak_streams_transcode::bigint AS peak_streams_transcode,
			d.peak_egress_kbps,
			d.peak_download_kbps,
			d.egress_bytes,
			d.download_egress_bytes,
			d.api_egress_bytes,
			d.api_peak_egress_kbps,
			d.peak_streams_at,
			d.peak_egress_at
		FROM dashboard_metric_daily d
		WHERE d.day >= $1::date AND d.day <= $2::date
	),` + dashmetrics.DailyMetricRollupCTEs(
	`s.bucket >= ($1::date::timestamp AT TIME ZONE 'UTC')
		AND s.bucket < LEAST((($2::date + 1)::timestamp AT TIME ZONE 'UTC'), $3::timestamptz)
		AND NOT EXISTS (SELECT 1 FROM daily_archive d WHERE d.day = (s.bucket AT TIME ZONE 'UTC')::date)`,
	`$4`,
) + `,
	combined_daily AS (
		SELECT * FROM daily_archive
		UNION ALL
		SELECT * FROM _dm_daily
	)`

// metricDaySpanForWindow maps an Insights window to the UTC calendar-day span
// read from dashboard_metric_daily / dashboard_node_egress_daily and unarchived
// raw samples. Preset windows (7d/30d/90d/1y) anchor strictly on dbNow's UTC
// day so a 7d request always spans exactly 7 UTC days regardless of the
// viewer's timezone.
func metricDaySpanForWindow(win resolvedInsightsWindow, dbNow time.Time) (fromDay, toDay string, upperUTC time.Time) {
	nowUTC := dbNow.UTC()
	todayUTC := nowUTC.Truncate(24 * time.Hour)
	switch win.Range {
	case InsightsRange7d:
		return todayUTC.AddDate(0, 0, -6).Format("2006-01-02"), todayUTC.Format("2006-01-02"), nowUTC
	case InsightsRange30d:
		return todayUTC.AddDate(0, 0, -29).Format("2006-01-02"), todayUTC.Format("2006-01-02"), nowUTC
	case InsightsRange90d:
		return todayUTC.AddDate(0, 0, -89).Format("2006-01-02"), todayUTC.Format("2006-01-02"), nowUTC
	case InsightsRange1y:
		return todayUTC.AddDate(0, 0, -364).Format("2006-01-02"), todayUTC.Format("2006-01-02"), nowUTC
	case InsightsRangeCustom:
		fromDay = win.FromLocalDate
		toDay = win.ToLocalDate
		if toCal, err := time.Parse("2006-01-02", toDay); err == nil {
			return fromDay, toDay, toCal.AddDate(0, 0, 1).UTC()
		}
		return fromDay, toDay, win.To.UTC()
	default:
		// All-time (or any fallback): span from earliest UTC day through today UTC.
		fromDay = win.From.UTC().Format("2006-01-02")
		if win.FromLocalDate != "" && win.FromLocalDate < fromDay {
			fromDay = win.FromLocalDate
		}
		return fromDay, todayUTC.Format("2006-01-02"), nowUTC
	}
}

// ---- Activity --------------------------------------------------------------

func queryAdminInsightsActivity(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsWindowQuery) (*AdminInsightsActivity, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if err := validateInsightsScope(q); err != nil {
		return nil, err
	}
	ctx, tx, done, err := beginAdminHistoryRead(ctx, pool, insightsReadWorkMem)
	if err != nil {
		return nil, fmt.Errorf("beginning insights activity read: %w", err)
	}
	defer done()

	oldestHist, oldestMeas, oldestRoute, oldestMetric, dbNow, err := queryInsightsGlobalBounds(ctx, tx)
	if err != nil {
		return nil, err
	}
	win, err := resolveInsightsWindow(dbNow, q.Range, q.FromDate, q.ToDate, q.TZ)
	if err != nil {
		return nil, err
	}
	resolveAllTimeBounds(&win, earlierTimePtr(oldestHist, oldestMetric))

	out := &AdminInsightsActivity{
		From: win.From, To: win.To, TZ: win.TZ, BucketUnit: win.BucketUnit,
		Coverage: AdminInsightsCoverage{
			OldestHistoryAt: oldestHist, OldestMeasuredAt: oldestMeas,
			OldestRouteFactsAt: oldestRoute, OldestMetricAt: oldestMetric,
		},
		Buckets: make([]AdminInsightsActivityBucket, 0), Heatmap: make([]AdminInsightsHeatmapCell, 0),
		PeakStreams: make([]AdminInsightsPeakStreamsPoint, 0), EndReasons: make([]AdminInsightsLabeledCount, 0),
	}
	if err := queryInsightsActivityPlays(ctx, tx, win, newInsightsPlaysScope(win, q), out); err != nil {
		return nil, err
	}
	if err := queryInsightsEndReasons(ctx, tx, win, q, out); err != nil {
		return nil, err
	}
	if err := queryInsightsActivityPeakStreams(ctx, tx, win, dbNow, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights activity read: %w", err)
	}
	return out, nil
}

// insightsActivityPlaysSQL is the statement behind queryInsightsActivityPlays:
// the summary, the time buckets and the heatmap over one materialized pass of
// the viewing grouping. The heatmap hash-aggregates; the summary and the
// buckets carry distinct account and profile counts, which sort.
func insightsActivityPlaysSQL(win resolvedInsightsWindow, scope insightsPlaysScope) (string, []any) {
	var args []any
	with, counted := scope.viewingRowsSQL(&args)
	args = append(args, win.BucketUnit, win.TZ)
	unitParam, tzParam := len(args)-1, len(args)
	aggregates := fmt.Sprintf(`
			COUNT(*) FILTER (WHERE is_play)::bigint,
			COUNT(*)::bigint,
			COUNT(*) FILTER (WHERE is_play AND NOT is_episode AND item_type = 'movie')::bigint,
			COUNT(*) FILTER (WHERE is_play AND is_episode)::bigint,
			COUNT(*) FILTER (WHERE is_play AND NOT is_episode AND COALESCE(item_type, '') <> 'movie')::bigint,
			COALESCE(SUM(watch_sec), 0)::double precision,
			COALESCE(SUM(CASE WHEN measured THEN played_seconds ELSE 0 END), 0)::double precision,
			COUNT(*) FILTER (WHERE measured)::bigint,
			COUNT(*) FILTER (WHERE route_facts_recorded)::bigint,
			COUNT(*) FILTER (WHERE is_play AND viewing_completed)::bigint,
			COUNT(*) FILTER (WHERE is_play AND %s)::bigint,
			COUNT(*) FILTER (WHERE is_play AND %s)::bigint,
			COUNT(*) FILTER (WHERE is_play AND %s)::bigint`,
		insightsDirectSQL("viewing_play_method"), insightsRemuxSQL("viewing_play_method"), insightsTranscodeSQL("viewing_play_method"))
	return with + fmt.Sprintf(`,
		counted AS MATERIALIZED (
			SELECT vr.user_id, vr.profile_id, vr.is_episode, vr.item_type, vr.watch_sec, vr.measured,
				vr.played_seconds, vr.route_facts_recorded, vr.viewing_completed, vr.viewing_play_method,
				%[1]s AS is_play,
				date_trunc($%[2]d, vr.started_at AT TIME ZONE $%[3]d) AS local_bucket,
				(EXTRACT(ISODOW FROM (vr.started_at AT TIME ZONE $%[3]d))::int - 1) AS dow,
				EXTRACT(HOUR FROM (vr.started_at AT TIME ZONE $%[3]d))::int AS hour
			FROM vr WHERE %[4]s
		)
		SELECT 1, 3, NULL::timestamptz, '', 0, 0, %[5]s,
			COUNT(DISTINCT user_id)::bigint, COUNT(DISTINCT (user_id, profile_id))::bigint
		FROM counted
		UNION ALL
		SELECT 0, 3, local_bucket AT TIME ZONE $%[3]d, to_char(local_bucket, 'YYYY-MM-DD'), 0, 0, %[5]s,
			COUNT(DISTINCT user_id)::bigint, COUNT(DISTINCT (user_id, profile_id))::bigint
		FROM counted GROUP BY local_bucket
		UNION ALL
		SELECT 1, 0, NULL::timestamptz, '', dow, hour, %[5]s, 0::bigint, 0::bigint
		FROM counted GROUP BY dow, hour
		ORDER BY 1, 2, 3, 5, 6
	`, insightsIsPlaySQL("vr"), unitParam, tzParam, counted, aggregates), args
}

// queryInsightsActivityPlays reads the summary, the time buckets and the
// heatmap in one statement, so the viewing grouping runs once per read.
func queryInsightsActivityPlays(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, scope insightsPlaysScope, out *AdminInsightsActivity) error {
	query, args := insightsActivityPlaysSQL(win, scope)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("querying insights activity: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var gBucket, gCell, dow, hour int
		var bucketStart *time.Time
		var label string
		var plays, sessions, movies, episodes, other, measured, routeFacts, completed, direct, remux, transcode, accounts, profiles int64
		var watchSec, measuredWatchSec float64
		if err := rows.Scan(&gBucket, &gCell, &bucketStart, &label, &dow, &hour,
			&plays, &sessions, &movies, &episodes, &other, &watchSec, &measuredWatchSec, &measured, &routeFacts,
			&completed, &direct, &remux, &transcode, &accounts, &profiles); err != nil {
			return fmt.Errorf("scanning insights activity: %w", err)
		}
		watch := int64(math.Round(watchSec))
		switch {
		case gBucket == 0 && bucketStart != nil:
			out.Buckets = append(out.Buckets, AdminInsightsActivityBucket{
				BucketStart: bucketStart.UTC(), LabelDate: label, Plays: plays, WatchSeconds: watch,
				Direct: direct, Remux: remux, Transcode: transcode, Movies: movies, Episodes: episodes, OtherMedia: other,
				ActiveAccounts: accounts, ActiveProfiles: profiles,
			})
		case gCell == 0:
			out.Heatmap = append(out.Heatmap, AdminInsightsHeatmapCell{DayOfWeek: dow, Hour: hour, Plays: plays, WatchSeconds: watch})
		default:
			sum := &out.Summary
			sum.Plays, sum.Sessions, sum.Movies, sum.Episodes, sum.OtherMedia = plays, sessions, movies, episodes, other
			sum.WatchSeconds, sum.MeasuredWatchSeconds = watch, int64(math.Round(measuredWatchSec))
			sum.CompletedPlays, sum.CompletionRate = completed, roundInsightsRate(completed, plays)
			sum.DirectPlays, sum.RemuxPlays, sum.TranscodePlays = direct, remux, transcode
			sum.UniqueAccounts, sum.UniqueProfiles = accounts, profiles
			out.Coverage.TotalSessions, out.Coverage.MeasuredSessions, out.Coverage.RouteFactsSessions = sessions, measured, routeFacts
		}
	}
	return rows.Err()
}

// queryInsightsEndReasons counts the window's sessions by end reason. It reads
// sessions, not plays: every session ends on its own.
func queryInsightsEndReasons(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, q AdminInsightsWindowQuery, out *AdminInsightsActivity) error {
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, q.LibraryID, q.UserID, q.ProfileID)
	counts, err := queryLabeledCountMap(ctx, tx, `
		SELECT COALESCE(NULLIF(LOWER(BTRIM(h.end_reason)), ''), '`+insightsUnknownKey+`'), COUNT(*)::bigint
		FROM admin_playback_history h `+insightsLibraryJoinSQL(q.LibraryID)+`
		WHERE `+whereSQL+`
		GROUP BY 1
	`, whereArgs, nil)
	if err != nil {
		return fmt.Errorf("querying insights end reasons: %w", err)
	}
	out.EndReasons = counts
	return nil
}

func queryInsightsActivityPeakStreams(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, dbNow time.Time, out *AdminInsightsActivity) error {
	fromDay, toDay, upperUTC := metricDaySpanForWindow(win, dbNow)
	rows, err := tx.Query(ctx, insightsCombinedDailyMetricsCTE+`
		SELECT
			to_char(day, 'YYYY-MM-DD') AS day_str,
			peak_streams, peak_direct, peak_remux, peak_transcode,
			peak_streams_direct, peak_streams_remux, peak_streams_transcode,
			peak_streams_at
		FROM combined_daily
		ORDER BY day ASC
	`, fromDay, toDay, upperUTC, int64(dashmetrics.BytesPerKbpsMinute))
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
