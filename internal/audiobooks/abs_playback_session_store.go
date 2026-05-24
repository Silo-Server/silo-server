package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSPlaybackSessionStore implements abs.ABSPlaybackSessionStore on the
// abs_playback_sessions table (migration 143). Each row tracks one
// /abs/api/items/{id}/play session opened by an ABS-compatible client
// and is closed (closed_at set) when the client calls /session/{sid}/close.
type ABSPlaybackSessionStore struct {
	Pool *pgxpool.Pool
}

// InsertPlaybackSession persists a new session row at play-start time.
func (s *ABSPlaybackSessionStore) InsertPlaybackSession(ctx context.Context, sess abs.ABSPlaybackSession) error {
	uid, err := strconv.Atoi(sess.UserID)
	if err != nil {
		return fmt.Errorf("abs_playback_session_store: invalid user_id %q: %w", sess.UserID, err)
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO abs_playback_sessions
		  (id, user_id, profile_id, content_id, media_file_id,
		   started_at, last_sync_at, time_listening_seconds, current_position_seconds)
		VALUES ($1, $2, $3, $4, $5, now(), now(), 0, $6)
		ON CONFLICT (id) DO NOTHING`,
		sess.ID,
		uid,
		sess.ProfileID,
		sess.ContentID,
		sess.MediaFileID,
		sess.CurrentPositionSeconds,
	)
	if err != nil {
		return fmt.Errorf("abs_playback_session_store: insert: %w", err)
	}
	return nil
}

// GetPlaybackSession fetches a session by its ULID. Returns abs.ErrNotFound
// when the row doesn't exist.
func (s *ABSPlaybackSessionStore) GetPlaybackSession(ctx context.Context, id string) (abs.ABSPlaybackSession, error) {
	var sess abs.ABSPlaybackSession
	var uid int
	var profileID string
	var closedAt *time.Time
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, content_id,
		       time_listening_seconds, current_position_seconds, closed_at
		FROM abs_playback_sessions
		WHERE id = $1`, id)
	err := row.Scan(
		&sess.ID, &uid, &profileID, &sess.ContentID,
		&sess.TimeListeningSeconds, &sess.CurrentPositionSeconds, &closedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return abs.ABSPlaybackSession{}, abs.ErrNotFound
	}
	if err != nil {
		return abs.ABSPlaybackSession{}, fmt.Errorf("abs_playback_session_store: get: %w", err)
	}
	sess.UserID = strconv.Itoa(uid)
	sess.ProfileID = profileID
	sess.ClosedAt = closedAt
	return sess, nil
}

// SyncPlaybackSession updates the position and accumulated listening time for
// an open session. Idempotent: calling it on an already-closed session is
// a no-op (the WHERE closed_at IS NULL guard prevents overwriting a final
// state with a stale sync payload that arrives after close).
func (s *ABSPlaybackSessionStore) SyncPlaybackSession(ctx context.Context, id string, currentPositionSeconds float64, timeListeningSeconds int) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE abs_playback_sessions
		SET current_position_seconds = $2,
		    time_listening_seconds   = time_listening_seconds + $3,
		    last_sync_at             = now()
		WHERE id = $1 AND closed_at IS NULL`,
		id, currentPositionSeconds, timeListeningSeconds,
	)
	if err != nil {
		return fmt.Errorf("abs_playback_session_store: sync: %w", err)
	}
	return nil
}

// ClosePlaybackSession sets closed_at = now() for the given session.
// Idempotent: closing an already-closed session is safe.
func (s *ABSPlaybackSessionStore) ClosePlaybackSession(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE abs_playback_sessions
		SET closed_at = now()
		WHERE id = $1 AND closed_at IS NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("abs_playback_session_store: close: %w", err)
	}
	return nil
}
