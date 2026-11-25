package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// PlaybackEndReason is why a playback attempt ended. The empty reason marks a
// server teardown the client can recover from by rebuilding the session under
// the same id (an encoder exit, a withdrawn plan); it records no reason.
type PlaybackEndReason string

const (
	// PlaybackEndStopped: the viewer's client stopped the play.
	PlaybackEndStopped PlaybackEndReason = "stopped"
	// PlaybackEndTerminated: an administrator stopped or terminated the play.
	PlaybackEndTerminated PlaybackEndReason = "terminated"
	// PlaybackEndExpired: the liveness reaper ended the play after its client
	// went quiet.
	PlaybackEndExpired PlaybackEndReason = "expired"
	// PlaybackEndAborted: the server abandoned the play, because its media
	// went missing or a replacement transport failed to commit.
	PlaybackEndAborted PlaybackEndReason = "aborted"
	// PlaybackEndReplaced: a Jellyfin client switched play method or
	// version, and the play continues under a new session.
	PlaybackEndReplaced PlaybackEndReason = playback.EndReasonReplaced
)

// PlaybackEndReasons lists every reason in its published order.
var PlaybackEndReasons = []PlaybackEndReason{PlaybackEndStopped, PlaybackEndTerminated, PlaybackEndExpired, PlaybackEndAborted, PlaybackEndReplaced}

// userInitiated reports a stop someone asked for, the viewer or an
// administrator. It retires the session for good: its recovery card is
// deleted and its stream tokens are denied everywhere.
func (r PlaybackEndReason) userInitiated() bool {
	return r == PlaybackEndStopped || r == PlaybackEndTerminated
}

// AdminPlaybackHistoryEntry is the finalized admin-facing record for a
// playback attempt.
type AdminPlaybackHistoryEntry struct {
	SessionID            string
	UserID               int
	ProfileID            string
	ProfileName          string
	MediaItemID          string
	MediaFileID          int
	EffectiveMediaFileID int
	AudioTrackIndex      *int
	PlayMethod           string
	StartedAt            string
	EndedAt              string
	// EndPositionSeconds is where the play ended, stored in the
	// watched_seconds column.
	EndPositionSeconds float64
	DurationSeconds    *float64
	Completed          bool
	ClientIP           string
	// Watch is the session's measured watch time. EndPositionSeconds is
	// where the play ended, not how much of it was watched.
	Watch playback.WatchTime
	// DeviceID, DevicePlatform, ClientName, ClientVersion, VideoDecision and
	// AudioDecision describe the play itself, so the first finalization that
	// knows one records it and later ones never change it. Empty is unknown.
	DeviceID, DevicePlatform     string
	ClientName, ClientVersion    string
	VideoDecision, AudioDecision string
	// EndReason is why this finalization ended the attempt; empty records
	// none.
	EndReason PlaybackEndReason
	// Subtitle is the subtitle the session's plan delivered when the play
	// ended; nil when it showed none. The one known later (SubtitleAt)
	// decides it, whatever position the play ended at: a play can seek
	// back, switch subtitles and stop before where an earlier end left it.
	Subtitle *playback.SessionSubtitle
	// Delivered route facts captured from the session when it finalized.
	// RouteFactsRecorded distinguishes post-upgrade direct-play rows (whose
	// transcode columns are legitimately NULL) from pre-upgrade rows.
	TargetVideoCodec         string
	TargetResolution         string
	TargetAudioCodec         string
	TargetAudioChannels      int
	OutputContainer          string
	OutputProtocol           string
	TargetBitrateKbps        int
	TranscodeHWAccel         string
	ToneMapMode              string
	AdaptationReasons        []string
	ReportingNode            string
	StreamLocation           string
	CompatOrigin             bool
	RoutingExecution         string
	RoutingExecutionNodeID   int
	RoutingExecutionNodeName string
	RoutingEgress            string
	RoutingEgressNodeID      int
	RoutingEgressNodeName    string
	RouteFactsRecorded       bool
	// SubtitleAt is when Subtitle was last known to be showing, when that
	// is before EndedAt: a sweep ending a play whose attempt is gone only
	// knows the subtitle its live or start row last synced. Zero is EndedAt.
	SubtitleAt time.Time
	// RouteFactsAt is when the route facts were last known, when that is
	// before EndedAt; a sweep reads them from the same row as the subtitle.
	// Zero is EndedAt.
	RouteFactsAt time.Time
}

