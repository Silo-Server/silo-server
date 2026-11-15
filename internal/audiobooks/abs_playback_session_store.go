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
		       time_listening_seconds, current_position_seconds,
		       started_at, last_sync_at, closed_at, media_file_id,
		       client_session_id IS NOT NULL
		FROM abs_playback_sessions
		WHERE id = $1`, id)
	err := row.Scan(
		&sess.ID, &uid, &profileID, &sess.ContentID,
		&sess.TimeListeningSeconds, &sess.CurrentPositionSeconds,
		&sess.StartedAt, &sess.LastSyncAt, &closedAt, &sess.MediaFileID,
		&sess.ReportOnly,
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

// reportedListeningSlackSeconds is how far a reported play's listening may
// run ahead of the time since its row began, for reporting delays.
const reportedListeningSlackSeconds = 60

// reportedPlayIdleSeconds matches the lifecycle sweep's idle window for
// reported plays: a row silent this long whose start was never recorded is
// one no sweep will end.
const reportedPlayIdleSeconds = 90

// updateReportedPlaySQL applies a report to the open play of a client
// session. Listening is the client's running total less the total the row
// began at, never negative, never more than the row's age allows, and never
// decreasing. The client's total only rises within its session, so a report
// whose total is below the highest one seen is an older one arriving late: it
// keeps the play alive but leaves the newer position. It returns the play and
// whether its lifecycle start was recorded.
const updateReportedPlaySQL = `
	UPDATE abs_playback_sessions s
	SET current_position_seconds = CASE
	        WHEN $5::double precision >= s.reported_listening_total THEN $4
	        ELSE s.current_position_seconds END,
	    reported_listening_total = GREATEST(s.reported_listening_total, $5::double precision),
	    time_listening_seconds   = GREATEST(s.time_listening_seconds,
	        GREATEST(LEAST(FLOOR($5::double precision - s.reported_listening_base),
	                       EXTRACT(EPOCH FROM now() - s.started_at) + $6), 0)::integer),
	    last_sync_at             = now()
	WHERE s.user_id = $1 AND s.profile_id = $2 AND s.client_session_id = $3 AND s.closed_at IS NULL
	RETURNING s.id, s.started_at, EXISTS (SELECT 1 FROM playback_session_lifecycle lc WHERE lc.session_id = s.id::uuid)`

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func updateReportedPlay(ctx context.Context, q queryRower, uid int, p abs.ReportedPlay) (abs.ReportedPlayState, bool, error) {
	var st abs.ReportedPlayState
	err := q.QueryRow(ctx, updateReportedPlaySQL, uid, p.ProfileID, p.ClientSessionID,
		p.PositionSeconds, p.ListeningSeconds, reportedListeningSlackSeconds).Scan(&st.ID, &st.StartedAt, &st.Announced)
	if errors.Is(err, pgx.ErrNoRows) {
		return abs.ReportedPlayState{}, false, nil
	}
	return st, err == nil, err
}

// UpdateReportedPlay applies a report to the client session's open reported
// play, in one statement. found is false when the client session has none.
func (s *ABSPlaybackSessionStore) UpdateReportedPlay(ctx context.Context, p abs.ReportedPlay) (abs.ReportedPlayState, bool, error) {
	uid, err := strconv.Atoi(p.UserID)
	if err != nil {
		return abs.ReportedPlayState{}, false, fmt.Errorf("abs_playback_session_store: invalid user_id %q: %w", p.UserID, err)
	}
	st, found, err := updateReportedPlay(ctx, s.Pool, uid, p)
	if err != nil {
		return abs.ReportedPlayState{}, false, fmt.Errorf("abs_playback_session_store: update reported play: %w", err)
	}
	return st, found, nil
}

// StartReportedPlay applies a report to the client session's open reported
// play, or starts one under newID unless the account already has maxOpen
// open, which an empty ID reports. It looks the open play up again under a
// per-account lock before admitting a new one, so concurrent first reports on
// different servers start one play, and the cap holds across the cluster.
// The account's silent rows whose start was never recorded are closed first:
// nothing announced them, no sweep will end them, and they would hold slots.
func (s *ABSPlaybackSessionStore) StartReportedPlay(ctx context.Context, p abs.ReportedPlay, newID string, maxOpen int) (abs.ReportedPlayState, error) {
	uid, err := strconv.Atoi(p.UserID)
	if err != nil {
		return abs.ReportedPlayState{}, fmt.Errorf("abs_playback_session_store: invalid user_id %q: %w", p.UserID, err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return abs.ReportedPlayState{}, fmt.Errorf("abs_playback_session_store: start reported play: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	st, err := startReportedPlayTx(ctx, tx, uid, p, newID, maxOpen)
	if err != nil {
		return abs.ReportedPlayState{}, fmt.Errorf("abs_playback_session_store: start reported play: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return abs.ReportedPlayState{}, fmt.Errorf("abs_playback_session_store: start reported play commit: %w", err)
	}
	return st, nil
}

func startReportedPlayTx(ctx context.Context, tx pgx.Tx, uid int, p abs.ReportedPlay, newID string, maxOpen int) (abs.ReportedPlayState, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('abs_reported_plays', $1))`, uid); err != nil {
		return abs.ReportedPlayState{}, err
	}
	if st, found, err := updateReportedPlay(ctx, tx, uid, p); err != nil || found {
		return st, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE abs_playback_sessions s SET closed_at = now()
		WHERE s.user_id = $1 AND s.client_session_id IS NOT NULL AND s.closed_at IS NULL
		  AND s.last_sync_at < now() - make_interval(secs => $2)
		  AND NOT EXISTS (SELECT 1 FROM playback_session_lifecycle lc WHERE lc.session_id = s.id::uuid)`,
		uid, reportedPlayIdleSeconds); err != nil {
		return abs.ReportedPlayState{}, err
	}
	var open int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM abs_playback_sessions
		WHERE user_id = $1 AND client_session_id IS NOT NULL AND closed_at IS NULL`, uid).Scan(&open); err != nil {
		return abs.ReportedPlayState{}, err
	}
	if open >= maxOpen {
		return abs.ReportedPlayState{}, nil
	}
	st := abs.ReportedPlayState{ID: newID}
	err := tx.QueryRow(ctx, `
		INSERT INTO abs_playback_sessions
		  (id, user_id, profile_id, content_id, media_file_id, started_at, last_sync_at,
		   time_listening_seconds, current_position_seconds, client_session_id,
		   reported_listening_base, reported_listening_total)
		VALUES ($1, $2, $3, $4, $5, now(), now(), 0, $6, $7, GREATEST($8::double precision, 0), $8)
		RETURNING started_at`,
		newID, uid, p.ProfileID, p.ContentID, p.MediaFileID, p.PositionSeconds, p.ClientSessionID, p.ListeningSeconds).Scan(&st.StartedAt)
	return st, err
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

