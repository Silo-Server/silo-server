package planstore

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type Postgres struct {
	db *pgxpool.Pool
}

func NewPostgres(db *pgxpool.Pool) *Postgres { return &Postgres{db: db} }

// SessionLockCapacity reports how many AcquireSessionLock holders the
// underlying pool can sustain concurrently. Each holder pins one pooled
// connection for its advisory-lock transaction while issuing further store
// queries from the same pool, so the bound leaves at least half the pool free
// for those queries and for the rest of the application.
func (s *Postgres) SessionLockCapacity() int {
	if s == nil || s.db == nil {
		return 0
	}
	capacity := int(s.db.Config().MaxConns) / 2
	if capacity < 1 {
		capacity = 1
	}
	return capacity
}

func (s *Postgres) AcquireSessionLock(ctx context.Context, sessionID string) (func(), error) {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		return nil, err
	}
	release := func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := tx.Rollback(rollbackCtx)
		cancel()
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			// Closing the physical connection is the fail-safe for an uncertain
			// rollback; PostgreSQL releases every transaction advisory lock when
			// the backend connection closes.
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = conn.Conn().Close(closeCtx)
			closeCancel()
		}
		conn.Release()
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, sessionID); err != nil {
		release()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(release)
	}, nil
}

func (s *Postgres) SaveAttempt(ctx context.Context, record playback.AttemptRecordV3) error {
	planJSON, err := json.Marshal(record.CurrentPlan)
	if err != nil {
		return err
	}
	requestJSON, err := json.Marshal(record.NormalizedRequest)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Expired rows linger for up to an hour until CleanupExpired runs; they
	// must not wedge a legitimate attempt-ID or session reuse into a
	// conflict that the recovery lookup (which filters expired rows) can
	// never resolve.
	if _, err := tx.Exec(ctx, `
		DELETE FROM playback_v3_attempts
		WHERE (playback_attempt_id = $1 OR session_id = $2::uuid) AND expires_at <= NOW()`,
		record.PlaybackAttemptID, record.SessionID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		INSERT INTO playback_v3_attempts (
			playback_attempt_id, session_id, user_id, profile_id,
			requested_media_file_id, effective_media_file_id,
			current_plan_id, current_replan_request_id, current_plan, normalized_request, request_digest, expires_at
		) VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT DO NOTHING`,
		record.PlaybackAttemptID, record.SessionID, record.UserID, record.ProfileID,
		record.RequestedMediaFileID, record.EffectiveMediaFileID,
		record.CurrentPlanID, record.CurrentReplanRequestID, planJSON, requestJSON, record.RequestDigest, record.ExpiresAt)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		// An attempt-ID reused with different input is an idempotency
		// violation, not a replayable duplicate.
		var digest string
		err := tx.QueryRow(ctx, `
			SELECT request_digest FROM playback_v3_attempts
			WHERE playback_attempt_id = $1 AND expires_at > NOW()`, record.PlaybackAttemptID).Scan(&digest)
		if err == nil && digest != "" && record.RequestDigest != "" && digest != record.RequestDigest {
			return playback.ErrIdempotencyKeyReusedV3
		}
		return playback.ErrPlaybackAttemptExistsV3
	}
	return tx.Commit(ctx)
}

func (s *Postgres) GetAttempt(ctx context.Context, sessionID string) (*playback.AttemptRecordV3, error) {
	return s.getAttempt(ctx, "session_id = $1::uuid", sessionID)
}

func (s *Postgres) GetAttemptByPlaybackAttemptID(ctx context.Context, attemptID string) (*playback.AttemptRecordV3, error) {
	return s.getAttempt(ctx, "playback_attempt_id = $1", attemptID)
}

func (s *Postgres) GetAttemptIdentity(ctx context.Context, sessionID string) (*playback.AttemptIdentityV3, error) {
	return s.getAttemptIdentity(ctx, "session_id = $1::uuid", sessionID)
}

func (s *Postgres) GetAttemptIdentityByPlaybackAttemptID(ctx context.Context, attemptID string) (*playback.AttemptIdentityV3, error) {
	return s.getAttemptIdentity(ctx, "playback_attempt_id = $1", attemptID)
}

// getAttemptIdentity fetches only the ownership columns; route-event
// authorization runs per event and must not pay for the plan JSONB decode.
func (s *Postgres) getAttemptIdentity(ctx context.Context, predicate string, value any) (*playback.AttemptIdentityV3, error) {
	var identity playback.AttemptIdentityV3
	err := s.db.QueryRow(ctx, `
		SELECT playback_attempt_id, session_id::text, user_id, profile_id
		FROM playback_v3_attempts
		WHERE `+predicate+` AND expires_at > NOW()`, value).Scan(
		&identity.PlaybackAttemptID, &identity.SessionID, &identity.UserID, &identity.ProfileID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, playback.ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

func (s *Postgres) getAttempt(ctx context.Context, predicate string, value any) (*playback.AttemptRecordV3, error) {
	var record playback.AttemptRecordV3
	var planJSON, requestJSON []byte
	err := s.db.QueryRow(ctx, `
		SELECT playback_attempt_id, session_id::text, user_id, profile_id,
		       requested_media_file_id, effective_media_file_id,
		       current_plan_id, current_replan_request_id, current_plan, normalized_request, request_digest, expires_at
		FROM playback_v3_attempts
		WHERE `+predicate+` AND expires_at > NOW()`, value).Scan(
		&record.PlaybackAttemptID, &record.SessionID, &record.UserID, &record.ProfileID,
		&record.RequestedMediaFileID, &record.EffectiveMediaFileID,
		&record.CurrentPlanID, &record.CurrentReplanRequestID, &planJSON, &requestJSON, &record.RequestDigest, &record.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, playback.ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(planJSON, &record.CurrentPlan); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(requestJSON, &record.NormalizedRequest); err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *Postgres) BeginReplan(ctx context.Context, sessionID, requestID, digest, baseReplanRequestID string, leaseUntil time.Time) (playback.ReplanLeaseV3, error) {
	// One retry: if a concurrent writer wins the insert race (possible only
	// when a caller skips the advisory session lock), re-read its row and
	// resolve to a replay/in-flight lease instead of surfacing a raw 23505.
	for attempt := 0; ; attempt++ {
		lease, retry, err := s.beginReplanOnce(ctx, sessionID, requestID, digest, baseReplanRequestID, leaseUntil)
		if retry && attempt == 0 {
			continue
		}
		return lease, err
	}
}

func (s *Postgres) beginReplanOnce(ctx context.Context, sessionID, requestID, digest, baseReplanRequestID string, leaseUntil time.Time) (playback.ReplanLeaseV3, bool, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return playback.ReplanLeaseV3{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var existingDigest, existingBase, state string
	var existingLease time.Time
	var response []byte
	err = tx.QueryRow(ctx, `
		SELECT request_digest, base_replan_request_id, state, lease_expires_at, response
		FROM playback_v3_replans
		WHERE session_id = $1::uuid AND replan_request_id = $2
		FOR UPDATE`, sessionID, requestID).Scan(&existingDigest, &existingBase, &state, &existingLease, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `
			INSERT INTO playback_v3_replans (session_id, replan_request_id, request_digest, base_replan_request_id, lease_expires_at)
			VALUES ($1::uuid, $2, $3, $4, $5)`, sessionID, requestID, digest, baseReplanRequestID, leaseUntil)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return playback.ReplanLeaseV3{}, true, nil
			}
			return playback.ReplanLeaseV3{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return playback.ReplanLeaseV3{}, false, err
		}
		return playback.ReplanLeaseV3{State: playback.ReplanLeaseOwnedV3}, false, nil
	}
	if err != nil {
		return playback.ReplanLeaseV3{}, false, err
	}
	if existingDigest != digest {
		return playback.ReplanLeaseV3{}, false, playback.ErrIdempotencyKeyReusedV3
	}
	if state == "completed" {
		if err := tx.Commit(ctx); err != nil {
			return playback.ReplanLeaseV3{}, false, err
		}
		return playback.ReplanLeaseV3{State: playback.ReplanLeaseCompletedV3, Response: response}, false, nil
	}
	if time.Now().Before(existingLease) {
		if err := tx.Commit(ctx); err != nil {
			return playback.ReplanLeaseV3{}, false, err
		}
		return playback.ReplanLeaseV3{State: playback.ReplanLeaseInFlightV3}, false, nil
	}
	if existingBase != baseReplanRequestID {
		return playback.ReplanLeaseV3{}, false, playback.ErrStaleReplanLeaseV3
	}
	_, err = tx.Exec(ctx, `UPDATE playback_v3_replans SET lease_expires_at = $3, updated_at = NOW() WHERE session_id = $1::uuid AND replan_request_id = $2`, sessionID, requestID, leaseUntil)
	if err != nil {
		return playback.ReplanLeaseV3{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return playback.ReplanLeaseV3{}, false, err
	}
	return playback.ReplanLeaseV3{State: playback.ReplanLeaseOwnedV3}, false, nil
}

func (s *Postgres) CompleteReplan(ctx context.Context, sessionID, requestID, baseReplanRequestID string, response json.RawMessage, record playback.AttemptRecordV3) error {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	planJSON, err := json.Marshal(record.CurrentPlan)
	if err != nil {
		return err
	}
	requestJSON, err := json.Marshal(record.NormalizedRequest)
	if err != nil {
		return err
	}
	// The base-revision predicate makes the commit a true compare-and-swap:
	// under the advisory session lock it never fails, but a skipped or broken
	// lock must surface as a conflict rather than silently last-writer-win
	// the durable plan.
	attemptResult, err := tx.Exec(ctx, `
		UPDATE playback_v3_attempts SET
			effective_media_file_id = $2, current_plan_id = $3,
			current_replan_request_id = $4, current_plan = $5, normalized_request = $6, expires_at = $7, updated_at = NOW()
		WHERE session_id = $1::uuid AND current_replan_request_id = $8`,
		sessionID, record.EffectiveMediaFileID, record.CurrentPlanID, record.CurrentReplanRequestID, planJSON, requestJSON, record.ExpiresAt, baseReplanRequestID)
	if err != nil {
		return err
	}
	if attemptResult.RowsAffected() != 1 {
		var exists bool
		if scanErr := tx.QueryRow(ctx, `SELECT true FROM playback_v3_attempts WHERE session_id = $1::uuid`, sessionID).Scan(&exists); scanErr == nil {
			return playback.ErrReplanSupersededV3
		}
		return playback.ErrSessionNotFound
	}
	replanResult, err := tx.Exec(ctx, `
		UPDATE playback_v3_replans SET state = 'completed', response = $3, updated_at = NOW()
		WHERE session_id = $1::uuid AND replan_request_id = $2`, sessionID, requestID, response)
	if err != nil {
		return err
	}
	if replanResult.RowsAffected() != 1 {
		return playback.ErrSessionNotFound
	}
	return tx.Commit(ctx)
}

func (s *Postgres) RecordRouteEvent(ctx context.Context, record playback.RouteEventRecordV3) error {
	if record.Diagnostics == nil {
		record.Diagnostics = map[string]string{}
	}
	diagnostics, err := json.Marshal(record.Diagnostics)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO playback_route_events (
			playback_attempt_id, session_id, plan_id, plan_attempt_id, plan_attempt_key,
			event, failure_classification, fallback_reason, output_route_generation,
			diagnostics, user_id, profile_id, client_name, client_version, client_model
		) VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''),
		          $6, NULLIF($7, ''), NULLIF($8, ''), $9, $10, $11, $12,
		          NULLIF($13, ''), NULLIF($14, ''), NULLIF($15, ''))`,
		record.PlaybackAttemptID, record.SessionID, record.PlanID, record.PlanAttemptID, record.PlanAttemptKey,
		record.Event, record.FailureClassification, record.FallbackReason, record.OutputRouteGeneration,
		diagnostics, record.UserID, record.ProfileID, record.ClientName, record.ClientVersion, record.ClientModel)
	return err
}

func (s *Postgres) CleanupExpired(ctx context.Context, now time.Time) (int64, error) {
	if _, err := s.db.Exec(ctx, `DELETE FROM playback_route_events WHERE received_at < $1`, now.Add(-30*24*time.Hour)); err != nil {
		return 0, err
	}
	result, err := s.db.Exec(ctx, `DELETE FROM playback_v3_attempts WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}
