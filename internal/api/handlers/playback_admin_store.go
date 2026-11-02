package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback"
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
	// PlaybackEndReplaced: a Jellyfin client switched play method, and the
	// play continues under a new session.
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
	SessionID       string
	UserID          int
	ProfileID       string
	ProfileName     string
	MediaItemID     string
	MediaFileID     int
	PlayMethod      string
	StartedAt       string
	EndedAt         string
	WatchedSeconds  float64
	DurationSeconds *float64
	Completed       bool
	ClientIP        string
	// Watch is the session's measured watch time. WatchedSeconds is where the
	// play ended, not how much of it was watched.
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
// replaces it, and one without a reason leaves it. That order is kept apart
// from ended_at, which only moves when a finalization gets further.
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
	batch := &pgx.Batch{}
	batch.Queue(`
		INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, profile_name, media_item_id, media_file_id,
			 play_method, started_at, ended_at, watched_seconds, duration_seconds, completed, client_ip,
			 start_position_seconds, played_seconds, paused_seconds, measured, measured_since, measured_until,
			 device_id, device_platform, client_name, client_version, video_decision, audio_decision,
			 end_reason, end_reason_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::inet, $14, $15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24, $25, $26::text, CASE WHEN $26::text IS NOT NULL THEN $9::timestamptz END)
		ON CONFLICT (session_id) DO UPDATE SET
			watched_seconds = GREATEST(admin_playback_history.watched_seconds, EXCLUDED.watched_seconds),
			duration_seconds = CASE WHEN EXCLUDED.watched_seconds > admin_playback_history.watched_seconds
				THEN COALESCE(EXCLUDED.duration_seconds, admin_playback_history.duration_seconds)
				ELSE admin_playback_history.duration_seconds END,
			completed = admin_playback_history.completed OR EXCLUDED.completed,
			ended_at = GREATEST(admin_playback_history.ended_at, EXCLUDED.ended_at),
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
			measured_until = GREATEST(admin_playback_history.measured_until, EXCLUDED.measured_until)
		WHERE EXCLUDED.watched_seconds > admin_playback_history.watched_seconds
			OR EXCLUDED.played_seconds > admin_playback_history.played_seconds
			OR EXCLUDED.paused_seconds > admin_playback_history.paused_seconds
			OR (EXCLUDED.measured AND NOT admin_playback_history.measured)
			OR (EXCLUDED.completed AND NOT admin_playback_history.completed)
			OR `+laterStretch+`
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
		entry.WatchedSeconds,
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
	)
	queueHistoryFacts(batch, entry)
	return batch
}

// LiveSession reads the shared live row for sessionID: the facts the replica
// holding the session last synced, or, once no live row is left, the row
// the session recorded when it started. ErrSessionNotFound when there is
// neither.
func (s *PGPlaybackAdminStore) LiveSession(ctx context.Context, sessionID string) (*playback.Session, error) {
	if s == nil || s.pool == nil {
		return nil, playback.ErrSessionNotFound
	}
	var live playback.Session
	var playMethod string
	var startedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, COALESCE(profile_id, ''), COALESCE(media_file_id, 0), COALESCE(requested_media_file_id, media_file_id, 0),
			COALESCE(play_method, ''), started_at, COALESCE(HOST(client_ip), ''),
			COALESCE(client_name, ''), COALESCE(client_version, ''), COALESCE(client_build, ''),
			COALESCE(client_channel, ''), COALESCE(client_user_agent, ''),
			COALESCE(device_id, ''), COALESCE(device_platform, ''),
			COALESCE(transcode_audio, FALSE), COALESCE(target_video_codec, ''),
			COALESCE(position_seconds, 0), COALESCE(compat_origin, FALSE)
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
		&live.UserID, &live.ProfileID, &live.MediaFileID, &live.RequestedMediaFileID,
		&playMethod, &startedAt, &live.ClientIP,
		&live.ClientName, &live.ClientVersion, &live.ClientBuild,
		&live.ClientChannel, &live.ClientUserAgent,
		&live.DeviceID, &live.DevicePlatform,
		&live.TranscodeAudio, &live.TargetVideoCodec,
		&live.Position, &live.IsJellyfinCompat,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, playback.ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading live playback session: %w", err)
	}
	live.ID = sessionID
	live.PlayMethod = playback.PlayMethod(playMethod)
	if startedAt != nil {
		live.StartedAt = *startedAt
	}
	return &live, nil
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

// queueHistoryFacts queues the statement that records the facts a
// finalization knows and the row lacks, and its end reason.
func queueHistoryFacts(batch *pgx.Batch, entry AdminPlaybackHistoryEntry) {
	// The upsert above only touches a row this finalization advances. This
	// one runs whether or not it did, so a copy that knows a fact the row
	// lacks still records it. It runs in the batch's implicit transaction,
	// after the upsert.
	const laterReason = `$8::text IS NOT NULL AND (end_reason_at IS NULL OR $9::timestamptz >= end_reason_at)`
	batch.Queue(`
		UPDATE admin_playback_history SET
			device_id = COALESCE(device_id, $2),
			device_platform = COALESCE(device_platform, $3),
			client_name = COALESCE(client_name, $4),
			client_version = COALESCE(client_version, $5),
			video_decision = COALESCE(video_decision, $6),
			audio_decision = COALESCE(audio_decision, $7),
			end_reason = CASE WHEN `+laterReason+` THEN $8 ELSE end_reason END,
			end_reason_at = CASE WHEN `+laterReason+` THEN $9::timestamptz ELSE end_reason_at END
		WHERE session_id = $1 AND (
			(device_id IS NULL AND $2::text IS NOT NULL)
			OR (device_platform IS NULL AND $3::text IS NOT NULL)
			OR (client_name IS NULL AND $4::text IS NOT NULL)
			OR (client_version IS NULL AND $5::text IS NOT NULL)
			OR (video_decision IS NULL AND $6::text IS NOT NULL)
			OR (audio_decision IS NULL AND $7::text IS NOT NULL)
			OR (`+laterReason+` AND (end_reason IS DISTINCT FROM $8 OR end_reason_at IS DISTINCT FROM $9::timestamptz)))
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
	)
}
