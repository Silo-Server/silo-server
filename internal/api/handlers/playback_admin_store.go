package handlers

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	evt "github.com/Silo-Server/silo-server/internal/events"
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
}

// PlaybackAdminStore persists finalized playback history and manages the
// shared active-session sync rows used by admin monitoring.
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
			 play_method, started_at, ended_at, watched_seconds, duration_seconds, completed, client_ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::inet)
		ON CONFLICT (session_id) DO NOTHING
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

// nullableIP returns nil for empty IP strings so PostgreSQL receives NULL
// instead of an invalid inet value.
func nullableIP(ip string) interface{} {
	if ip == "" {
		return nil
	}
	return ip
}
