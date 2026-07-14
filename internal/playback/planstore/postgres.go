package planstore

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type Postgres struct {
	db *pgxpool.Pool
}

func NewPostgres(db *pgxpool.Pool) *Postgres { return &Postgres{db: db} }

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
	result, err := s.db.Exec(ctx, `
		INSERT INTO playback_v3_attempts (
			playback_attempt_id, session_id, user_id, profile_id,
			requested_media_file_id, effective_media_file_id,
			current_plan_id, current_replan_request_id, current_plan, normalized_request, expires_at
		) VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (playback_attempt_id) DO NOTHING`,
		record.PlaybackAttemptID, record.SessionID, record.UserID, record.ProfileID,
		record.RequestedMediaFileID, record.EffectiveMediaFileID,
		record.CurrentPlanID, record.CurrentReplanRequestID, planJSON, requestJSON, record.ExpiresAt)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return playback.ErrPlaybackAttemptExistsV3
	}
	return nil
}

func (s *Postgres) GetAttempt(ctx context.Context, sessionID string) (*playback.AttemptRecordV3, error) {
	return s.getAttempt(ctx, "session_id = $1::uuid", sessionID)
}

func (s *Postgres) GetAttemptByPlaybackAttemptID(ctx context.Context, attemptID string) (*playback.AttemptRecordV3, error) {
	return s.getAttempt(ctx, "playback_attempt_id = $1", attemptID)
}

func (s *Postgres) getAttempt(ctx context.Context, predicate string, value any) (*playback.AttemptRecordV3, error) {
	var record playback.AttemptRecordV3
	var planJSON, requestJSON []byte
	err := s.db.QueryRow(ctx, `
		SELECT playback_attempt_id, session_id::text, user_id, profile_id,
		       requested_media_file_id, effective_media_file_id,
		       current_plan_id, current_replan_request_id, current_plan, normalized_request, expires_at
		FROM playback_v3_attempts
		WHERE `+predicate+` AND expires_at > NOW()`, value).Scan(
		&record.PlaybackAttemptID, &record.SessionID, &record.UserID, &record.ProfileID,
		&record.RequestedMediaFileID, &record.EffectiveMediaFileID,
		&record.CurrentPlanID, &record.CurrentReplanRequestID, &planJSON, &requestJSON, &record.ExpiresAt,
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
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return playback.ReplanLeaseV3{}, err
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
			return playback.ReplanLeaseV3{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return playback.ReplanLeaseV3{}, err
		}
		return playback.ReplanLeaseV3{State: playback.ReplanLeaseOwnedV3}, nil
	}
	if err != nil {
		return playback.ReplanLeaseV3{}, err
	}
	if existingDigest != digest {
		return playback.ReplanLeaseV3{}, playback.ErrIdempotencyKeyReusedV3
	}
	if state == "completed" {
		if err := tx.Commit(ctx); err != nil {
			return playback.ReplanLeaseV3{}, err
		}
		return playback.ReplanLeaseV3{State: playback.ReplanLeaseCompletedV3, Response: response}, nil
	}
	if time.Now().Before(existingLease) {
		if err := tx.Commit(ctx); err != nil {
			return playback.ReplanLeaseV3{}, err
		}
		return playback.ReplanLeaseV3{State: playback.ReplanLeaseInFlightV3}, nil
	}
	if existingBase != baseReplanRequestID {
		return playback.ReplanLeaseV3{}, playback.ErrStaleReplanLeaseV3
	}
	_, err = tx.Exec(ctx, `UPDATE playback_v3_replans SET lease_expires_at = $3, updated_at = NOW() WHERE session_id = $1::uuid AND replan_request_id = $2`, sessionID, requestID, leaseUntil)
	if err != nil {
		return playback.ReplanLeaseV3{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return playback.ReplanLeaseV3{}, err
	}
	return playback.ReplanLeaseV3{State: playback.ReplanLeaseOwnedV3}, nil
}

func (s *Postgres) CompleteReplan(ctx context.Context, sessionID, requestID string, response json.RawMessage, record playback.AttemptRecordV3) error {
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
	attemptResult, err := tx.Exec(ctx, `
		UPDATE playback_v3_attempts SET
			effective_media_file_id = $2, current_plan_id = $3,
			current_replan_request_id = $4, current_plan = $5, normalized_request = $6, expires_at = $7, updated_at = NOW()
		WHERE session_id = $1::uuid`, sessionID, record.EffectiveMediaFileID, record.CurrentPlanID, record.CurrentReplanRequestID, planJSON, requestJSON, record.ExpiresAt)
	if err != nil {
		return err
	}
	if attemptResult.RowsAffected() != 1 {
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