// subtitleAt is when the entry's subtitle was last known to be showing.
func (entry AdminPlaybackHistoryEntry) subtitleAt() string {
	if entry.SubtitleAt.IsZero() {
		return entry.EndedAt
	}
	return entry.SubtitleAt.UTC().Format(time.RFC3339Nano)
}

// routeFactsAt is when the entry's route facts were last known.
func (entry AdminPlaybackHistoryEntry) routeFactsAt() string {
	if entry.RouteFactsAt.IsZero() {
		return entry.EndedAt
	}
	return entry.RouteFactsAt.UTC().Format(time.RFC3339Nano)
}

// PlaybackAdminStore persists finalized playback history and manages the
// shared active-session sync rows used by admin monitoring.
//
// RecordHistory keeps one row per session. A session finalized again (copies
// on several replicas, or one resumed after expiry) updates its row only when
// it reached further, so the row holds the furthest position whichever
// finalization lands first. Completion, once any copy records it, stays.
// Measured watch time is kept separately. Copies that measured the same
// stretch overlap in time, and the one that received the play's progress
// reports holds the largest counters, so each counter keeps its largest
// value whatever the position did. A copy whose measurement starts after the
// row's ended (a session rebuilt under the same id after it was finalized)
// measured a later stretch, so its counters are added.
//
// The device, client and stream decisions are filled once: a finalization
// records any the row lacks, whether or not it advanced the counters, and
// never replaces one. The end reason is the latest finalization's: one that
// ended no earlier than the finalization that recorded the current reason
// replaces it, and one without a reason leaves it. The subtitle is ordered
// the same way, on its own. Both orders are kept apart from ended_at, which
// only moves when a finalization gets further.
type PlaybackAdminStore interface {
	RecordHistory(ctx context.Context, entry AdminPlaybackHistoryEntry) error
	DeleteSession(ctx context.Context, sessionID string) error
}

// PGPlaybackAdminStore stores admin playback data in PostgreSQL.
type PGPlaybackAdminStore struct {
	pool      *pgxpool.Pool
	eventsHub *evt.Hub
}

// NewPGPlaybackAdminStore creates a PostgreSQL-backed admin playback store.
func NewPGPlaybackAdminStore(pool *pgxpool.Pool, eventsHub *evt.Hub) *PGPlaybackAdminStore {
	return &PGPlaybackAdminStore{pool: pool, eventsHub: eventsHub}
}

func (s *PGPlaybackAdminStore) RecordHistory(ctx context.Context, entry AdminPlaybackHistoryEntry) error {
	if s == nil || s.pool == nil {
		return nil
	}
	if err := s.pool.SendBatch(ctx, historyBatch(entry)).Close(); err != nil {
		return fmt.Errorf("recording playback history: %w", err)
	}
	return nil
}

