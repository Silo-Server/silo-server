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

	"github.com/Silo-Server/silo-server/internal/dashmetrics"
	"github.com/Silo-Server/silo-server/internal/playback"
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
	tx, err := beginInsightsPlaysRead(ctx, pool)
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

	out := &AdminInsightsActivity{
		From: win.From, To: win.To, TZ: win.TZ, BucketUnit: win.BucketUnit,
		Coverage: AdminInsightsCoverage{
			OldestHistoryAt: oldestHist, OldestMeasuredAt: oldestMeas,
			OldestRouteFactsAt: oldestRoute, OldestMetricAt: oldestMetric,
		},
		Buckets: make([]AdminInsightsActivityBucket, 0), Heatmap: make([]AdminInsightsHeatmapCell, 0),
		PeakStreams: make([]AdminInsightsPeakStreamsPoint, 0),
	}
	if err := queryInsightsActivityPlays(ctx, tx, win, newInsightsPlaysScope(win, q), out); err != nil {
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
// the viewing grouping. The buckets and heatmap hash-aggregate; only the
// summary row needs the distinct account and profile counts, which sort.
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
		SELECT 0, 3, local_bucket AT TIME ZONE $%[3]d, to_char(local_bucket, 'YYYY-MM-DD'), 0, 0, %[5]s, 0::bigint, 0::bigint
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

// ---- People ----------------------------------------------------------------

func queryAdminInsightsPeopleSnapshot(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	tx, err := beginInsightsPlaysRead(ctx, pool)
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
	// People covers every account; it takes no account or profile filter.
	scope := newInsightsPlaysScope(win, AdminInsightsWindowQuery{LibraryID: q.LibraryID})
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, q.LibraryID, 0, "")

	out := &AdminInsightsPeople{
		From: win.From, To: win.To, TZ: win.TZ,
		Coverage: AdminInsightsCoverage{
			OldestHistoryAt: oldestHist, OldestMeasuredAt: oldestMeas,
			OldestRouteFactsAt: oldestRoute, OldestMetricAt: oldestMetric,
		},
		Accounts: make([]AdminInsightsPersonAccount, 0),
	}

	liveUsers, liveProfiles, err := queryInsightsLiveUsers(ctx, tx, q.LibraryID)
	if err != nil {
		return nil, err
	}
	out.LiveAccounts = int64(len(liveUsers))
	byUser, err := queryInsightsPeopleProfiles(ctx, tx, scope, liveUsers, liveProfiles, out)
	if err != nil {
		return nil, err
	}
	if err := queryInsightsPeopleTopClients(ctx, tx, q.LibraryID, whereSQL, whereArgs, byUser); err != nil {
		return nil, err
	}

	allAccounts := finalizeInsightsPeopleAccounts(byUser, q.Search, out)
	sortInsightsAccounts(allAccounts, q.Sort, q.Dir)
	out.Accounts = allAccounts
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights people read: %w", err)
	}
	return out, nil
}

func queryAdminInsightsPeople(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error) {
	full, err := queryAdminInsightsPeopleSnapshot(ctx, pool, q)
	if err != nil {
		return nil, err
	}
	return sliceAdminInsightsPeople(full, q.Limit, q.Offset), nil
}

func sliceAdminInsightsPeople(full *AdminInsightsPeople, rawLimit, rawOffset int) *AdminInsightsPeople {
	if full == nil {
		return nil
	}
	cp := *full
	limit, offset := clampInsightsPeopleLimit(rawLimit), max(rawOffset, 0)
	if offset >= len(full.Accounts) {
		cp.Accounts = make([]AdminInsightsPersonAccount, 0)
	} else {
		end := min(offset+limit, len(full.Accounts))
		cp.Accounts = append([]AdminInsightsPersonAccount(nil), full.Accounts[offset:end]...)
	}
	return &cp
}

