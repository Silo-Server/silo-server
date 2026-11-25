package handlers

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

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
	ctx, tx, done, err := beginAdminHistoryRead(ctx, pool, "")
	if err != nil {
		return nil, fmt.Errorf("beginning insights devices read: %w", err)
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
		TopTranscoders:       make([]AdminInsightsTranscoderRow, 0),
		SourceResolutions:    make([]AdminInsightsLabeledCount, 0), DeliveredResolutions: make([]AdminInsightsLabeledCount, 0),
		ResolutionChanges: make([]AdminInsightsResolutionChange, 0),
		BucketUnit:        win.BucketUnit, ClientVersions: make([]AdminInsightsClientVersionRow, 0),
		ProtocolBuckets: make([]AdminInsightsProtocolBucket, 0),
	}

	if err := queryInsightsDevicesSummaryAndBreakdowns(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsClientCodecMatrix(ctx, tx, q.LibraryID, whereSQL, whereArgs, clampInsightsMinSessions(q.MinSessions), out); err != nil {
		return nil, err
	}
	if err := queryInsightsAdaptationReasons(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsResolutions(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsClientVersions(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsProtocolBuckets(ctx, tx, win, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsSubtitleUsage(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsAudioUsage(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights devices read: %w", err)
	}
	return out, nil
}

// queryInsightsDevicesSummaryAndBreakdowns reads the Devices summary KPIs and
// the HW-accel, tone-map, and subtitle burn-in codec breakdowns in one scan of
// admin_playback_history.
func queryInsightsDevicesSummaryAndBreakdowns(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	rows, err := tx.Query(ctx, `
		SELECT
			CASE WHEN `+insightsPlayTranscodeSQL+` AND h.route_facts_recorded THEN COALESCE(h.transcode_hw_accel, '') END,
			NULLIF(LOWER(BTRIM(COALESCE(h.tone_map_mode, ''))), ''),
			CASE WHEN h.subtitle->>'delivery' = 'burn_in' THEN COALESCE(NULLIF(LOWER(BTRIM(h.subtitle->>'codec')), ''), 'unknown') END,
			COUNT(*)::bigint,
			COUNT(*) FILTER (WHERE h.measured)::bigint,
			COUNT(*) FILTER (WHERE h.route_facts_recorded)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayDirectSQL+`)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayRemuxSQL+`)::bigint,
			COUNT(*) FILTER (WHERE `+insightsPlayTranscodeSQL+`)::bigint
		FROM admin_playback_history h `+insightsLibraryJoinSQL(libraryID)+`
		WHERE `+whereSQL+`
		GROUP BY 1, 2, 3
	`, whereArgs...)
	if err != nil {
		return fmt.Errorf("querying insights devices summary and breakdowns: %w", err)
	}
	defer rows.Close()
	hwAccel := make(map[string]int64)
	toneMap := make(map[string]int64)
	burnIn := make(map[string]int64)
	for rows.Next() {
		var hw, tm, burn *string
		var total, measured, routeFacts, direct, remux, transcode int64
		if err := rows.Scan(&hw, &tm, &burn, &total, &measured, &routeFacts, &direct, &remux, &transcode); err != nil {
			return fmt.Errorf("scanning insights devices summary and breakdowns: %w", err)
		}
		out.Summary.TotalSessions += total
		out.Coverage.MeasuredSessions += measured
		out.Summary.RouteFactsSessions += routeFacts
		out.Summary.DirectSessions += direct
		out.Summary.RemuxSessions += remux
		out.Summary.TranscodeSessions += transcode
		if hw != nil {
			k := normalizeHWAccelKey(*hw)
			hwAccel[k] += total
			switch k {
			case insightsHWSoftwareKey:
				out.Summary.SWTranscodeSessions += total
			case insightsUnknownKey:
			default:
				out.Summary.HWTranscodeSessions += total
			}
		}
		if tm != nil {
			toneMap[*tm] += total
			out.Summary.HDRToneMapSessions += total
			switch *tm {
			case "hardware":
				out.Summary.HDRToneMapHWSessions += total
			case "software":
				out.Summary.HDRToneMapSWSessions += total
			}
		}
		if burn != nil {
			burnIn[*burn] += total
			out.Summary.SubtitleBurnInSessions += total
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out.Summary.DirectRate = roundInsightsRate(out.Summary.DirectSessions, out.Summary.TotalSessions)
	out.Coverage.TotalSessions = out.Summary.TotalSessions
	out.Coverage.RouteFactsSessions = out.Summary.RouteFactsSessions
	out.HWAccelBreakdown = sortedLabeledCounts(hwAccel)
	out.ToneMapBreakdown = sortedLabeledCounts(toneMap)
	out.SubtitleBurnInCodecs = sortedLabeledCounts(burnIn)
	return nil
}

// insightsMatrixMaxRows caps the client and platform pairs the matrix returns,
// the ones with the most sessions. Client names and platforms are what clients
// report, so their number is whatever the clients make it; the cap keeps one
// read's memory and response bounded however many identities history holds.
const insightsMatrixMaxRows = 200

func insightsClientCodecMatrixSQL(libraryID int, whereSQL string, whereArgs []any, minSessions int) (string, []any) {
	args := append(append([]any{}, whereArgs...), minSessions, insightsMatrixMaxRows, insightsTopTranscodersLimit)
	minParam, capParam, topParam := len(args)-2, len(args)-1, len(args)
	// Pre-group sessions by file before probing media_files so each unique file
	// is looked up once, and return only matrix rows (over the threshold, within
	// the cap) and top transcoders.
	query := fmt.Sprintf(`
		WITH per_file AS (
			SELECT
				COALESCE(h.effective_media_file_id, h.media_file_id) AS file_id,
				LOWER(COALESCE(NULLIF(BTRIM(h.client_name), ''), 'unknown')) AS client_key,
				LOWER(COALESCE(NULLIF(BTRIM(h.device_platform), ''), 'unknown')) AS platform_key,
				MIN(NULLIF(BTRIM(h.client_name), '')) AS client_name,
				MIN(NULLIF(BTRIM(h.device_platform), '')) AS device_platform,
				BOOL_OR(COALESCE(h.compat_origin, FALSE)) AS compat_origin,
				COUNT(*)::bigint AS sessions,
				COUNT(*) FILTER (WHERE %[1]s)::bigint AS direct_sessions,
				COUNT(*) FILTER (WHERE %[2]s)::bigint AS remux_sessions,
				COUNT(*) FILTER (WHERE %[3]s)::bigint AS transcode_sessions
			FROM admin_playback_history h %[8]s
			WHERE %[4]s
			GROUP BY 1, 2, 3
		),
		files AS (
			SELECT mf.id, LOWER(COALESCE(NULLIF(BTRIM(mf.codec_video), ''), 'unknown')) AS source_codec
			FROM (SELECT DISTINCT file_id FROM per_file WHERE file_id IS NOT NULL) f
			JOIN media_files mf ON mf.id = f.file_id
		),
		cells AS (
			SELECT
				pf.client_key,
				pf.platform_key,
				COALESCE(MIN(pf.client_name), 'Unknown') AS client_name,
				COALESCE(MIN(pf.device_platform), 'Unknown') AS device_platform,
				COALESCE(f.source_codec, 'unknown') AS source_codec,
				BOOL_OR(pf.compat_origin) AS compat_origin,
				SUM(pf.sessions)::bigint AS sessions,
				SUM(pf.direct_sessions)::bigint AS direct_sessions,
				SUM(pf.remux_sessions)::bigint AS remux_sessions,
				SUM(pf.transcode_sessions)::bigint AS transcode_sessions
			FROM per_file pf
			LEFT JOIN files f ON f.id = pf.file_id
			GROUP BY 1, 2, 5
		),
		pairs AS (
			SELECT client_key, platform_key,
				SUM(sessions) AS total, SUM(transcode_sessions) AS transcodes
			FROM cells
			GROUP BY 1, 2
		),
		ranked AS (
			SELECT client_key, platform_key,
				(total >= $%[5]d AND ROW_NUMBER() OVER (
					ORDER BY (total >= $%[5]d) DESC, total DESC, client_key, platform_key) <= $%[6]d) AS in_matrix,
				(transcodes > 0 AND ROW_NUMBER() OVER (
					ORDER BY transcodes DESC, total DESC, client_key, platform_key) <= $%[7]d) AS top_transcoder
			FROM pairs
		)
		SELECT c.client_name, c.device_platform, c.source_codec, c.compat_origin,
			c.sessions, c.direct_sessions, c.remux_sessions, c.transcode_sessions, r.in_matrix
		FROM cells c
		JOIN ranked r USING (client_key, platform_key)
		WHERE r.in_matrix OR r.top_transcoder
	`, insightsPlayDirectSQL, insightsPlayRemuxSQL, insightsPlayTranscodeSQL, whereSQL, minParam, capParam, topParam, insightsLibraryJoinSQL(libraryID))
	return query, args
}

func queryInsightsClientCodecMatrix(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, minSessions int, out *AdminInsightsDevices) error {
	query, args := insightsClientCodecMatrixSQL(libraryID, whereSQL, whereArgs, minSessions)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("querying insights client codec matrix: %w", err)
	}
	defer rows.Close()

	type matrixKey struct{ client, platform string }
	rowMap := make(map[matrixKey]*AdminInsightsClientMatrixRow)
	inMatrix := make(map[matrixKey]bool)
	for rows.Next() {
		var clientName, platform, codec string
		var compatOrigin, matrixRow bool
		var sessions, direct, remux, transcode int64
		if err := rows.Scan(&clientName, &platform, &codec, &compatOrigin, &sessions, &direct, &remux, &transcode, &matrixRow); err != nil {
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
		inMatrix[k] = matrixRow
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

	out.TopTranscoders = topInsightsTranscoders(slices.Collect(maps.Values(rowMap)))
	codecTotals := make(map[string]int64)
	for k, row := range rowMap {
		if !inMatrix[k] {
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

// insightsTopTranscodersLimit caps the Devices tab's transcoder ranking.
const insightsTopTranscodersLimit = 10

// topInsightsTranscoders ranks clients by transcoded sessions. It ignores the
// matrix's minimum-session threshold, which keeps direct-play rates from
// resting on a handful of sessions; a client that transcodes still belongs in
// this ranking however few sessions it has.
func topInsightsTranscoders(rows []*AdminInsightsClientMatrixRow) []AdminInsightsTranscoderRow {
	out := make([]AdminInsightsTranscoderRow, 0, insightsTopTranscodersLimit)
	for _, row := range rows {
		if row.TranscodeSessions > 0 {
			out = append(out, AdminInsightsTranscoderRow{
				ClientName: row.ClientName, DevicePlatform: row.DevicePlatform, IsJellyfinClient: row.IsJellyfinClient,
				TotalSessions: row.TotalSessions, TranscodeSessions: row.TranscodeSessions,
			})
		}
	}
	slices.SortFunc(out, func(a, b AdminInsightsTranscoderRow) int {
		if c := cmp.Compare(b.TranscodeSessions, a.TranscodeSessions); c != 0 {
			return c
		}
		if c := cmp.Compare(b.TotalSessions, a.TotalSessions); c != 0 {
			return c
		}
		if c := cmp.Compare(strings.ToLower(a.ClientName), strings.ToLower(b.ClientName)); c != 0 {
			return c
		}
		return cmp.Compare(strings.ToLower(a.DevicePlatform), strings.ToLower(b.DevicePlatform))
	})
	if len(out) > insightsTopTranscodersLimit {
		out = out[:insightsTopTranscodersLimit]
	}
	return out
}
