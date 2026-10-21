package handlers

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback"
)

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
}

// PlaybackAdminStore persists finalized playback history and manages the
// shared active-session sync rows used by admin monitoring.
//
// RecordHistory keeps one row per session. A session finalized again (copies
// on several replicas, or one resumed after expiry) updates its row only when
// it reached further, so the row holds the furthest position whichever
// finalization lands first. Measured watch time is kept separately: the copy
// that received the play's progress reports holds the largest counters, so
// each counter keeps its largest value whatever the position did.
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

	var duration any
	if entry.DurationSeconds != nil {
		duration = *entry.DurationSeconds
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, profile_name, media_item_id, media_file_id,
			 play_method, started_at, ended_at, watched_seconds, duration_seconds, completed, client_ip,
			 start_position_seconds, played_seconds, paused_seconds, measured)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::inet, $14, $15, $16, $17)
		ON CONFLICT (session_id) DO UPDATE SET
			watched_seconds = GREATEST(admin_playback_history.watched_seconds, EXCLUDED.watched_seconds),
			duration_seconds = CASE WHEN EXCLUDED.watched_seconds > admin_playback_history.watched_seconds
				THEN COALESCE(EXCLUDED.duration_seconds, admin_playback_history.duration_seconds)
				ELSE admin_playback_history.duration_seconds END,
			completed = CASE WHEN EXCLUDED.watched_seconds > admin_playback_history.watched_seconds
				THEN admin_playback_history.completed OR EXCLUDED.completed
				ELSE admin_playback_history.completed END,
			ended_at = CASE WHEN EXCLUDED.watched_seconds > admin_playback_history.watched_seconds
				THEN GREATEST(admin_playback_history.ended_at, EXCLUDED.ended_at)
				ELSE admin_playback_history.ended_at END,
			start_position_seconds = CASE
				WHEN NOT admin_playback_history.measured THEN EXCLUDED.start_position_seconds
				WHEN NOT EXCLUDED.measured THEN admin_playback_history.start_position_seconds
				ELSE LEAST(admin_playback_history.start_position_seconds, EXCLUDED.start_position_seconds) END,
			played_seconds = GREATEST(admin_playback_history.played_seconds, EXCLUDED.played_seconds),
			paused_seconds = GREATEST(admin_playback_history.paused_seconds, EXCLUDED.paused_seconds),
			measured = admin_playback_history.measured OR EXCLUDED.measured
		WHERE EXCLUDED.watched_seconds > admin_playback_history.watched_seconds
			OR EXCLUDED.played_seconds > admin_playback_history.played_seconds
			OR EXCLUDED.paused_seconds > admin_playback_history.paused_seconds
			OR (EXCLUDED.measured AND NOT admin_playback_history.measured)
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
	)
	if err != nil {
		return fmt.Errorf("recording playback history: %w", err)
	}

	return nil
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

// nullableIP returns nil for empty IP strings so PostgreSQL receives NULL
// instead of an invalid inet value.
func nullableIP(ip string) interface{} {
	if ip == "" {
		return nil
	}
	return ip
}