func queryInsightsLiveUsers(ctx context.Context, tx pgx.Tx, libraryID int) (map[int]bool, map[string]bool, error) {
	liveUsers := make(map[int]bool)
	liveProfiles := make(map[string]bool)
	query := `SELECT DISTINCT s.user_id, COALESCE(s.profile_id, '') FROM playback_sessions_sync s`
	var args []any
	if libraryID > 0 {
		query = `
			SELECT DISTINCT s.user_id, COALESCE(s.profile_id, '')
			FROM playback_sessions_sync s
			LEFT JOIN media_files mf ON mf.id = s.media_file_id
			LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
			WHERE mf.media_folder_id = $1
			   OR (mf.id IS NOT NULL AND EXISTS (
					SELECT 1 FROM media_item_libraries mil
					WHERE mil.content_id = COALESCE(ep.series_id, mf.content_id)
					  AND mil.media_folder_id = $1
			   ))`
		args = append(args, libraryID)
	}
	rows, err := tx.Query(ctx, query, args...)
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

// insightsPeopleProfilesSQL groups the window's sessions by profile. A
// profile appears when it has any session in the window; its play counts may
// be zero when it only continued a viewing begun before the window.
func insightsPeopleProfilesSQL(with, counted string) string {
	return with + `,
		counted AS (
			SELECT vr.*, ` + insightsIsPlaySQL("vr") + ` AS is_play FROM vr WHERE ` + counted + `
		),
		grouped AS (
			SELECT
				user_id, profile_id,
				COUNT(*) FILTER (WHERE is_play)::bigint AS plays,
				COUNT(*)::bigint AS sessions,
				COUNT(*) FILTER (WHERE is_play AND NOT is_episode AND item_type = 'movie')::bigint AS movies,
				COUNT(*) FILTER (WHERE is_play AND is_episode)::bigint AS episodes,
				COALESCE(SUM(watch_sec), 0)::double precision AS watch_seconds,
				COUNT(*) FILTER (WHERE measured)::bigint AS measured_sessions,
				COUNT(*) FILTER (WHERE route_facts_recorded)::bigint AS route_facts_sessions,
				COUNT(*) FILTER (WHERE is_play AND ` + insightsDirectSQL("viewing_play_method") + `)::bigint AS direct_plays,
				COUNT(*) FILTER (WHERE is_play AND ` + insightsRemuxSQL("viewing_play_method") + `)::bigint AS remux_plays,
				COUNT(*) FILTER (WHERE is_play AND ` + insightsTranscodeSQL("viewing_play_method") + `)::bigint AS transcode_plays,
				COUNT(*) FILTER (WHERE is_play AND viewing_completed)::bigint AS completed_plays,
				MAX(ended_at) AS last_seen_at
			FROM counted
			GROUP BY user_id, profile_id
		)
		SELECT
			g.user_id, COALESCE(u.username, ''), COALESCE(u.role, ''),
			g.profile_id, COALESCE(pn.profile_name, g.profile_id),
			g.plays, g.sessions, g.movies, g.episodes, g.watch_seconds,
			g.measured_sessions, g.route_facts_sessions,
			g.direct_plays, g.remux_plays, g.transcode_plays, g.completed_plays,
			g.last_seen_at
		FROM grouped g
		LEFT JOIN users u ON u.id = g.user_id
		LEFT JOIN LATERAL (` + insightsProfileNameSQL("g") + `) pn ON TRUE
	`
}

// insightsProfileNameSQL reads a profile's latest recorded display name from
// history: profile names live in the per-user stores, not in Postgres.
func insightsProfileNameSQL(alias string) string {
	return `SELECT p.profile_name
		FROM admin_playback_history p
		WHERE p.user_id = ` + alias + `.user_id AND p.profile_id = ` + alias + `.profile_id AND p.profile_name <> ''
		ORDER BY p.ended_at DESC
		LIMIT 1`
}

func queryInsightsPeopleProfiles(
	ctx context.Context,
	tx pgx.Tx,
	scope insightsPlaysScope,
	liveUsers map[int]bool,
	liveProfiles map[string]bool,
	out *AdminInsightsPeople,
) (map[int]*AdminInsightsPersonAccount, error) {
	var args []any
	with, counted := scope.viewingRowsSQL(&args)
	rows, err := tx.Query(ctx, insightsPeopleProfilesSQL(with, counted), args...)
	if err != nil {
		return nil, fmt.Errorf("querying insights people: %w", err)
	}
	defer rows.Close()

	byUser := make(map[int]*AdminInsightsPersonAccount)
	for rows.Next() {
		var uid int
		var username, role, profileID, profileName string
		var plays, sessions, movies, episodes, measured, routeFacts, direct, remux, transcode, completed int64
		var watchSecF float64
		var lastSeen *time.Time
		if err := rows.Scan(
			&uid, &username, &role, &profileID, &profileName,
			&plays, &sessions, &movies, &episodes, &watchSecF, &measured, &routeFacts,
			&direct, &remux, &transcode, &completed, &lastSeen,
		); err != nil {
			return nil, fmt.Errorf("scanning insights person profile: %w", err)
		}
		lastSeen = normalizeTimePtrUTC(lastSeen)
		watchSec := int64(math.Round(watchSecF))

		out.Coverage.TotalSessions += sessions
		out.Coverage.MeasuredSessions += measured
		out.Coverage.RouteFactsSessions += routeFacts
		out.ActiveProfiles++
		out.TotalWatchSeconds += watchSec

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
		acc.Sessions += sessions
		acc.Movies += movies
		acc.Episodes += episodes
		acc.WatchSeconds += watchSec
		acc.MeasuredSessions += measured
		acc.DirectPlays += direct
		acc.RemuxPlays += remux
		acc.TranscodePlays += transcode
		acc.CompletedPlays += completed
		if lastSeen != nil && (acc.LastSeenAt == nil || lastSeen.After(*acc.LastSeenAt)) {
			acc.LastSeenAt = lastSeen
		}
		acc.Profiles = append(acc.Profiles, AdminInsightsPersonProfile{
			ProfileID: profileID, ProfileName: profileName, IsLive: liveProfiles[strconv.Itoa(uid)+":"+profileID],
			Plays: plays, Sessions: sessions, WatchSeconds: watchSec, MeasuredSessions: measured,
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
		acc.MeasuredShare = roundInsightsRate(acc.MeasuredSessions, acc.Sessions)
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
	case "", insightsHWAutoKey, insightsUnknownKey:
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
	if err := validateInsightsScope(q.AdminInsightsWindowQuery); err != nil {
		return nil, err
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
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, q.LibraryID, q.UserID, q.ProfileID)

	out := &AdminInsightsDevices{
		From: win.From, To: win.To, TZ: win.TZ,
		Coverage: AdminInsightsCoverage{
			OldestHistoryAt: oldestHist, OldestMeasuredAt: oldestMeas,
			OldestRouteFactsAt: oldestRoute, OldestMetricAt: oldestMetric,
		},
		Codecs: make([]string, 0), Matrix: make([]AdminInsightsClientMatrixRow, 0),
		AdaptationReasons: make([]AdminInsightsAdaptationReason, 0),
		HWAccelBreakdown:  make([]AdminInsightsLabeledCount, 0), ToneMapBreakdown: make([]AdminInsightsLabeledCount, 0),
		SubtitleBurnInCodecs: make([]AdminInsightsLabeledCount, 0),
		SourceResolutions:    make([]AdminInsightsLabeledCount, 0), DeliveredResolutions: make([]AdminInsightsLabeledCount, 0),
		ResolutionChanges: make([]AdminInsightsResolutionChange, 0),
	}

	if err := queryInsightsDevicesSummary(ctx, tx, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsClientCodecMatrix(ctx, tx, whereSQL, whereArgs, clampInsightsMinSessions(q.MinSessions), out); err != nil {
		return nil, err
	}
	if err := queryInsightsAdaptationReasons(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsDevicesBreakdowns(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsResolutions(ctx, tx, whereSQL, whereArgs, out); err != nil {
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
			COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+` AND h.route_facts_recorded AND NULLIF(LOWER(BTRIM(COALESCE(h.transcode_hw_accel, ''))), '') IS NOT NULL AND LOWER(BTRIM(h.transcode_hw_accel)) NOT IN ('none', 'software', 'cpu', 'auto', 'unknown'))::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+` AND h.route_facts_recorded AND LOWER(BTRIM(COALESCE(h.transcode_hw_accel, ''))) IN ('none', 'software', 'cpu'))::bigint,
			COUNT(*) FILTER (WHERE h.subtitle->>'delivery' = 'burn_in')::bigint,
			COUNT(*) FILTER (WHERE NULLIF(LOWER(BTRIM(COALESCE(h.tone_map_mode, ''))), '') IS NOT NULL)::bigint,
			COUNT(*) FILTER (WHERE LOWER(BTRIM(COALESCE(h.tone_map_mode, ''))) = 'hardware')::bigint,
			COUNT(*) FILTER (WHERE LOWER(BTRIM(COALESCE(h.tone_map_mode, ''))) = 'software')::bigint
		FROM admin_playback_history h
		LEFT JOIN media_files mf ON mf.id = COALESCE(h.effective_media_file_id, h.media_file_id)
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
		WHERE `+whereSQL, whereArgs...).Scan(
		&out.Summary.TotalSessions, &out.Coverage.MeasuredSessions, &out.Summary.RouteFactsSessions,
		&out.Summary.DirectSessions, &out.Summary.RemuxSessions, &out.Summary.TranscodeSessions,
		&out.Summary.HWTranscodeSessions, &out.Summary.SWTranscodeSessions,
		&out.Summary.SubtitleBurnInSessions, &out.Summary.HDRToneMapSessions,
		&out.Summary.HDRToneMapHWSessions, &out.Summary.HDRToneMapSWSessions,
	)
	if err != nil {
		return fmt.Errorf("querying insights devices summary: %w", err)
	}
	out.Summary.DirectRate = roundInsightsRate(out.Summary.DirectSessions, out.Summary.TotalSessions)
	out.Coverage.TotalSessions = out.Summary.TotalSessions
	out.Coverage.RouteFactsSessions = out.Summary.RouteFactsSessions
	return nil
}

func queryInsightsClientCodecMatrix(ctx context.Context, tx pgx.Tx, whereSQL string, whereArgs []any, minSessions int, out *AdminInsightsDevices) error {
	rows, err := tx.Query(ctx, `
		SELECT
			COALESCE(MIN(NULLIF(BTRIM(h.client_name), '')), 'Unknown') AS client_name,
			COALESCE(MIN(NULLIF(BTRIM(h.device_platform), '')), 'Unknown') AS device_platform,
			LOWER(COALESCE(NULLIF(BTRIM(mf.codec_video), ''), 'unknown')) AS source_codec,
			BOOL_OR(COALESCE(h.compat_origin, FALSE)) AS compat_origin,
			COUNT(*)::bigint AS sessions,
			COUNT(*) FILTER (WHERE `+insightsPlayDirectSQL+`)::bigint AS direct_sessions,
			COUNT(*) FILTER (WHERE `+insightsPlayRemuxSQL+`)::bigint AS remux_sessions,
			COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+`)::bigint AS transcode_sessions
		FROM admin_playback_history h
		LEFT JOIN media_files mf ON mf.id = COALESCE(h.effective_media_file_id, h.media_file_id)
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
		WHERE `+whereSQL+`
		GROUP BY
			LOWER(COALESCE(NULLIF(BTRIM(h.client_name), ''), 'unknown')),
			LOWER(COALESCE(NULLIF(BTRIM(h.device_platform), ''), 'unknown')),
			LOWER(COALESCE(NULLIF(BTRIM(mf.codec_video), ''), 'unknown'))
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
		var sessions, direct, remux, transcode int64
		if err := rows.Scan(&clientName, &platform, &codec, &compatOrigin, &sessions, &direct, &remux, &transcode); err != nil {
			return fmt.Errorf("scanning insights client codec matrix: %w", err)
		}
		k := matrixKey{client: strings.ToLower(clientName), platform: strings.ToLower(platform)}
		row := rowMap[k]
		if row == nil {
			row = &AdminInsightsClientMatrixRow{
				ClientName: clientName, DevicePlatform: platform,
				IsJellyfinClient: compatOrigin || isJellyfinEcosystemClient(clientName, ""),
				Cells:            make([]AdminInsightsClientCodecCell, 0, 4),
			}
			rowMap[k] = row
		} else {
			mergeMatrixRowIdentity(row, clientName, platform, compatOrigin)
		}
		row.TotalSessions += sessions
		row.DirectSessions += direct
		row.RemuxSessions += remux
		row.TranscodeSessions += transcode
		row.Cells = append(row.Cells, AdminInsightsClientCodecCell{
			SourceVideoCodec: codec, Sessions: sessions, DirectSessions: direct,
			RemuxSessions: remux, TranscodeSessions: transcode, DirectRate: roundInsightsRate(direct, sessions),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	codecTotals := make(map[string]int64)
	for _, row := range rowMap {
		if row.TotalSessions < int64(minSessions) {
			continue
		}
		row.DirectRate = roundInsightsRate(row.DirectSessions, row.TotalSessions)
		for _, cell := range row.Cells {
			codecTotals[cell.SourceVideoCodec] += cell.Sessions
		}
		out.Matrix = append(out.Matrix, *row)
	}
	orderClientCodecMatrix(out, codecTotals)
	return nil
}

func mergeMatrixRowIdentity(row *AdminInsightsClientMatrixRow, clientName, platform string, compatOrigin bool) {
	if clientName < row.ClientName {
		row.ClientName = clientName
	}
	if platform < row.DevicePlatform {
		row.DevicePlatform = platform
	}
	if compatOrigin {
		row.IsJellyfinClient = true
	}
}

func orderClientCodecMatrix(out *AdminInsightsDevices, codecTotals map[string]int64) {
	slices.SortFunc(out.Matrix, func(a, b AdminInsightsClientMatrixRow) int {
		if c := cmp.Compare(b.TotalSessions, a.TotalSessions); c != 0 {
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
	adaptedTotal := out.Summary.RemuxSessions + out.Summary.TranscodeSessions
	for reason, count := range reasonCounts {
		out.AdaptationReasons = append(out.AdaptationReasons, AdminInsightsAdaptationReason{
			Reason: reason, Sessions: count, ShareOfAdapted: roundInsightsRate(count, adaptedTotal),
		})
	}
	slices.SortFunc(out.AdaptationReasons, func(a, b AdminInsightsAdaptationReason) int {
		if c := cmp.Compare(b.Sessions, a.Sessions); c != 0 {
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

func sortedLabeledCounts(m map[string]int64) []AdminInsightsLabeledCount {
	res := make([]AdminInsightsLabeledCount, 0, len(m))
	for k, v := range m {
		res = append(res, AdminInsightsLabeledCount{Key: k, Sessions: v})
	}
	slices.SortFunc(res, func(a, b AdminInsightsLabeledCount) int {
		if a.Key == insightsUnknownKey && b.Key != insightsUnknownKey {
			return 1
		}
		if b.Key == insightsUnknownKey && a.Key != insightsUnknownKey {
			return -1
		}
		if c := cmp.Compare(b.Sessions, a.Sessions); c != 0 {
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
	fromDay, toDay, upperUTC := metricDaySpanForWindow(win, dbNow)
	pointIdx, apiBytes, apiPeak, err := queryInsightsBandwidthDaily(ctx, tx, fromDay, toDay, upperUTC, out)
	if err != nil {
		return nil, err
	}
	nodeSources, err := queryInsightsBandwidthNodes(ctx, tx, fromDay, toDay, upperUTC, pointIdx, out)
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

func queryInsightsBandwidthDaily(ctx context.Context, tx pgx.Tx, fromDay, toDay string, upperUTC time.Time, out *AdminInsightsBandwidth) (map[string]int, int64, int64, error) {
	rows, err := tx.Query(ctx, insightsCombinedDailyMetricsCTE+`
		SELECT
			to_char(day, 'YYYY-MM-DD') AS day_str,
			egress_bytes, download_egress_bytes, api_egress_bytes,
			peak_egress_kbps, peak_download_kbps, api_peak_egress_kbps, peak_egress_at
		FROM combined_daily
		ORDER BY day ASC
	`, fromDay, toDay, upperUTC, int64(dashmetrics.BytesPerKbpsMinute))
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

func queryInsightsBandwidthNodes(ctx context.Context, tx pgx.Tx, fromDay, toDay string, upperUTC time.Time, pointIdx map[string]int, out *AdminInsightsBandwidth) (map[int]*AdminInsightsEgressSource, error) {
	rows, err := tx.Query(ctx, `
		WITH archived_node_days AS (
			SELECT d.day, d.node_id, d.node_name, d.node_type, d.egress_bytes, d.peak_egress_kbps
			FROM dashboard_node_egress_daily d
			WHERE d.day >= $1::date AND d.day <= $2::date
		),
		node_daily AS (
			SELECT day, node_id, node_name, node_type, egress_bytes, peak_egress_kbps
			FROM archived_node_days
			UNION ALL
			SELECT
				(s.bucket AT TIME ZONE 'UTC')::date AS day,
				s.node_id,
				''::text AS node_name,
				NULL::text AS node_type,
				(COALESCE(SUM(s.egress_kbps), 0) * $4)::bigint AS egress_bytes,
				COALESCE(MAX(s.egress_kbps), 0)::bigint AS peak_egress_kbps
			FROM dashboard_node_egress_samples s
			WHERE s.bucket >= ($1::date::timestamp AT TIME ZONE 'UTC')
			  AND s.bucket < LEAST((($2::date + 1)::timestamp AT TIME ZONE 'UTC'), $3::timestamptz)
			  AND NOT EXISTS (
				SELECT 1 FROM archived_node_days a
				WHERE a.day = (s.bucket AT TIME ZONE 'UTC')::date AND a.node_id = s.node_id
			  )
			GROUP BY 1, 2
		)
		SELECT
			to_char(nd.day, 'YYYY-MM-DD') AS day_str,
			nd.node_id,
			COALESCE(
				NULLIF(BTRIM(sn.name), ''),
				NULLIF(BTRIM(nd.node_name), ''),
				CASE
					WHEN nd.node_id < 0 THEN 'Archived node ' || (-nd.node_id)::text
					ELSE 'Removed node ' || nd.node_id::text
				END
			) AS node_name,
			COALESCE(sn.type, nd.node_type),
			nd.egress_bytes,
			nd.peak_egress_kbps
		FROM node_daily nd
		LEFT JOIN stream_nodes sn ON nd.node_id > 0 AND sn.id = nd.node_id
		ORDER BY nd.day ASC, nd.node_id ASC
	`, fromDay, toDay, upperUTC, int64(dashmetrics.BytesPerKbpsMinute))
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
		} else {
			if src.NodeType == nil && nodeType != nil {
				src.NodeType = nodeType
			}
			if (strings.HasPrefix(src.Name, "Removed node ") || strings.HasPrefix(src.Name, "Archived node ")) &&
				!strings.HasPrefix(nodeName, "Removed node ") && !strings.HasPrefix(nodeName, "Archived node ") {
				src.Name = nodeName
			}
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
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, 0, 0, "")
	err := tx.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE h.stream_location = 'remote')::bigint,
			COUNT(*) FILTER (WHERE h.stream_location = 'local')::bigint,
			COUNT(*) FILTER (WHERE h.stream_location IS NULL OR h.stream_location NOT IN ('local', 'remote'))::bigint
		FROM admin_playback_history h
		WHERE `+whereSQL, whereArgs...).Scan(
		&out.Summary.RemoteSessions, &out.Summary.LocalSessions, &out.Summary.UnknownLocationSessions,
	)
	if err != nil {
		return fmt.Errorf("querying insights bandwidth stream location share: %w", err)
	}
	return nil
}
