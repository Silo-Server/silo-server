package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// AdminPlaybackHistoryFilter narrows the finalized playback log. Zero values
// mean "no filter".
type AdminPlaybackHistoryFilter struct {
	UserID      int
	ProfileID   string
	MediaItemID string
	// Completed is nil for every attempt, otherwise the exact flag.
	Completed *bool
	// EndedAfter keeps attempts that ended at or after the instant; nil
	// keeps every attempt.
	EndedAfter *time.Time
	// EndedBefore keeps attempts that ended strictly before the instant, so
	// adjacent windows sharing a bound never list an attempt twice; nil keeps
	// every attempt.
	EndedBefore *time.Time `json:",omitempty"`
	// StartedAfter / StartedBefore window attempts by started_at (inclusive
	// below, exclusive above) so Insights drill-downs match the exact Insights
	// time window.
	StartedAfter  *time.Time `json:",omitempty"`
	StartedBefore *time.Time `json:",omitempty"`
	// Rich analytics filters (all optional, additive).
	Search           string `json:",omitempty"`
	PlayMethod       string `json:",omitempty"`
	DevicePlatform   string `json:",omitempty"`
	ClientName       string `json:",omitempty"`
	SourceVideoCodec string `json:",omitempty"`
	SubtitleBurnIn   *bool  `json:",omitempty"`
	StreamLocation   string `json:",omitempty"`
	AdaptationReason string `json:",omitempty"`
	LibraryID        int    `json:",omitempty"`
	// SessionID limits the page to one session's row. Internal: a lifecycle
	// event reads the row its end recorded.
	SessionID string `json:",omitempty"`
}

// AdminPlaybackHistoryPageKey is the keyset position of the last row a page
// emitted: the log is ordered newest-ended first with the unique session id
// breaking ties, so the next page resumes strictly after it.
type AdminPlaybackHistoryPageKey struct {
	EndedAt   time.Time `json:"ended_at"`
	SessionID string    `json:"session_id"`
}

// AdminPlaybackHistoryRow is one finalized playback attempt as an
// administrator sees it.
type AdminPlaybackHistoryRow struct {
	SessionID   string
	UserID      int
	Username    string
	ProfileID   string
	ProfileName string
	MediaItemID string
	MediaFileID int
	MediaTitle  string
	MediaType   string
	PlayMethod  string
	StartedAt   time.Time
	EndedAt     time.Time
	// EndPositionSeconds is where the play ended (the watched_seconds column).
	EndPositionSeconds float64
	DurationSeconds    *float64
	Completed          bool
	// PlayedSeconds is the measured watch time (see playback.WatchTime); nil
	// when the play has no measurement, which includes every row written
	// before it was recorded. StartPositionSeconds, PausedSeconds and the
	// measured window come from the same measurement and are nil with it.
	PlayedSeconds        *float64
	Measured             bool
	StartPositionSeconds *float64
	PausedSeconds        *float64
	MeasuredSince        *time.Time
	MeasuredUntil        *time.Time
	// ClientIP is the stored client address; nil when none was recorded.
	ClientIP *string
	// The play's device, client, stream decisions and end reason; nil when
	// unknown, which includes every row written before they were recorded.
	DeviceID, DevicePlatform     *string
	ClientName, ClientVersion    *string
	VideoDecision, AudioDecision *string
	EndReason                    *string
	// LifecycleSeq is the sequence number of the end the row records; nil
	// on rows recorded before lifecycle numbers existed.
	LifecycleSeq *int64
	// SeriesTitle, SeasonNumber and EpisodeNumber describe an episode's
	// place in its series; empty and nil for anything else.
	SeriesTitle   string
	SeasonNumber  *int
	EpisodeNumber *int
	// Subtitle is the subtitle the play ended on; nil when it showed none or
	// the row predates the record.
	Subtitle *playback.SessionSubtitle
	// Delivered route facts captured on finalization. Nil on pre-upgrade rows
	// or when the session did not use that component.
	TargetVideoCodec         *string
	TargetResolution         *string
	TargetAudioCodec         *string
	TargetAudioChannels      *int
	OutputContainer          *string
	OutputProtocol           *string
	TargetBitrateKbps        *int
	TranscodeHWAccel         *string
	ToneMapMode              *string
	AdaptationReasons        []string
	ReportingNode            *string
	StreamLocation           *string
	CompatOrigin             *bool
	IsJellyfinClient         bool
	RoutingExecution         *string
	RoutingExecutionNodeID   *int
	RoutingExecutionNodeName *string
	RoutingEgress            *string
	RoutingEgressNodeID      *int
	RoutingEgressNodeName    *string
	RouteFactsRecorded       bool
	// Source file facts joined from media_files / media_folders (nil when the
	// file is no longer in the catalog or lacked probe metadata).
	SourceVideoCodec     *string
	SourceResolution     *string
	SourceAudioCodec     *string
	SourceAudioChannels  *int
	SourceContainer      *string
	SourceBitrateKbps    *int
	SourceHDR            *bool
	SourceVideoRangeType *string
	LibraryID            *int
	LibraryName          *string
}