// historyBatch is RecordHistory's statements, which run as one transaction.
func historyBatch(entry AdminPlaybackHistoryEntry) *pgx.Batch {
	var duration any
	if entry.DurationSeconds != nil {
		duration = *entry.DurationSeconds
	}

	// laterStretch: this copy began measuring after the row's measurement
	// ended, so it watched a different stretch of the play.
	const laterStretch = `(EXCLUDED.measured AND admin_playback_history.measured
		AND EXCLUDED.measured_since > admin_playback_history.measured_until)`
	// watchAdvanced: this copy knows more of the play than the row. Only such
	// a copy moves ended_at; a route snapshot alone does not, so a sweep that
	// ends a recorded play later keeps the recorded end.
	const watchAdvanced = `(EXCLUDED.watched_seconds > admin_playback_history.watched_seconds
		OR EXCLUDED.played_seconds > admin_playback_history.played_seconds
		OR EXCLUDED.paused_seconds > admin_playback_history.paused_seconds
		OR (EXCLUDED.measured AND NOT admin_playback_history.measured)
		OR (EXCLUDED.completed AND NOT admin_playback_history.completed)
		OR ` + laterStretch + `)`
	// routeSnapshotReplace: a finalization carrying recorded route facts
	// replaces the route snapshot atomically when the existing row has none or
	// its facts are at least as recent as the row's (route_facts_at, or
	// ended_at for a row recorded before that column). Replacing the whole
	// tuple avoids mixing cleared node IDs or obsolete transcode targets from
	// an earlier plan with a later replan.
	const rowRouteFactsAt = `COALESCE(admin_playback_history.route_facts_at, admin_playback_history.ended_at)`
	const routeSnapshotReplace = `(EXCLUDED.route_facts_recorded AND (NOT admin_playback_history.route_facts_recorded OR EXCLUDED.route_facts_at >= ` + rowRouteFactsAt + `))`
	batch := &pgx.Batch{}
	batch.Queue(`
		INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, profile_name, media_item_id, media_file_id,
			 play_method, started_at, ended_at, watched_seconds, duration_seconds, completed, client_ip,
			 start_position_seconds, played_seconds, paused_seconds, measured, measured_since, measured_until,
			 device_id, device_platform, client_name, client_version, video_decision, audio_decision,
			 end_reason, end_reason_at, subtitle,
			 target_video_codec, target_resolution, target_audio_codec, target_audio_channels,
			 output_container, output_protocol, target_bitrate_kbps, transcode_hw_accel, tone_map_mode,
			 adaptation_reasons, reporting_node, stream_location, compat_origin,
			 routing_execution, routing_execution_node_id, routing_egress, routing_egress_node_id,
			 route_facts_recorded, effective_media_file_id, audio_track_index,
			 routing_execution_node_name, routing_egress_node_name, subtitle_at, route_facts_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::inet, $14, $15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24, $25, $26::text, CASE WHEN $26::text IS NOT NULL THEN $9::timestamptz END, $27,
			$28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42, $43, $44, $45,
			$46, $47,
			COALESCE(NULLIF(BTRIM($48::text), ''), (SELECT NULLIF(BTRIM(name), '') FROM stream_nodes WHERE id = $42)),
			COALESCE(NULLIF(BTRIM($49::text), ''), (SELECT NULLIF(BTRIM(name), '') FROM stream_nodes WHERE id = $44)),
			$50::timestamptz, CASE WHEN $45 THEN $51::timestamptz END)
		ON CONFLICT (session_id) DO UPDATE SET
			play_method = CASE WHEN `+routeSnapshotReplace+` AND EXCLUDED.play_method <> ''
				THEN EXCLUDED.play_method ELSE admin_playback_history.play_method END,
			video_decision = CASE WHEN `+routeSnapshotReplace+`
				THEN EXCLUDED.video_decision ELSE admin_playback_history.video_decision END,
			audio_decision = CASE WHEN `+routeSnapshotReplace+`
				THEN EXCLUDED.audio_decision ELSE admin_playback_history.audio_decision END,
			effective_media_file_id = CASE WHEN `+routeSnapshotReplace+`
				THEN EXCLUDED.effective_media_file_id ELSE admin_playback_history.effective_media_file_id END,
			audio_track_index = CASE WHEN `+routeSnapshotReplace+`
				THEN EXCLUDED.audio_track_index ELSE admin_playback_history.audio_track_index END,
			watched_seconds = GREATEST(admin_playback_history.watched_seconds, EXCLUDED.watched_seconds),
			duration_seconds = CASE WHEN EXCLUDED.watched_seconds > admin_playback_history.watched_seconds
				THEN COALESCE(EXCLUDED.duration_seconds, admin_playback_history.duration_seconds)
				ELSE admin_playback_history.duration_seconds END,
			completed = admin_playback_history.completed OR EXCLUDED.completed,
			-- A row recorded before subtitle_at existed ordered its subtitle
			-- by ended_at; keep that order before ended_at moves.
			subtitle_at = COALESCE(admin_playback_history.subtitle_at, admin_playback_history.ended_at),
			ended_at = CASE WHEN `+watchAdvanced+`
				THEN GREATEST(admin_playback_history.ended_at, EXCLUDED.ended_at) ELSE admin_playback_history.ended_at END,
			start_position_seconds = CASE
				WHEN NOT admin_playback_history.measured THEN EXCLUDED.start_position_seconds
				WHEN NOT EXCLUDED.measured OR `+laterStretch+` THEN admin_playback_history.start_position_seconds
				ELSE LEAST(admin_playback_history.start_position_seconds, EXCLUDED.start_position_seconds) END,
			played_seconds = CASE WHEN `+laterStretch+`
				THEN admin_playback_history.played_seconds + EXCLUDED.played_seconds
				ELSE GREATEST(admin_playback_history.played_seconds, EXCLUDED.played_seconds) END,
			paused_seconds = CASE WHEN `+laterStretch+`
				THEN admin_playback_history.paused_seconds + EXCLUDED.paused_seconds
				ELSE GREATEST(admin_playback_history.paused_seconds, EXCLUDED.paused_seconds) END,
			measured = admin_playback_history.measured OR EXCLUDED.measured,
			measured_since = LEAST(admin_playback_history.measured_since, EXCLUDED.measured_since),
			measured_until = GREATEST(admin_playback_history.measured_until, EXCLUDED.measured_until),
			target_video_codec = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.target_video_codec ELSE admin_playback_history.target_video_codec END,
			target_resolution = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.target_resolution ELSE admin_playback_history.target_resolution END,
			target_audio_codec = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.target_audio_codec ELSE admin_playback_history.target_audio_codec END,
			target_audio_channels = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.target_audio_channels ELSE admin_playback_history.target_audio_channels END,
			output_container = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.output_container ELSE admin_playback_history.output_container END,
			output_protocol = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.output_protocol ELSE admin_playback_history.output_protocol END,
			target_bitrate_kbps = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.target_bitrate_kbps ELSE admin_playback_history.target_bitrate_kbps END,
			transcode_hw_accel = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.transcode_hw_accel ELSE admin_playback_history.transcode_hw_accel END,
			tone_map_mode = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.tone_map_mode ELSE admin_playback_history.tone_map_mode END,
			adaptation_reasons = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.adaptation_reasons ELSE admin_playback_history.adaptation_reasons END,
			reporting_node = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.reporting_node ELSE admin_playback_history.reporting_node END,
			stream_location = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.stream_location ELSE admin_playback_history.stream_location END,
			compat_origin = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.compat_origin ELSE admin_playback_history.compat_origin END,
			routing_execution = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.routing_execution ELSE admin_playback_history.routing_execution END,
			routing_execution_node_id = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.routing_execution_node_id ELSE admin_playback_history.routing_execution_node_id END,
			routing_execution_node_name = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.routing_execution_node_name ELSE admin_playback_history.routing_execution_node_name END,
			routing_egress = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.routing_egress ELSE admin_playback_history.routing_egress END,
			routing_egress_node_id = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.routing_egress_node_id ELSE admin_playback_history.routing_egress_node_id END,
			routing_egress_node_name = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.routing_egress_node_name ELSE admin_playback_history.routing_egress_node_name END,
			route_facts_at = CASE WHEN `+routeSnapshotReplace+` THEN EXCLUDED.route_facts_at ELSE admin_playback_history.route_facts_at END,
			route_facts_recorded = admin_playback_history.route_facts_recorded OR EXCLUDED.route_facts_recorded
		WHERE `+watchAdvanced+`
			OR (EXCLUDED.route_facts_recorded AND NOT admin_playback_history.route_facts_recorded)
			-- Newer route facts, even unchanged ones, so route_facts_at keeps
			-- up and a delayed older copy can't take their place.
			OR (EXCLUDED.route_facts_recorded AND EXCLUDED.route_facts_at > `+rowRouteFactsAt+`)
	`,
		entry.SessionID,
		entry.UserID,
		entry.ProfileID,
		entry.ProfileName,
		entry.MediaItemID,
		entry.MediaFileID,
		entry.PlayMethod,
		entry.StartedAt,
		entry.EndedAt,
		entry.EndPositionSeconds,
		duration,
		entry.Completed,
		nullableIP(entry.ClientIP),
		entry.Watch.StartPosition,
		entry.Watch.PlayedSeconds,
		entry.Watch.PausedSeconds,
		entry.Watch.Measured,
		nullableTime(entry.Watch.Since),
		nullableTime(entry.Watch.Until),
		nullableString(entry.DeviceID),
		nullableString(entry.DevicePlatform),
		nullableString(entry.ClientName),
		nullableString(entry.ClientVersion),
		nullableString(entry.VideoDecision),
		nullableString(entry.AudioDecision),
		nullableString(string(entry.EndReason)),
		entry.Subtitle,
		nullableString(entry.TargetVideoCodec),
		nullableString(entry.TargetResolution),
		nullableString(entry.TargetAudioCodec),
		nullablePositiveInt(entry.TargetAudioChannels),
		nullableString(entry.OutputContainer),
		nullableString(entry.OutputProtocol),
		nullablePositiveInt(entry.TargetBitrateKbps),
		nullableString(entry.TranscodeHWAccel),
		nullableString(entry.ToneMapMode),
		nullableStringSlice(entry.AdaptationReasons),
		nullableString(entry.ReportingNode),
		nullableString(entry.StreamLocation),
		nullableTrueBool(entry.CompatOrigin),
		nullableString(entry.RoutingExecution),
		nullablePositiveInt(entry.RoutingExecutionNodeID),
		nullableString(entry.RoutingEgress),
		nullablePositiveInt(entry.RoutingEgressNodeID),
		entry.RouteFactsRecorded,
		nullablePositiveInt(entry.EffectiveMediaFileID),
		nullableNonNegIntPtr(entry.AudioTrackIndex),
		nullableString(entry.RoutingExecutionNodeName),
		nullableString(entry.RoutingEgressNodeName),
		entry.subtitleAt(),
		entry.routeFactsAt(),
	)
	queueHistoryFacts(batch, entry)
	return batch
}