// ClosePlaybackSession sets closed_at = now() for the given session, first
// applying the final sync when there is one, in the same statement. It reports
// whether this call closed the session: of concurrent or repeated closes, only
// one does, and only its final sync is applied.
func (s *ABSPlaybackSessionStore) ClosePlaybackSession(ctx context.Context, id string, final *abs.FinalSync) (bool, error) {
	var position float64
	var listening int
	if final != nil {
		position, listening = final.PositionSeconds, final.ListeningSeconds
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE abs_playback_sessions
		SET closed_at                = now(),
		    current_position_seconds = CASE WHEN $2 THEN $3 ELSE current_position_seconds END,
		    time_listening_seconds   = time_listening_seconds + $4,
		    last_sync_at             = CASE WHEN $2 THEN now() ELSE last_sync_at END
		WHERE id = $1 AND closed_at IS NULL`,
		id, final != nil, position, listening,
	)
	if err != nil {
		return false, fmt.Errorf("abs_playback_session_store: close: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *ABSPlaybackSessionStore) CloseOpenSessionsForPrincipal(ctx context.Context, userID, profileID string) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("abs_playback_session_store: invalid user id %q: %w", userID, err)
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE abs_playback_sessions
		SET closed_at = now()
		WHERE user_id = $1 AND profile_id = $2 AND closed_at IS NULL`,
		uid, profileID,
	)
	if err != nil {
		return fmt.Errorf("abs_playback_session_store: close principal sessions: %w", err)
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
	if err := rows.Err(); err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats days: %w", err)
	}

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
	if err := dowRows.Err(); err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats dow: %w", err)
	}

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
	if err := mRows.Err(); err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats months: %w", err)
	}
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