// AdminPlaybackHistoryPage is one keyset page plus whether more rows follow.
type AdminPlaybackHistoryPage struct {
	Items   []AdminPlaybackHistoryRow
	HasMore bool
}

const (
	legacyPlayMethodDirect = "direct_play"
	legacyPlayMethodRemux  = "direct_stream"
	insightsUnknownKey     = "unknown"
)

func escapeHistoryLike(raw string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(raw)
}

func buildAdminPlaybackHistoryWhere(filter AdminPlaybackHistoryFilter, after *AdminPlaybackHistoryPageKey) ([]string, []any) {
	var conditions []string
	var args []any
	add := func(sql string, value any) {
		args = append(args, value)
		conditions = append(conditions, sql+"$"+strconv.Itoa(len(args)))
	}
	if filter.UserID != 0 {
		add("h.user_id = ", filter.UserID)
	}
	if filter.ProfileID != "" {
		add("h.profile_id = ", filter.ProfileID)
	}
	if filter.MediaItemID != "" {
		add("h.media_item_id = ", filter.MediaItemID)
	}
	if filter.Completed != nil {
		add("h.completed = ", *filter.Completed)
	}
	if filter.EndedAfter != nil {
		add("h.ended_at >= ", *filter.EndedAfter)
	}
	if filter.EndedBefore != nil {
		add("h.ended_at < ", *filter.EndedBefore)
	}
	if filter.StartedAfter != nil {
		add("h.started_at >= ", *filter.StartedAfter)
	}
	if filter.StartedBefore != nil {
		add("h.started_at < ", *filter.StartedBefore)
	}
	if filter.PlayMethod != "" {
		switch strings.ToLower(strings.TrimSpace(filter.PlayMethod)) {
		case string(playback.PlayDirect), legacyPlayMethodDirect:
			conditions = append(conditions, "h.play_method IN ('direct', 'direct_play')")
		case string(playback.PlayRemux), legacyPlayMethodRemux:
			conditions = append(conditions, "h.play_method IN ('remux', 'direct_stream')")
		case string(playback.PlayTranscode):
			conditions = append(conditions, "h.play_method = 'transcode'")
		default:
			add("h.play_method = ", filter.PlayMethod)
		}
	}
	appendOptionalTextFilter := func(val, col string) {
		if val == "" {
			return
		}
		if strings.EqualFold(val, insightsUnknownKey) {
			conditions = append(conditions, "NULLIF(BTRIM(COALESCE("+col+", '')), '') IS NULL")
			return
		}
		args = append(args, val)
		conditions = append(conditions, fmt.Sprintf("LOWER(COALESCE(%s, '')) = LOWER($%d)", col, len(args)))
	}
	appendOptionalTextFilter(filter.DevicePlatform, "h.device_platform")
	appendOptionalTextFilter(filter.ClientName, "h.client_name")
	appendOptionalTextFilter(filter.SourceVideoCodec, "mf.codec_video")
	if filter.SubtitleBurnIn != nil {
		if *filter.SubtitleBurnIn {
			conditions = append(conditions, "h.subtitle->>'delivery' = 'burn_in'")
		} else {
			conditions = append(conditions, "COALESCE(h.subtitle->>'delivery', '') <> 'burn_in'")
		}
	}
	if filter.StreamLocation != "" {
		add("h.stream_location = ", strings.ToLower(strings.TrimSpace(filter.StreamLocation)))
	}
	if filter.AdaptationReason != "" {
		args = append(args, filter.AdaptationReason)
		conditions = append(conditions, fmt.Sprintf("$%d = ANY(h.adaptation_reasons)", len(args)))
	}
	if filter.LibraryID > 0 {
		args = append(args, filter.LibraryID)
		p := len(args)
		conditions = append(conditions, fmt.Sprintf(`(mf.media_folder_id = $%d OR (mf.id IS NULL AND EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = COALESCE(ep.series_id, h.media_item_id) AND mil.media_folder_id = $%d)))`, p, p))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		args = append(args, "%"+escapeHistoryLike(search)+"%")
		p := len(args)
		conditions = append(conditions, fmt.Sprintf(`(
			COALESCE(ep.title, mi.title, '') ILIKE $%d ESCAPE '\'
			OR COALESCE(sm.title, '') ILIKE $%d ESCAPE '\'
			OR COALESCE(u.username, '') ILIKE $%d ESCAPE '\'
			OR COALESCE(h.profile_name, '') ILIKE $%d ESCAPE '\'
			OR COALESCE(h.client_name, '') ILIKE $%d ESCAPE '\'
			OR COALESCE(h.device_platform, '') ILIKE $%d ESCAPE '\'
			OR COALESCE(host(h.client_ip), '') ILIKE $%d ESCAPE '\'
			OR h.session_id ILIKE $%d ESCAPE '\'
		)`, p, p, p, p, p, p, p, p))
	}
	if filter.SessionID != "" {
		add("h.session_id = ", filter.SessionID)
	}
	if after != nil {
		args = append(args, after.EndedAt, after.SessionID)
		conditions = append(conditions, fmt.Sprintf("(h.ended_at, h.session_id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	return conditions, args
}

const adminPlaybackHistorySelectSQL = `
	SELECT
		h.session_id,
		h.user_id,
		COALESCE(u.username, ''),
		h.profile_id,
		COALESCE(NULLIF(h.profile_name, ''), h.profile_id),
		h.media_item_id,
		h.media_file_id,
		COALESCE(ep.title, mi.title, ''),
		COALESCE(CASE WHEN ep.content_id IS NOT NULL THEN 'episode' ELSE mi.type END, ''),
		h.play_method,
		h.started_at,
		h.ended_at,
		h.watched_seconds,
		h.duration_seconds,
		h.completed,
		CASE WHEN h.measured THEN h.played_seconds END,
		h.measured,
		CASE WHEN h.measured THEN h.start_position_seconds END,
		CASE WHEN h.measured THEN h.paused_seconds END,
		CASE WHEN h.measured THEN h.measured_since END,
		CASE WHEN h.measured THEN h.measured_until END,
		host(h.client_ip),
		h.device_id,
		h.device_platform,
		h.client_name,
		h.client_version,
		h.video_decision,
		h.audio_decision,
		h.end_reason,
		h.lifecycle_seq,
		COALESCE(sm.title, ''),
		ep.season_number,
		ep.episode_number,
		h.subtitle,
		h.target_video_codec,
		h.target_resolution,
		h.target_audio_codec,
		h.target_audio_channels,
		h.output_container,
		h.output_protocol,
		h.target_bitrate_kbps,
		h.transcode_hw_accel,
		h.tone_map_mode,
		h.adaptation_reasons,
		h.reporting_node,
		h.stream_location,
		h.compat_origin,
		h.routing_execution,
		h.routing_execution_node_id,
		NULLIF(BTRIM(COALESCE(exec_node.name, '')), ''),
		h.routing_egress,
		h.routing_egress_node_id,
		NULLIF(BTRIM(COALESCE(egress_node.name, '')), ''),
		h.route_facts_recorded,
		NULLIF(BTRIM(COALESCE(mf.codec_video, '')), ''),
		NULLIF(BTRIM(COALESCE(mf.resolution, '')), ''),
		NULLIF(BTRIM(COALESCE(mf.codec_audio, '')), ''),
		mf.audio_channels,
		NULLIF(BTRIM(COALESCE(mf.container, '')), ''),
		mf.bitrate,
		mf.hdr,
		NULLIF(BTRIM(COALESCE(mf.video_tracks->0->>'video_range_type', '')), ''),
		mf.media_folder_id::integer,
		NULLIF(BTRIM(COALESCE(lib.name, '')), '')
	FROM admin_playback_history h
	LEFT JOIN users u ON u.id = h.user_id
	LEFT JOIN media_items mi ON mi.content_id = h.media_item_id
	LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
	LEFT JOIN media_items sm ON sm.content_id = ep.series_id
	LEFT JOIN media_files mf ON mf.id = h.media_file_id
	LEFT JOIN media_folders lib ON lib.id = mf.media_folder_id
	LEFT JOIN stream_nodes exec_node ON exec_node.id = h.routing_execution_node_id
	LEFT JOIN stream_nodes egress_node ON egress_node.id = h.routing_egress_node_id`

func scanAdminPlaybackHistoryRow(rows pgx.Rows) (AdminPlaybackHistoryRow, error) {
	var row AdminPlaybackHistoryRow
	if err := rows.Scan(
		&row.SessionID, &row.UserID, &row.Username, &row.ProfileID, &row.ProfileName,
		&row.MediaItemID, &row.MediaFileID, &row.MediaTitle, &row.MediaType, &row.PlayMethod,
		&row.StartedAt, &row.EndedAt, &row.EndPositionSeconds, &row.DurationSeconds, &row.Completed,
		&row.PlayedSeconds, &row.Measured, &row.StartPositionSeconds, &row.PausedSeconds,
		&row.MeasuredSince, &row.MeasuredUntil, &row.ClientIP,
		&row.DeviceID, &row.DevicePlatform, &row.ClientName, &row.ClientVersion,
		&row.VideoDecision, &row.AudioDecision, &row.EndReason, &row.LifecycleSeq,
		&row.SeriesTitle, &row.SeasonNumber, &row.EpisodeNumber, &row.Subtitle,
		&row.TargetVideoCodec, &row.TargetResolution, &row.TargetAudioCodec,
		&row.TargetAudioChannels, &row.OutputContainer, &row.OutputProtocol,
		&row.TargetBitrateKbps, &row.TranscodeHWAccel, &row.ToneMapMode,
		&row.AdaptationReasons, &row.ReportingNode, &row.StreamLocation,
		&row.CompatOrigin, &row.RoutingExecution, &row.RoutingExecutionNodeID,
		&row.RoutingExecutionNodeName, &row.RoutingEgress, &row.RoutingEgressNodeID,
		&row.RoutingEgressNodeName, &row.RouteFactsRecorded,
		&row.SourceVideoCodec, &row.SourceResolution, &row.SourceAudioCodec,
		&row.SourceAudioChannels, &row.SourceContainer, &row.SourceBitrateKbps,
		&row.SourceHDR, &row.SourceVideoRangeType, &row.LibraryID, &row.LibraryName,
	); err != nil {
		return row, err
	}
	if row.PlayMethod == string(playback.PlayDirect) || row.PlayMethod == legacyPlayMethodDirect {
		row.AdaptationReasons = nil
	} else {
		row.AdaptationReasons = playback.NormalizeAdaptationReasons(row.AdaptationReasons)
	}
	clientName := ""
	if row.ClientName != nil {
		clientName = *row.ClientName
	}
	row.IsJellyfinClient = (row.CompatOrigin != nil && *row.CompatOrigin) || isJellyfinEcosystemClient(clientName, "")
	return row, nil
}

// ListAdminPlaybackHistoryPage reads one consistent page of the finalized
// playback log, newest ended first. It is the seam v2 listAdminPlaybackHistory
// uses; v1 keeps its offset projection over the same table.
func (h *AdminHandler) ListAdminPlaybackHistoryPage(ctx context.Context, filter AdminPlaybackHistoryFilter, after *AdminPlaybackHistoryPageKey, limit int) (AdminPlaybackHistoryPage, error) {
	var out AdminPlaybackHistoryPage
	if h.pool == nil {
		return out, errors.New("playback history database unavailable")
	}
	if limit < 1 || limit > 200 {
		return out, errors.New("invalid playback history page limit")
	}
	conditions, args := buildAdminPlaybackHistoryWhere(filter, after)
	args = append(args, limit+1)
	query := adminPlaybackHistorySelectSQL
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY h.ended_at DESC, h.session_id DESC LIMIT $" + strconv.Itoa(len(args))

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return out, err
	}
	out.Items = make([]AdminPlaybackHistoryRow, 0)
	for rows.Next() {
		row, scanErr := scanAdminPlaybackHistoryRow(rows)
		if scanErr != nil {
			rows.Close()
			return out, scanErr
		}
		out.Items = append(out.Items, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.HasMore = true
		out.Items = out.Items[:limit]
	}
	if err = tx.Commit(ctx); err != nil {
		return AdminPlaybackHistoryPage{}, err
	}
	return out, nil
}

// ReadAdminPlaybackHistoryEntry reads one session's admin history row as the
// history list projects it; nil when the session has none.
func (h *AdminHandler) ReadAdminPlaybackHistoryEntry(ctx context.Context, sessionID string) (*AdminPlaybackHistoryRow, error) {
	page, err := h.ListAdminPlaybackHistoryPage(ctx, AdminPlaybackHistoryFilter{SessionID: sessionID}, nil, 1)
	if err != nil || len(page.Items) == 0 {
		return nil, err
	}
	return &page.Items[0], nil
}
