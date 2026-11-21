package planstore

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
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
	recipeJSON, err := json.Marshal(record.FrozenRecipe)
	if err != nil {
		return err
	}
	responseJSON, err := json.Marshal(record.StartResponse)
	if err != nil {
		return err
	}
	desiredJSON, err := marshalDesiredTracks(record.DesiredTracks)
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
		WHERE (playback_attempt_id = $1 OR session_id = NULLIF($2, '')::uuid) AND expires_at <= NOW()`,
		record.PlaybackAttemptID, record.SessionID); err != nil {
		return err
	}
	// The requested media file ID is a historical identity, not a live
	// reference: a virtual candidate rotation can delete the requested file row
	// between the client's request and the attempt persist while the effective
	// file survives. The requested_media_file_id FK was dropped so the raw
	// requested ID is persisted verbatim; the effective FK still guards the
	// live file. The replan path and the idempotency check rely on the raw
	// requested identity surviving the persist.
	result, err := tx.Exec(ctx, `
		INSERT INTO playback_v3_attempts (
			playback_attempt_id, session_id, user_id, profile_id,
			requested_media_file_id, effective_media_file_id,
			current_plan_id, current_replan_request_id, current_plan, frozen_recipe,
			normalized_request, start_response, request_digest, expires_at, server_bitrate_cap_kbps,
			desired_tracks, login_session_id
		) VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT DO NOTHING`,
		record.PlaybackAttemptID, record.SessionID, record.UserID, record.ProfileID,
		record.RequestedMediaFileID, record.EffectiveMediaFileID,
		record.CurrentPlanID, record.CurrentReplanRequestID, planJSON, recipeJSON,
		requestJSON, responseJSON, record.RequestDigest, record.ExpiresAt, record.ServerBitrateCapKbps,
		desiredJSON, record.LoginSessionID)
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
		SELECT playback_attempt_id, COALESCE(session_id::text, ''), user_id, profile_id, login_session_id
		FROM playback_v3_attempts
		WHERE `+predicate+` AND expires_at > NOW()`, value).Scan(
		&identity.PlaybackAttemptID, &identity.SessionID, &identity.UserID, &identity.ProfileID,
		&identity.LoginSessionID,
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
	var planJSON, recipeJSON, requestJSON, responseJSON, sampleJSON, recoveryJSON, selectionJSON, ledgerJSON, desiredJSON []byte
	err := s.db.QueryRow(ctx, `
		SELECT playback_attempt_id, COALESCE(session_id::text, ''), user_id, profile_id,
		       requested_media_file_id, effective_media_file_id,
		       current_plan_id, current_replan_request_id, current_plan, frozen_recipe,
		       normalized_request, start_response, request_digest, expires_at, server_bitrate_cap_kbps,
		       last_sequence, last_sample, stopped_at, COALESCE(stop_receipt->>'stop_id', ''), updated_at,
		       recovery_state, recovery_revision, audio_selection, audio_reconcile_ledger,
		       desired_tracks, login_session_id, created_at
		FROM playback_v3_attempts
		WHERE `+predicate+` AND expires_at > NOW()`, value).Scan(
		&record.PlaybackAttemptID, &record.SessionID, &record.UserID, &record.ProfileID,
		&record.RequestedMediaFileID, &record.EffectiveMediaFileID,
		&record.CurrentPlanID, &record.CurrentReplanRequestID, &planJSON, &recipeJSON,
		&requestJSON, &responseJSON, &record.RequestDigest, &record.ExpiresAt, &record.ServerBitrateCapKbps,
		&record.LastSequence, &sampleJSON, &record.StoppedAt, &record.StopID, &record.LastSampleAt,
		&recoveryJSON, &record.RecoveryRevision, &selectionJSON, &ledgerJSON,
		&desiredJSON, &record.LoginSessionID, &record.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, playback.ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if record.LastSample, err = decodeProgressSample(sampleJSON); err != nil {
		return nil, err
	}
	if len(desiredJSON) > 0 {
		record.DesiredTracks = &playback.DesiredTracksV3{}
		if err := json.Unmarshal(desiredJSON, record.DesiredTracks); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal(planJSON, &record.CurrentPlan); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(recipeJSON, &record.FrozenRecipe); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(requestJSON, &record.NormalizedRequest); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(responseJSON, &record.StartResponse); err != nil {
		return nil, err
	}
	if len(recoveryJSON) > 0 {
		if err := json.Unmarshal(recoveryJSON, &record.RecoveryState); err != nil {
			return nil, err
		}
	}
	// audio_selection and audio_reconcile_ledger postdate the intent work:
	// rows written before the migration carry '{}' and decode to the zero
	// value, which reconciliation treats as "no intent / no decision".
	if len(selectionJSON) > 0 {
		var selection playback.AudioSelectionV3
		if err := json.Unmarshal(selectionJSON, &selection); err != nil {
			return nil, err
		}
		record.SelectionOrigin = selection.Origin
		record.PreferredAudioLanguage = selection.PreferredLanguage
		record.SeriesAudioPreferenceSignature = selection.SeriesSignature
		record.SelectedAudioSignature = selection.SelectedSignature
	}
	if len(ledgerJSON) > 0 {
		if err := json.Unmarshal(ledgerJSON, &record.AudioReconcileLedger); err != nil {
			return nil, err
		}
	}
	return &record, nil
}

// AppendRecoveryExclusions unions confirmed exclusions into the attempt's
// durable recovery_state under a row lock. The transaction serializes
// concurrent appends on the attempt row, and the base_revision compare makes a
// writer that read a stale revision lose with ErrRecoveryRevisionConflictV3
// instead of clobbering a newer chain. The merge itself is the shared
// UnionRecoveryExclusions, so the memory and Postgres stores cannot drift.
func (s *Postgres) AppendRecoveryExclusions(ctx context.Context, sessionID string, baseRevision int64, exclusions []playback.RecoveryExclusionV3) (playback.RecoveryStateV3, int64, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var stateJSON []byte
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT recovery_state, recovery_revision FROM playback_v3_attempts
		WHERE session_id = $1::uuid AND expires_at > NOW()
		FOR UPDATE`, sessionID).Scan(&stateJSON, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return playback.RecoveryStateV3{}, 0, playback.ErrSessionNotFound
	}
	if err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	if baseRevision >= 0 && revision != baseRevision {
		return playback.RecoveryStateV3{}, revision, playback.ErrRecoveryRevisionConflictV3
	}
	var base playback.RecoveryStateV3
	if len(stateJSON) > 0 {
		if err := json.Unmarshal(stateJSON, &base); err != nil {
			return playback.RecoveryStateV3{}, 0, err
		}
	}
	merged := playback.UnionRecoveryExclusions(base, exclusions)
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	var next int64
	if err := tx.QueryRow(ctx, `
		UPDATE playback_v3_attempts
		SET recovery_state = $2, recovery_revision = recovery_revision + 1, updated_at = NOW()
		WHERE session_id = $1::uuid AND expires_at > NOW()
		RETURNING recovery_revision`, sessionID, mergedJSON).Scan(&next); err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	return merged, next, nil
}

func (s *Postgres) AppendRecipeExclusions(ctx context.Context, sessionID string, baseRevision int64, keys []string) (playback.RecoveryStateV3, int64, error) {
	if len(keys) == 0 {
		return playback.RecoveryStateV3{}, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var stateJSON []byte
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT recovery_state, recovery_revision FROM playback_v3_attempts
		WHERE session_id = $1::uuid AND expires_at > NOW()
		FOR UPDATE`, sessionID).Scan(&stateJSON, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return playback.RecoveryStateV3{}, 0, playback.ErrSessionNotFound
	}
	if err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	if baseRevision >= 0 && revision != baseRevision {
		return playback.RecoveryStateV3{}, revision, playback.ErrRecoveryRevisionConflictV3
	}
	var state playback.RecoveryStateV3
	if len(stateJSON) > 0 {
		if err := json.Unmarshal(stateJSON, &state); err != nil {
			return playback.RecoveryStateV3{}, 0, err
		}
	}
	state.RecipeExclusions = playback.UnionRecipeExclusions(state.RecipeExclusions, keys)
	mergedJSON, err := json.Marshal(state)
	if err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	var next int64
	if err := tx.QueryRow(ctx, `
		UPDATE playback_v3_attempts
		SET recovery_state = $2, recovery_revision = recovery_revision + 1, updated_at = NOW()
		WHERE session_id = $1::uuid AND expires_at > NOW()
		RETURNING recovery_revision`, sessionID, mergedJSON).Scan(&next); err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	return state, next, nil
}

// RecordAudioReconciliation appends the settled reconciliation decision to
// the attempt's durable ledger under a row lock. The transaction serializes
// concurrent records on the attempt row, and the base-revision compare makes
// a writer that read a stale revision lose with
// ErrRecoveryRevisionConflictV3 instead of clobbering a newer decision.
// Recording the same (generation, session, decision, digest) twice is a no-op
// returning the stored ledger: retries and second replicas converge instead of
// minting a second event.
func (s *Postgres) RecordAudioReconciliation(ctx context.Context, sessionID string, baseRevision int64, entry playback.AudioReconcileEntryV3) (playback.AudioReconcileLedgerV3, int64, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return playback.AudioReconcileLedgerV3{}, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ledgerJSON []byte
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT audio_reconcile_ledger, COALESCE((audio_reconcile_ledger->>'revision')::bigint, 0) FROM playback_v3_attempts
		WHERE session_id = $1::uuid AND expires_at > NOW()
		FOR UPDATE`, sessionID).Scan(&ledgerJSON, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return playback.AudioReconcileLedgerV3{}, 0, playback.ErrSessionNotFound
	}
	if err != nil {
		return playback.AudioReconcileLedgerV3{}, 0, err
	}
	if baseRevision >= 0 && revision != baseRevision {
		return playback.AudioReconcileLedgerV3{}, revision, playback.ErrRecoveryRevisionConflictV3
	}
	var base playback.AudioReconcileLedgerV3
	if len(ledgerJSON) > 0 {
		if err := json.Unmarshal(ledgerJSON, &base); err != nil {
			return playback.AudioReconcileLedgerV3{}, 0, err
		}
	}
	base.Revision = revision
	merged := playback.AppendAudioReconcileEntry(base, entry)
	if len(merged.Entries) == len(base.Entries) &&
		playback.AudioReconcileLedgerEntriesEqual(merged.Entries, base.Entries) {
		// The same decision is already recorded with the same delivery
		// state: report the stored ledger without taking the write lock's
		// revision. A retry must not look like a new decision to a caller
		// reading the revision it handed back.
		if err := tx.Commit(ctx); err != nil {
			return playback.AudioReconcileLedgerV3{}, 0, err
		}
		return base, revision, nil
	}
	merged.Revision = revision + 1
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return playback.AudioReconcileLedgerV3{}, 0, err
	}
	var next int64
	if err := tx.QueryRow(ctx, `
		UPDATE playback_v3_attempts
		SET audio_reconcile_ledger = $2, updated_at = NOW()
		WHERE session_id = $1::uuid AND expires_at > NOW()
		RETURNING (audio_reconcile_ledger->>'revision')::bigint`, sessionID, mergedJSON).Scan(&next); err != nil {
		return playback.AudioReconcileLedgerV3{}, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return playback.AudioReconcileLedgerV3{}, 0, err
	}
	return merged, next, nil
}

// GetAudioReconcileLedger reads the durable reconciliation ledger and its
// revision.
func (s *Postgres) GetAudioReconcileLedger(ctx context.Context, sessionID string) (playback.AudioReconcileLedgerV3, int64, error) {
	var ledgerJSON []byte
	var revision int64
	err := s.db.QueryRow(ctx, `
		SELECT audio_reconcile_ledger, COALESCE((audio_reconcile_ledger->>'revision')::bigint, 0) FROM playback_v3_attempts
		WHERE session_id = $1::uuid AND expires_at > NOW()`, sessionID).Scan(&ledgerJSON, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return playback.AudioReconcileLedgerV3{}, 0, playback.ErrSessionNotFound
	}
	if err != nil {
		return playback.AudioReconcileLedgerV3{}, 0, err
	}
	var ledger playback.AudioReconcileLedgerV3
	if len(ledgerJSON) > 0 {
		if err := json.Unmarshal(ledgerJSON, &ledger); err != nil {
			return playback.AudioReconcileLedgerV3{}, 0, err
		}
	}
	ledger.Revision = revision
	return ledger, revision, nil
}

// GetRecoveryState reads the durable exclusion chain and its revision.
func (s *Postgres) GetRecoveryState(ctx context.Context, sessionID string) (playback.RecoveryStateV3, int64, error) {
	var stateJSON []byte
	var revision int64
	err := s.db.QueryRow(ctx, `
		SELECT recovery_state, recovery_revision FROM playback_v3_attempts
		WHERE session_id = $1::uuid AND expires_at > NOW()`, sessionID).Scan(&stateJSON, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return playback.RecoveryStateV3{}, 0, playback.ErrSessionNotFound
	}
	if err != nil {
		return playback.RecoveryStateV3{}, 0, err
	}
	var state playback.RecoveryStateV3
	if len(stateJSON) > 0 {
		if err := json.Unmarshal(stateJSON, &state); err != nil {
			return playback.RecoveryStateV3{}, 0, err
		}
	}
	return state, revision, nil
}

func (s *Postgres) BeginReplan(ctx context.Context, sessionID, requestID, digest, baseReplanRequestID string, leaseUntil time.Time) (playback.ReplanLeaseV3, error) {
	leaseToken := uuid.NewString()
	// One retry: if a concurrent writer wins the insert race (possible only
	// when a caller skips the advisory session lock), re-read its row and
	// resolve to a replay/in-flight lease instead of surfacing a raw 23505.
	for attempt := 0; ; attempt++ {
		lease, retry, err := s.beginReplanOnce(ctx, sessionID, requestID, digest, baseReplanRequestID, leaseToken, leaseUntil)
		if retry && attempt == 0 {
			continue
		}
		return lease, err
	}
}

func (s *Postgres) beginReplanOnce(ctx context.Context, sessionID, requestID, digest, baseReplanRequestID, leaseToken string, leaseUntil time.Time) (playback.ReplanLeaseV3, bool, error) {
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
			INSERT INTO playback_v3_replans (session_id, replan_request_id, request_digest, base_replan_request_id, lease_owner, lease_expires_at)
			VALUES ($1::uuid, $2, $3, $4, $5, $6)`, sessionID, requestID, digest, baseReplanRequestID, leaseToken, leaseUntil)
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
		return playback.ReplanLeaseV3{State: playback.ReplanLeaseOwnedV3, LeaseToken: leaseToken}, false, nil
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
	_, err = tx.Exec(ctx, `UPDATE playback_v3_replans SET lease_owner = $3, lease_expires_at = $4, updated_at = NOW() WHERE session_id = $1::uuid AND replan_request_id = $2`, sessionID, requestID, leaseToken, leaseUntil)
	if err != nil {
		return playback.ReplanLeaseV3{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return playback.ReplanLeaseV3{}, false, err
	}
	return playback.ReplanLeaseV3{State: playback.ReplanLeaseOwnedV3, LeaseToken: leaseToken}, false, nil
}

func (s *Postgres) ReleaseReplan(ctx context.Context, sessionID, requestID, leaseToken string) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM playback_v3_replans
		WHERE session_id = $1::uuid AND replan_request_id = $2
		  AND state = 'active' AND lease_owner = $3`,
		sessionID, requestID, leaseToken)
	return err
}

func (s *Postgres) CompleteReplan(ctx context.Context, sessionID, requestID, leaseToken, baseReplanRequestID string, response json.RawMessage, record playback.AttemptRecordV3) error {
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
	recipeJSON, err := json.Marshal(record.FrozenRecipe)
	if err != nil {
		return err
	}
	startResponseJSON, err := json.Marshal(record.StartResponse)
	if err != nil {
		return err
	}
	desiredJSON, err := marshalDesiredTracks(record.DesiredTracks)
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
			current_replan_request_id = $4, current_plan = $5, frozen_recipe = $6,
			normalized_request = $7, start_response = $8, expires_at = $9, desired_tracks = $11,
			updated_at = NOW()
		WHERE session_id = $1::uuid AND current_replan_request_id = $10 AND stopped_at IS NULL`,
		sessionID, record.EffectiveMediaFileID, record.CurrentPlanID, record.CurrentReplanRequestID, planJSON, recipeJSON, requestJSON, startResponseJSON, record.ExpiresAt, baseReplanRequestID, desiredJSON)
	if err != nil {
		return err
	}
	if attemptResult.RowsAffected() != 1 {
		var stopped *time.Time
		if scanErr := tx.QueryRow(ctx, `SELECT stopped_at FROM playback_v3_attempts WHERE session_id = $1::uuid`, sessionID).Scan(&stopped); scanErr == nil {
			if stopped != nil {
				return playback.ErrAttemptStoppedV3
			}
			return playback.ErrReplanSupersededV3
		}
		return playback.ErrSessionNotFound
	}
	replanResult, err := tx.Exec(ctx, `
		UPDATE playback_v3_replans SET state = 'completed', response = $4, updated_at = NOW()
		WHERE session_id = $1::uuid AND replan_request_id = $2
		  AND state = 'active' AND lease_owner = $3`, sessionID, requestID, leaseToken, response)
	if err != nil {
		return err
	}
	if replanResult.RowsAffected() != 1 {
		// The lease row is gone: it was released or cleaned up, or another
		// writer superseded it. Either way the replacement is stale, not a
		// missing session, so the caller can answer the retryable
		// stale_playback_plan. The deferred rollback discards the attempt update
		// above.
		return playback.ErrReplanSupersededV3
	}
	return tx.Commit(ctx)
}