// LiveSession reads the shared live row for sessionID: the facts the replica
// holding the session last synced, or, once no live row is left, the row
// the session recorded when it started. UpdatedAt is when the row was
// synced. ErrSessionNotFound when there is neither.
func (s *PGPlaybackAdminStore) LiveSession(ctx context.Context, sessionID string) (*playback.Session, error) {
	live, _, err := s.LiveSessionWithOrigin(ctx, sessionID)
	return live, err
}

// LiveSessionWithOrigin is LiveSession plus whether the returned row came from
// the live playback_sessions_sync table (true) rather than the start-time
// fallback in playback_session_lifecycle.start_facts (false).
func (s *PGPlaybackAdminStore) LiveSessionWithOrigin(ctx context.Context, sessionID string) (*playback.Session, bool, error) {
	if s == nil || s.pool == nil {
		return nil, false, playback.ErrSessionNotFound
	}
	var live playback.Session
	var source int
	var playMethod string
	var toneMapMode string
	var startedAt, syncedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT source, user_id, COALESCE(profile_id, ''), COALESCE(media_file_id, 0), COALESCE(requested_media_file_id, media_file_id, 0),
			COALESCE(audio_track_index, 0),
			COALESCE(play_method, ''), started_at, COALESCE(HOST(client_ip), ''),
			COALESCE(client_name, ''), COALESCE(client_version, ''), COALESCE(client_build, ''),
			COALESCE(client_channel, ''), COALESCE(client_user_agent, ''),
			COALESCE(device_id, ''), COALESCE(device_platform, ''),
			COALESCE(transcode_audio, FALSE), COALESCE(target_video_codec, ''),
			COALESCE(target_resolution, ''), COALESCE(target_audio_codec, ''),
			COALESCE(target_audio_channels, 0), COALESCE(output_container, ''), COALESCE(output_protocol, ''),
			COALESCE(target_bitrate_kbps, 0), COALESCE(transcode_hw_accel, ''), COALESCE(tone_map_mode, ''),
			COALESCE(reporting_node, ''), COALESCE(stream_location, ''),
			COALESCE(routing_execution, ''), COALESCE(routing_execution_node_id, 0),
			COALESCE(routing_egress, ''), COALESCE(routing_egress_node_id, 0),
			COALESCE(position_seconds, 0), COALESCE(compat_origin, FALSE),
			COALESCE(timeline_duration_seconds, 0), adaptation_reasons, subtitle,
			COALESCE(plan_id, ''), last_sync_at
		FROM (
			SELECT 0 AS source, live.* FROM playback_sessions_sync live WHERE live.session_id = $1
			UNION ALL
			-- No live row left: the row the session recorded when it started.
			SELECT 1, (jsonb_populate_record(NULL::playback_sessions_sync, jsonb_build_object(
				'session_id', lc.session_id, 'user_id', lc.user_id, 'profile_id', lc.profile_id,
				'media_file_id', lc.media_file_id, 'requested_media_file_id', lc.requested_media_file_id,
				'started_at', lc.started_at) || lc.start_facts)).*
			FROM playback_session_lifecycle lc WHERE lc.session_id = $2::uuid
		) s ORDER BY source LIMIT 1`, sessionID, lifecycleIDParam(sessionID)).Scan(
		&source, &live.UserID, &live.ProfileID, &live.MediaFileID, &live.RequestedMediaFileID,
		&live.AudioTrackIndex,
		&playMethod, &startedAt, &live.ClientIP,
		&live.ClientName, &live.ClientVersion, &live.ClientBuild,
		&live.ClientChannel, &live.ClientUserAgent,
		&live.DeviceID, &live.DevicePlatform,
		&live.TranscodeAudio, &live.TargetVideoCodec,
		&live.TargetResolution, &live.TargetAudioCodec,
		&live.TargetAudioChannels, &live.OutputContainer, &live.OutputProtocol,
		&live.TargetBitrateKbps, &live.TranscodeHWAccel, &toneMapMode,
		&live.ReportingNode, &live.StreamLocation,
		&live.RoutingExecution, &live.RoutingExecutionNodeID,
		&live.RoutingEgress, &live.RoutingEgressNodeID,
		&live.Position, &live.IsJellyfinCompat,
		&live.TimelineDuration, &live.AdaptationReasons, &live.Subtitle,
		&live.PlanID, &syncedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, playback.ErrSessionNotFound
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading live playback session: %w", err)
	}
	live.ID = sessionID
	live.PlayMethod = playback.PlayMethod(playMethod)
	live.ToneMapMode = tonemap.Mode(toneMapMode)
	if startedAt != nil {
		live.StartedAt = *startedAt
	}
	if syncedAt != nil {
		live.UpdatedAt = *syncedAt
	}
	return &live, source == 0, nil
}

// SessionWatch is the watch-time measurement the server holding sessionID
// last saved on its lifecycle row (when it started, then on each live-session
// sync). ok is false when none was saved.
func (s *PGPlaybackAdminStore) SessionWatch(ctx context.Context, sessionID string) (watch playback.WatchTime, ok bool, err error) {
	id := lifecycleIDParam(sessionID)
	if s == nil || s.pool == nil || id == nil {
		return playback.WatchTime{}, false, nil
	}
	var raw []byte
	err = s.pool.QueryRow(ctx, `SELECT watch FROM playback_session_lifecycle WHERE session_id = $1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && raw == nil) {
		return playback.WatchTime{}, false, nil
	}
	if err != nil {
		return playback.WatchTime{}, false, fmt.Errorf("reading saved watch time: %w", err)
	}
	if err := json.Unmarshal(raw, &watch); err != nil {
		return playback.WatchTime{}, false, fmt.Errorf("decoding saved watch time: %w", err)
	}
	return watch, watch.Measured, nil
}

