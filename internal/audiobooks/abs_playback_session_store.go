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

// AggregateStats returns the aggregated /me/listening-stats payload.
func (s *ABSPlaybackSessionStore) AggregateStats(ctx context.Context, userID, profileID string) (abs.Stats, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: invalid user id %q: %w", userID, err)
	}
	out := abs.Stats{Days: []abs.DayStat{}, Monthly: []abs.MonthStat{}}

	row := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(time_listening_seconds), 0), COUNT(DISTINCT content_id)
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2`,
		uid, profileID,
	)
	if err := row.Scan(&out.TotalTime, &out.Items); err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats totals: %w", err)
	}

	rows, err := s.Pool.Query(ctx, `
		SELECT TO_CHAR(date_trunc('day', started_at), 'YYYY-MM-DD'),
		       COALESCE(SUM(time_listening_seconds), 0)
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2
		  AND started_at >= now() - INTERVAL '30 days'
		GROUP BY 1
		ORDER BY 1 DESC`,
		uid, profileID,
	)
	if err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats days: %w", err)
	}
	for rows.Next() {
		var d abs.DayStat
		if scanErr := rows.Scan(&d.Date, &d.Seconds); scanErr != nil {
			rows.Close()
			return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats days scan: %w", scanErr)
		}
		out.Days = append(out.Days, d)
	}
	rows.Close()

	dowRows, err := s.Pool.Query(ctx, `
		SELECT EXTRACT(DOW FROM started_at)::int,
		       COALESCE(SUM(time_listening_seconds), 0)
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2
		GROUP BY 1`,
		uid, profileID,
	)
	if err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats dow: %w", err)
	}
	for dowRows.Next() {
		var dow, secs int
		if scanErr := dowRows.Scan(&dow, &secs); scanErr != nil {
			dowRows.Close()
			return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats dow scan: %w", scanErr)
		}
		if dow >= 0 && dow < 7 {
			out.DayOfWeek[dow] = secs
		}
	}
	dowRows.Close()

	mRows, err := s.Pool.Query(ctx, `
		SELECT TO_CHAR(date_trunc('month', started_at), 'YYYY-MM'),
		       COALESCE(SUM(time_listening_seconds), 0)
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2
		  AND started_at >= now() - INTERVAL '12 months'
		GROUP BY 1
		ORDER BY 1 DESC`,
		uid, profileID,
	)
	if err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats months: %w", err)
	}
	for mRows.Next() {
		var m abs.MonthStat
		if scanErr := mRows.Scan(&m.Month, &m.Seconds); scanErr != nil {
			mRows.Close()
			return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats months scan: %w", scanErr)
		}
		out.Monthly = append(out.Monthly, m)
	}
	mRows.Close()
	return out, nil
}

func (s *ABSPlaybackSessionStore) ListClosedSessions(ctx context.Context, userID, profileID string, limit, offset int) ([]abs.ABSPlaybackSession, int, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_playback_session_store: invalid user id %q: %w", userID, err)
	}
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2 AND closed_at IS NOT NULL`,
		uid, profileID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("abs_playback_session_store: list closed count: %w", err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, content_id,
		       time_listening_seconds, current_position_seconds, closed_at
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2 AND closed_at IS NOT NULL
		ORDER BY started_at DESC
		LIMIT $3 OFFSET $4`,
		uid, profileID, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_playback_session_store: list closed: %w", err)
	}
	defer rows.Close()
	out := make([]abs.ABSPlaybackSession, 0, limit)
	for rows.Next() {
		var sess abs.ABSPlaybackSession
		var scanUID int
		var scanProfile string
		var closedAt *time.Time
		if err := rows.Scan(&sess.ID, &scanUID, &scanProfile, &sess.ContentID, &sess.TimeListeningSeconds, &sess.CurrentPositionSeconds, &closedAt); err != nil {
			return nil, 0, fmt.Errorf("abs_playback_session_store: list closed scan: %w", err)
		}
		sess.UserID = strconv.Itoa(scanUID)
		sess.ProfileID = scanProfile
		sess.ClosedAt = closedAt
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("abs_playback_session_store: list closed rows: %w", err)
	}
	return out, total, nil
}
