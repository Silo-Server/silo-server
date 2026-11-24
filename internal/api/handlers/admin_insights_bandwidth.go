package handlers

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/dashmetrics"
)

func queryAdminInsightsBandwidth(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsBandwidthQuery) (*AdminInsightsBandwidth, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	ctx, tx, done, err := beginAdminHistoryRead(ctx, pool, "")
	if err != nil {
		return nil, fmt.Errorf("beginning insights bandwidth read: %w", err)
	}
	defer done()

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