// nullableJSON stores an absent document as NULL.
func nullableJSON(data json.RawMessage) any {
	if len(data) == 0 {
		return nil
	}
	return string(data)
}

// HistoryEndReason is the end reason the admin log holds for sessionID, or
// "" when it holds no row or a row without one.
func (s *PGPlaybackAdminStore) HistoryEndReason(ctx context.Context, sessionID string) (PlaybackEndReason, error) {
	if s == nil || s.pool == nil {
		return "", nil
	}
	var reason *string
	err := s.pool.QueryRow(ctx, `SELECT end_reason FROM admin_playback_history WHERE session_id = $1`, sessionID).Scan(&reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading playback history end reason: %w", err)
	}
	if reason == nil {
		return "", nil
	}
	return PlaybackEndReason(*reason), nil
}

func (s *PGPlaybackAdminStore) DeleteSession(ctx context.Context, sessionID string) error {
	if s == nil || s.pool == nil {
		return nil
	}

	tag, err := s.pool.Exec(ctx, `DELETE FROM playback_sessions_sync WHERE session_id = $1`, sessionID)
	if err != nil {
		return fmt.Errorf("deleting synced playback session: %w", err)
	}
	if tag.RowsAffected() > 0 && s.eventsHub != nil {
		if err := s.eventsHub.PublishJSON(
			ctx,
			evt.ChannelSessions,
			"sessions.replaced",
			nil,
			evt.PublishOptions{AdminOnly: true},
		); err != nil {
			return fmt.Errorf("publishing session change: %w", err)
		}
	}

	return nil
}