func (s *Postgres) RecordRouteEvent(ctx context.Context, record playback.RouteEventRecordV3) (bool, error) {
	if record.Diagnostics == nil {
		record.Diagnostics = map[string]string{}
	}
	diagnostics, err := json.Marshal(record.Diagnostics)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A v2 report carries an event id: the partial unique index makes a retry
	// after a lost 202 a no-op instead of a second row, which RowsAffected
	// reports as zero. Legacy v1 reports have no id and keep their
	// unconditional insert.
	tag, err := tx.Exec(ctx, `
		INSERT INTO playback_route_events (
			playback_attempt_id, session_id, plan_id, plan_attempt_id, plan_attempt_key,
			event, failure_classification, fallback_reason, output_context_id,
			diagnostics, user_id, profile_id, client_name, client_version, client_build,
			client_channel, client_model, event_id
		) VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''),
		          $6, NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), $10, $11, $12,
		          NULLIF($13, ''), NULLIF($14, ''), NULLIF($15, ''), NULLIF($16, ''),
		          NULLIF($17, ''), NULLIF($18, '')::uuid)
		ON CONFLICT (playback_attempt_id, event_id) WHERE event_id IS NOT NULL DO NOTHING`,
		record.PlaybackAttemptID, record.SessionID, record.PlanID, record.PlanAttemptID, record.PlanAttemptKey,
		record.Event, record.FailureClassification, record.FallbackReason, record.OutputContextID,
		diagnostics, record.UserID, record.ProfileID, record.ClientName, record.ClientVersion,
		record.ClientBuild, record.ClientChannel, record.ClientModel, record.EventID)
	if err != nil {
		return false, err
	}
	inserted := tag.RowsAffected() == 1
	if inserted && record.Event == playback.RouteEventFirstFrameV3 && record.PlanID != "" {
		if _, updateErr := tx.Exec(ctx, `
			UPDATE playback_v3_attempts
			SET current_transport_rendered = true
			WHERE playback_attempt_id = $1 AND current_plan_id = $2`,
			record.PlaybackAttemptID, record.PlanID); updateErr != nil {
			return false, updateErr
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return inserted, nil
}

// Progress and stop sequencing. Both are single-statement compare-and-sets on
// the attempt row, so they need neither the advisory session lock nor a
// transaction: concurrent samples for one attempt serialize on the row lock
// and the losers re-read the committed state to classify themselves.

func decodeProgressSample(data []byte) (*playback.ProgressSampleV3, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var sample playback.ProgressSampleV3
	if err := json.Unmarshal(data, &sample); err != nil {
		return nil, err
	}
	return &sample, nil
}

func (s *Postgres) ApplyProgress(ctx context.Context, sessionID string, sample playback.ProgressSampleV3) (playback.ProgressReceiptV3, error) {
	sampleJSON, err := json.Marshal(sample)
	if err != nil {
		return playback.ProgressReceiptV3{}, err
	}
	// The sample and the session's announced pause state commit together:
	// the attempt's row lock, held to the commit, orders concurrent samples,
	// and the lifecycle statement reads after it, so the announced state is
	// always the latest accepted sample's.
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return playback.ProgressReceiptV3{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `
		UPDATE playback_v3_attempts
		SET last_sequence = $2, last_sample = $3, updated_at = NOW()
		WHERE session_id = $1::uuid AND expires_at > NOW() AND stopped_at IS NULL AND last_sequence < $2`,
		sessionID, sample.Sequence, sampleJSON)
	if err != nil {
		return playback.ProgressReceiptV3{}, err
	}
	if result.RowsAffected() == 1 {
		receipt := playback.ProgressReceiptV3{Outcome: playback.ProgressAppliedV3, Accepted: &sample}
		change := playback.LifecycleChangeV3{Paused: sample.IsPaused}
		state := "playing"
		if sample.IsPaused {
			state = "paused"
		}
		err := tx.QueryRow(ctx, `
			UPDATE playback_session_lifecycle
			SET state = $2, seq = nextval('playback_session_lifecycle_seq'), updated_at = NOW()
			WHERE session_id = $1 AND state NOT IN ('stopped', $2)
			RETURNING seq, updated_at`, sessionID, state).Scan(&change.Seq, &change.At)
		switch {
		case err == nil:
			receipt.Lifecycle = &change
		case !errors.Is(err, pgx.ErrNoRows):
			return playback.ProgressReceiptV3{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return playback.ProgressReceiptV3{}, err
		}
		return receipt, nil
	}
	if err := tx.Rollback(ctx); err != nil {
		return playback.ProgressReceiptV3{}, err
	}
	var lastSequence int64
	var lastJSON []byte
	var stoppedAt *time.Time
	err = s.db.QueryRow(ctx, `
		SELECT last_sequence, last_sample, stopped_at FROM playback_v3_attempts
		WHERE session_id = $1::uuid AND expires_at > NOW()`, sessionID).Scan(&lastSequence, &lastJSON, &stoppedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return playback.ProgressReceiptV3{}, playback.ErrSessionNotFound
	}
	if err != nil {
		return playback.ProgressReceiptV3{}, err
	}
	if stoppedAt != nil {
		return playback.ProgressReceiptV3{}, playback.ErrAttemptStoppedV3
	}
	last, err := decodeProgressSample(lastJSON)
	if err != nil {
		return playback.ProgressReceiptV3{}, err
	}
	return playback.ResolveUnappliedProgressV3(lastSequence, last, sample)
}

func (s *Postgres) StopAttempt(ctx context.Context, sessionID, stopID string, final *playback.ProgressSampleV3) (playback.StopReceiptV3, bool, error) {
	if _, err := uuid.Parse(stopID); err != nil {
		return playback.StopReceiptV3{}, false, playback.ErrInvalidStopIDV3
	}
	var finalSequence *int64
	var finalJSON []byte
	if final != nil {
		finalSequence = &final.Sequence
		var err error
		if finalJSON, err = json.Marshal(final); err != nil {
			return playback.StopReceiptV3{}, false, err
		}
	}
	// SET expressions all read the pre-update row, so the "final sample is
	// newer" test repeats verbatim for the sequence, the sample and the
	// receipt's accepted sample.
	var receiptJSON []byte
	err := s.db.QueryRow(ctx, `
		UPDATE playback_v3_attempts
		SET stopped_at = NOW(),
		    stop_id = $2::uuid,
		    last_sequence = CASE WHEN $3::bigint > last_sequence THEN $3::bigint ELSE last_sequence END,
		    last_sample = CASE WHEN $3::bigint > last_sequence THEN $4::jsonb ELSE last_sample END,
		    stop_receipt = jsonb_strip_nulls(jsonb_build_object(
		        'stop_id', $2::uuid::text,
		        'accepted', CASE WHEN $3::bigint > last_sequence THEN $4::jsonb ELSE last_sample END)),
		    updated_at = NOW()
		WHERE session_id = $1::uuid AND expires_at > NOW() AND stopped_at IS NULL
		RETURNING stop_receipt`, sessionID, stopID, finalSequence, finalJSON).Scan(&receiptJSON)
	if err == nil {
		var receipt playback.StopReceiptV3
		if err := json.Unmarshal(receiptJSON, &receipt); err != nil {
			return playback.StopReceiptV3{}, false, err
		}
		return receipt, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return playback.StopReceiptV3{}, false, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT stop_receipt FROM playback_v3_attempts
		WHERE session_id = $1::uuid AND expires_at > NOW() AND stopped_at IS NOT NULL`, sessionID).Scan(&receiptJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return playback.StopReceiptV3{}, false, playback.ErrSessionNotFound
	}
	if err != nil {
		return playback.StopReceiptV3{}, false, err
	}
	var receipt playback.StopReceiptV3
	if err := json.Unmarshal(receiptJSON, &receipt); err != nil {
		return playback.StopReceiptV3{}, false, err
	}
	return receipt, false, nil
}

// ExpireIdleAttempt stops a live attempt under stopID only while it has
// accepted no progress since idleBefore. The predicate runs under the row
// lock ApplyProgress takes, so a sample that lands first keeps it live.
func (s *Postgres) ExpireIdleAttempt(ctx context.Context, sessionID, stopID string, idleBefore time.Time) (bool, error) {
	if _, err := uuid.Parse(stopID); err != nil {
		return false, playback.ErrInvalidStopIDV3
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE playback_v3_attempts
		SET stopped_at = NOW(),
		    stop_id = $2::uuid,
		    stop_receipt = jsonb_strip_nulls(jsonb_build_object('stop_id', $2::uuid::text, 'accepted', last_sample)),
		    updated_at = NOW()
		WHERE session_id = $1::uuid AND expires_at > NOW() AND stopped_at IS NULL AND updated_at < $3`,
		sessionID, stopID, idleBefore)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Postgres) RecordStopReceipt(ctx context.Context, sessionID string, receipt playback.StopReceiptV3) error {
	if _, err := uuid.Parse(receipt.StopID); err != nil {
		return playback.ErrInvalidStopIDV3
	}
	// The write replaces stop_receipt wholesale, so it also ends the caller's
	// finalization claim: the caller passes FinalizingUntil zero. A finalized
	// receipt completes the stop; an unfinalized one reports a cleanup that did
	// not finish and leaves the row reclaimable for a retry.
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(ctx, `
		UPDATE playback_v3_attempts SET stop_receipt = $2, updated_at = NOW()
		WHERE session_id = $1::uuid AND stopped_at IS NOT NULL AND stop_id = $3::uuid`,
		sessionID, receiptJSON, receipt.StopID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return playback.ErrSessionNotFound
	}
	return nil
}

func (s *Postgres) ClaimStopFinalization(ctx context.Context, sessionID string, leaseUntil time.Time) (bool, error) {
	// One UPDATE claims the lease only when nobody finalized and no live
	// lease exists, so concurrent replays cannot both run the writers.
	result, err := s.db.Exec(ctx, `
		UPDATE playback_v3_attempts
		SET stop_receipt = jsonb_set(stop_receipt, '{finalizing_until}', to_jsonb($2::timestamptz), true),
		    updated_at = NOW()
		WHERE session_id = $1::uuid AND expires_at > NOW() AND stopped_at IS NOT NULL
		  AND COALESCE((stop_receipt->>'finalized')::boolean, false) = false
		  AND COALESCE((stop_receipt->>'finalizing_until')::timestamptz, 'epoch'::timestamptz) < NOW()`,
		sessionID, leaseUntil)
	if err != nil {
		return false, err
	}
	if result.RowsAffected() == 1 {
		return true, nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT true FROM playback_v3_attempts WHERE session_id = $1::uuid AND expires_at > NOW() AND stopped_at IS NOT NULL`, sessionID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, playback.ErrSessionNotFound
		}
		return false, err
	}
	return false, nil
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

// PlanDelivery resolves the reported plan, including one replaced by a replan.
// Looking only at current_plan would attribute the old plan's stalls to its replacement.
func (s *Postgres) PlanDelivery(ctx context.Context, attempt, plan string) (string, error) {
	var delivery string
	err := s.db.QueryRow(ctx, `SELECT p->>'delivery' FROM (
 SELECT start_response->'playback_plan' p FROM playback_v3_attempts WHERE playback_attempt_id=$1
 UNION ALL SELECT current_plan FROM playback_v3_attempts WHERE playback_attempt_id=$1
 UNION ALL SELECT r.response->'playback_plan' FROM playback_v3_replans r JOIN playback_v3_attempts a ON a.session_id=r.session_id WHERE a.playback_attempt_id=$1
 ) plans WHERE p->>'plan_id'=$2 LIMIT 1`, attempt, plan).Scan(&delivery)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return delivery, err
}

// marshalDesiredTracks stores a nil intent as SQL NULL, the value rows
// written before the column existed carry.
func marshalDesiredTracks(desired *playback.DesiredTracksV3) ([]byte, error) {
	if desired == nil {
		return nil, nil
	}
	return json.Marshal(desired)
}