// adminPlaybackWatchTimeSQL is the watch time, in seconds, of the
// admin_playback_history row aliased alias. A row with a measurement uses it.
// An older row only knows where the play ended, which overstates a resumed
// play by its resume point, so its end position is capped at the session's
// wall-clock length; that still counts pauses and seeks, but is the best
// estimate the row holds.
func adminPlaybackWatchTimeSQL(alias string) string {
	return `CASE WHEN ` + alias + `.measured THEN ` + alias + `.played_seconds ELSE LEAST(GREATEST(` + alias + `.watched_seconds, 0),
		GREATEST(EXTRACT(EPOCH FROM (` + alias + `.ended_at - ` + alias + `.started_at)), 0)) END`
}

// nullableTime returns nil for the zero time, which an unmeasured play
// carries, so PostgreSQL stores NULL.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// nullableIP returns nil for empty IP strings so PostgreSQL receives NULL
// instead of an invalid inet value.
func nullableIP(ip string) interface{} {
	return nullableString(ip)
}

// nullableString returns nil for an empty string, which stores an unknown
// value as NULL.
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePositiveInt(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableNonNegIntPtr(value *int) any {
	if value == nil || *value < 0 {
		return nil
	}
	return *value
}

func nullableStringSlice(values []string) any {
	if len(values) == 0 {
		return nil
	}
	return values
}

func nullableTrueBool(value bool) any {
	if !value {
		return nil
	}
	return true
}

// queueHistoryFacts queues the statement that records the facts a
// finalization knows and the row lacks, and its end reason and subtitle.
func queueHistoryFacts(batch *pgx.Batch, entry AdminPlaybackHistoryEntry) {
	// The upsert above only touches a row this finalization advances. This
	// one runs whether or not it did, so a copy that knows a fact the row
	// lacks still records it. It runs in the batch's implicit transaction,
	// after the upsert.
	const laterReason = `$8::text IS NOT NULL AND (end_reason_at IS NULL OR $9::timestamptz >= end_reason_at)`
	const backfillRoute = `(NOT route_facts_recorded AND $27)`
	// A row written before subtitle_at existed ordered its subtitle by
	// ended_at (the upsert keeps that order when it moves ended_at).
	const laterSubtitle = `$33::timestamptz >= COALESCE(subtitle_at, ended_at)`
	batch.Queue(`
		UPDATE admin_playback_history SET
			device_id = COALESCE(device_id, $2),
			device_platform = COALESCE(device_platform, $3),
			client_name = COALESCE(client_name, $4),
			client_version = COALESCE(client_version, $5),
			video_decision = CASE WHEN `+backfillRoute+` THEN $6 ELSE COALESCE(video_decision, $6) END,
			audio_decision = CASE WHEN `+backfillRoute+` THEN $7 ELSE COALESCE(audio_decision, $7) END,
			end_reason = CASE WHEN `+laterReason+` THEN $8 ELSE end_reason END,
			end_reason_at = CASE WHEN `+laterReason+` THEN $9::timestamptz ELSE end_reason_at END,
			target_video_codec = CASE WHEN `+backfillRoute+` THEN $10 ELSE target_video_codec END,
			target_resolution = CASE WHEN `+backfillRoute+` THEN $11 ELSE target_resolution END,
			target_audio_codec = CASE WHEN `+backfillRoute+` THEN $12 ELSE target_audio_codec END,
			target_audio_channels = CASE WHEN `+backfillRoute+` THEN $13 ELSE target_audio_channels END,
			output_container = CASE WHEN `+backfillRoute+` THEN $14 ELSE output_container END,
			output_protocol = CASE WHEN `+backfillRoute+` THEN $15 ELSE output_protocol END,
			target_bitrate_kbps = CASE WHEN `+backfillRoute+` THEN $16 ELSE target_bitrate_kbps END,
			transcode_hw_accel = CASE WHEN `+backfillRoute+` THEN $17 ELSE transcode_hw_accel END,
			tone_map_mode = CASE WHEN `+backfillRoute+` THEN $18 ELSE tone_map_mode END,
			adaptation_reasons = CASE WHEN `+backfillRoute+` THEN $19 ELSE adaptation_reasons END,
			reporting_node = CASE WHEN `+backfillRoute+` THEN $20 ELSE reporting_node END,
			stream_location = CASE WHEN `+backfillRoute+` THEN $21 ELSE stream_location END,
			compat_origin = CASE WHEN `+backfillRoute+` THEN $22 ELSE compat_origin END,
			routing_execution = CASE WHEN `+backfillRoute+` THEN $23 ELSE routing_execution END,
			routing_execution_node_id = CASE WHEN `+backfillRoute+` THEN $24 ELSE routing_execution_node_id END,
			routing_egress = CASE WHEN `+backfillRoute+` THEN $25 ELSE routing_egress END,
			routing_egress_node_id = CASE WHEN `+backfillRoute+` THEN $26 ELSE routing_egress_node_id END,
			effective_media_file_id = CASE WHEN `+backfillRoute+` THEN $28 ELSE effective_media_file_id END,
			audio_track_index = CASE WHEN `+backfillRoute+` THEN $29 ELSE audio_track_index END,
			routing_execution_node_name = CASE WHEN `+backfillRoute+` THEN COALESCE(NULLIF(BTRIM($30::text), ''), (SELECT NULLIF(BTRIM(name), '') FROM stream_nodes WHERE id = $24)) ELSE routing_execution_node_name END,
			routing_egress_node_name = CASE WHEN `+backfillRoute+` THEN COALESCE(NULLIF(BTRIM($31::text), ''), (SELECT NULLIF(BTRIM(name), '') FROM stream_nodes WHERE id = $26)) ELSE routing_egress_node_name END,
			route_facts_at = CASE WHEN `+backfillRoute+` THEN $34::timestamptz ELSE route_facts_at END,
			route_facts_recorded = route_facts_recorded OR $27,
			subtitle = CASE WHEN `+laterSubtitle+` THEN $32::jsonb ELSE subtitle END,
			subtitle_at = CASE WHEN `+laterSubtitle+` THEN $33::timestamptz ELSE subtitle_at END
		WHERE session_id = $1 AND (
			(device_id IS NULL AND $2::text IS NOT NULL)
			OR (device_platform IS NULL AND $3::text IS NOT NULL)
			OR (client_name IS NULL AND $4::text IS NOT NULL)
			OR (client_version IS NULL AND $5::text IS NOT NULL)
			OR (video_decision IS NULL AND $6::text IS NOT NULL)
			OR (audio_decision IS NULL AND $7::text IS NOT NULL)
			OR (`+laterReason+` AND (end_reason IS DISTINCT FROM $8 OR end_reason_at IS DISTINCT FROM $9::timestamptz))
			OR `+backfillRoute+`
			OR (`+laterSubtitle+` AND (subtitle IS DISTINCT FROM $32::jsonb OR subtitle_at IS DISTINCT FROM $33::timestamptz)))
	`,
		entry.SessionID,
		nullableString(entry.DeviceID),
		nullableString(entry.DevicePlatform),
		nullableString(entry.ClientName),
		nullableString(entry.ClientVersion),
		nullableString(entry.VideoDecision),
		nullableString(entry.AudioDecision),
		nullableString(string(entry.EndReason)),
		entry.EndedAt,
		nullableString(entry.TargetVideoCodec),
		nullableString(entry.TargetResolution),
		nullableString(entry.TargetAudioCodec),
		nullablePositiveInt(entry.TargetAudioChannels),
		nullableString(entry.OutputContainer),
		nullableString(entry.OutputProtocol),
		nullablePositiveInt(entry.TargetBitrateKbps),
		nullableString(entry.TranscodeHWAccel),
		nullableString(entry.ToneMapMode),
		nullableStringSlice(entry.AdaptationReasons),
		nullableString(entry.ReportingNode),
		nullableString(entry.StreamLocation),
		nullableTrueBool(entry.CompatOrigin),
		nullableString(entry.RoutingExecution),
		nullablePositiveInt(entry.RoutingExecutionNodeID),
		nullableString(entry.RoutingEgress),
		nullablePositiveInt(entry.RoutingEgressNodeID),
		entry.RouteFactsRecorded,
		nullablePositiveInt(entry.EffectiveMediaFileID),
		nullableNonNegIntPtr(entry.AudioTrackIndex),
		nullableString(entry.RoutingExecutionNodeName),
		nullableString(entry.RoutingEgressNodeName),
		entry.Subtitle,
		entry.subtitleAt(),
		entry.routeFactsAt(),
	)
}
