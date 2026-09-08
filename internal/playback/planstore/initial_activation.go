package planstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Control transactions never call a selected source or worker. Registration is
// locked first, then the attempt; database time is sampled after both locks.
// Binding.Scope.MediaItemID is the trusted caller-resolved progress target;
// these control transactions do not resolve catalog file-to-target identity.
type initialActivationRow struct {
	activation    *playback.InitialActivationV3
	authority     playback.AttemptAuthorityV3
	userID        int
	profileID     string
	sessionID     string
	expiresAt     time.Time
	grantNotAfter *time.Time
	now           time.Time
	admitting     bool
}

func (s *Postgres) withInitialActivation(ctx context.Context, binding playback.InitialActivationBindingV3, mutate func(pgx.Tx, *initialActivationRow) (playback.InitialActivationV3, error)) (playback.InitialActivationV3, error) {
	var zero playback.InitialActivationV3
	if err := binding.Validate(); err != nil {
		return zero, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer rollbackAuthority(tx)
	var backend, sourceID, admissionID, admissionState string
	var generation int64
	err = tx.QueryRow(ctx, `SELECT backend,source_id::text,selection_generation,admission_id::text,admission_state FROM playback_source_registrations WHERE user_id=$1 FOR UPDATE`, binding.Source.AccountID).Scan(&backend, &sourceID, &generation, &admissionID, &admissionState)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, playback.ErrInitialActivationUnavailableV3
	}
	if err != nil {
		return zero, err
	}
	row := initialActivationRow{admitting: backend == binding.Source.Backend && sourceID == binding.Source.SourceID && generation == binding.Source.SelectionGeneration && admissionID == binding.AdmissionID && admissionState == "admitting"}
	var data []byte
	row.authority.PlaybackAttemptID = binding.Fence.AttemptID
	err = tx.QueryRow(ctx, `SELECT user_id,profile_id,COALESCE(session_id::text,''),control_state,COALESCE(control_owner::text,''),COALESCE(control_incarnation::text,''),control_epoch,COALESCE(control_lease_expires_at,'epoch'::timestamptz),expires_at,control_grant_not_after,control_activation FROM playback_v3_attempts WHERE playback_attempt_id=$1 FOR UPDATE`, binding.Fence.AttemptID).Scan(&row.userID, &row.profileID, &row.sessionID, &row.authority.State, &row.authority.OwnerID, &row.authority.Incarnation, &row.authority.Epoch, &row.authority.LeaseExpiresAt, &row.expiresAt, &row.grantNotAfter, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, playback.ErrInitialActivationConflictV3
	}
	if err != nil {
		return zero, err
	}
	if row.userID != binding.Source.AccountID || row.profileID != binding.Scope.ProfileID || row.authority.OwnerID != binding.Fence.OwnerID || row.authority.Incarnation != binding.Fence.Incarnation || row.authority.Epoch != binding.Fence.Epoch {
		return zero, playback.ErrInitialActivationConflictV3
	}
	if len(data) > initialActivationDocumentLimit {
		return zero, playback.ErrInitialActivationInvalidV3
	}
	if len(data) > 0 {
		row.activation = new(playback.InitialActivationV3)
		if err := json.Unmarshal(data, row.activation); err != nil {
			return zero, err
		}
		if err := validateStoredInitialActivation(*row.activation); err != nil {
			return zero, err
		}
		if row.sessionID != binding.Scope.SessionID {
			return zero, playback.ErrInitialActivationConflictV3
		}
		if row.activation.Binding != binding {
			return zero, playback.ErrInitialActivationConflictV3
		}
	}
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&row.now); err != nil {
		return zero, err
	}
	result, err := mutate(tx, &row)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return result, nil
}
func (r *initialActivationRow) live() bool {
	return r.authority.LeaseExpiresAt.After(r.now) && r.expiresAt.After(r.now)
}
func saveInitialActivation(ctx context.Context, tx pgx.Tx, state playback.InitialActivationV3) error {
	if err := validateStoredInitialActivation(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > initialActivationDocumentLimit {
		return playback.ErrInitialActivationInvalidV3
	}
	_, err = tx.Exec(ctx, `UPDATE playback_v3_attempts SET control_activation=$2,updated_at=clock_timestamp() WHERE playback_attempt_id=$1`, state.Binding.Fence.AttemptID, data)
	return err
}

func (s *Postgres) BeginInitialActivation(ctx context.Context, binding playback.InitialActivationBindingV3) (playback.InitialActivationV3, error) {
	return s.withInitialActivation(ctx, binding, func(tx pgx.Tx, row *initialActivationRow) (playback.InitialActivationV3, error) {
		var zero playback.InitialActivationV3
		if !row.admitting || !row.live() || row.authority.State != playback.AttemptPreparingV3 {
			return zero, playback.ErrInitialActivationConflictV3
		}
		if row.activation != nil {
			if row.activation.Phase != playback.InitialActivationPendingV3 && row.activation.Phase != playback.InitialActivationInstalledV3 {
				return zero, playback.ErrInitialActivationConflictV3
			}
			return *row.activation, nil
		}
		if binding.ClientTimeline != (playback.ClientPlaybackTimelineV3{}) {
			// withInitialActivation holds the account registration lock. Another
			// attempt cannot pass this barrier concurrently, even on another API.
			sourceJSON, err := json.Marshal(binding.Source)
			if err != nil {
				return zero, err
			}
			var pending bool
			err = tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM playback_v3_attempts
 WHERE user_id=$1 AND profile_id=$2 AND playback_attempt_id<>$3
 AND control_activation->'binding'->'source'=$4::jsonb
 AND control_activation->'binding'->'scope'->>'MediaItemID'=$5
 AND control_activation->'binding'->'client_timeline' IS NOT NULL
 AND NOT (control_activation->>'phase' IN ('stopped','aborted')
 AND control_activation->'terminal' IS NOT NULL
 AND control_activation->'terminal'<>'null'::jsonb))`, binding.Source.AccountID, binding.Scope.ProfileID, binding.Fence.AttemptID, sourceJSON, binding.Scope.MediaItemID).Scan(&pending)
			if err != nil {
				return zero, err
			}
			if pending {
				return zero, playback.ErrClientPlaybackTimelineBusyV3
			}
		}
		// An unbound execution must not be adopted into an initial intent whose
		// source installation has not yet been acknowledged.
		if row.grantNotAfter != nil {
			return zero, playback.ErrInitialActivationConflictV3
		}
		if row.sessionID != "" && row.sessionID != binding.Scope.SessionID {
			return zero, playback.ErrInitialActivationConflictV3
		}
		state := playback.InitialActivationV3{Binding: binding, Phase: playback.InitialActivationPendingV3}
		if _, err := tx.Exec(ctx, `UPDATE playback_v3_attempts SET session_id=$2::uuid WHERE playback_attempt_id=$1`, binding.Fence.AttemptID, binding.Scope.SessionID); err != nil {
			return zero, err
		}
		return state, saveInitialActivation(ctx, tx, state)
	})
}
func (s *Postgres) ReadInitialActivation(ctx context.Context, binding playback.InitialActivationBindingV3) (playback.InitialActivationV3, error) {
	return s.withInitialActivation(ctx, binding, func(_ pgx.Tx, row *initialActivationRow) (playback.InitialActivationV3, error) {
		if row.activation == nil {
			return playback.InitialActivationV3{}, playback.ErrInitialActivationConflictV3
		}
		return *row.activation, nil
	})
}
func (s *Postgres) AcknowledgeInitialInstallation(ctx context.Context, binding playback.InitialActivationBindingV3, observed playback.InitialActivationReceiptV3) (playback.InitialActivationV3, error) {
	receipt, err := observed.StateFor(binding)
	if err != nil {
		return playback.InitialActivationV3{}, err
	}
	return s.withInitialActivation(ctx, binding, func(tx pgx.Tx, row *initialActivationRow) (playback.InitialActivationV3, error) {
		var zero playback.InitialActivationV3
		if !row.admitting || !row.live() || row.authority.State != playback.AttemptPreparingV3 || row.activation == nil {
			return zero, playback.ErrInitialActivationConflictV3
		}
		if err := playback.ValidateInitialInstallV3(binding, receipt); err != nil {
			return zero, err
		}
		state := *row.activation
		if state.Phase == playback.InitialActivationInstalledV3 {
			if !reflect.DeepEqual(state.Install, &receipt) {
				return zero, playback.ErrInitialActivationInvalidV3
			}
			return state, nil
		}
		if state.Phase != playback.InitialActivationPendingV3 {
			return zero, playback.ErrInitialActivationConflictV3
		}
		state.Phase = playback.InitialActivationInstalledV3
		state.Install = &receipt
		return state, saveInitialActivation(ctx, tx, state)
	})
}

// CancelInitialActivation records the captured live owner's durable cancellation.
// It does not grant a reconciler renewed publication or execution authority.
func (s *Postgres) CancelInitialActivation(ctx context.Context, binding playback.InitialActivationBindingV3, abortID string) (playback.InitialActivationV3, error) {
	return s.abortInitialActivation(ctx, binding, abortID, true)
}
func (s *Postgres) AbortInitialActivation(ctx context.Context, binding playback.InitialActivationBindingV3, abortID string) (playback.InitialActivationV3, error) {
	return s.abortInitialActivation(ctx, binding, abortID, false)
}
func (s *Postgres) abortInitialActivation(ctx context.Context, binding playback.InitialActivationBindingV3, abortID string, ownerCancellation bool) (playback.InitialActivationV3, error) {
	if !validInitialUUID(abortID) {
		return playback.InitialActivationV3{}, playback.ErrInitialActivationInvalidV3
	}
	return s.withInitialActivation(ctx, binding, func(tx pgx.Tx, row *initialActivationRow) (playback.InitialActivationV3, error) {
		var zero playback.InitialActivationV3
		if row.activation == nil {
			return zero, playback.ErrInitialActivationConflictV3
		}
		state := *row.activation
		if state.Phase == playback.InitialActivationAbortingV3 || state.Phase == playback.InitialActivationAbortedV3 {
			if state.AbortID != abortID {
				return zero, playback.ErrInitialActivationConflictV3
			}
			return state, nil
		}
		eligible := !row.admitting || !row.live()
		if ownerCancellation {
			eligible = row.live() && row.authority.State == playback.AttemptPreparingV3
		}
		if (state.Phase != playback.InitialActivationPendingV3 && state.Phase != playback.InitialActivationInstalledV3) || !eligible {
			return zero, playback.ErrInitialActivationConflictV3
		}
		if !ownerCancellation && row.admitting && !row.live() {
			state.AbortReason = playback.InitialAbortOwnerLostV3
		}
		state.Phase = playback.InitialActivationAbortingV3
		state.AbortID = abortID
		deadline, err := initialRecoveryDrain(ctx, tx, row, binding.Scope.SessionID)
		if err != nil {
			return zero, err
		}
		state.DrainNotBefore = deadline
		if _, err := tx.Exec(ctx, `UPDATE playback_v3_attempts SET control_state='draining',control_drain_not_before=$2 WHERE playback_attempt_id=$1`, binding.Fence.AttemptID, state.DrainNotBefore); err != nil {
			return zero, err
		}
		return state, saveInitialActivation(ctx, tx, state)
	})
}
func (s *Postgres) CompleteInitialAbort(ctx context.Context, binding playback.InitialActivationBindingV3, abortID string, observed playback.InitialActivationReceiptV3) (playback.InitialActivationV3, error) {
	receipt, err := observed.StateFor(binding)
	if err != nil {
		return playback.InitialActivationV3{}, err
	}
	return s.withInitialActivation(ctx, binding, func(tx pgx.Tx, row *initialActivationRow) (playback.InitialActivationV3, error) {
		var zero playback.InitialActivationV3
		if row.activation == nil {
			return zero, playback.ErrInitialActivationConflictV3
		}
		state := *row.activation
		if state.AbortReason == playback.InitialAbortOwnerLostV3 && !row.admitting {
			return zero, playback.ErrInitialActivationConflictV3
		}
		if state.AbortID == abortID && state.DrainNotBefore.After(row.now) {
			return zero, playback.ErrPlaybackRecoveryDrainingV3
		}
		if state.AbortID != abortID || (state.Phase != playback.InitialActivationAbortingV3 && state.Phase != playback.InitialActivationAbortedV3) || state.DrainNotBefore.After(row.now) {
			return zero, playback.ErrInitialActivationConflictV3
		}
		if err := playback.ValidateInitialTerminalV3(binding, receipt); err != nil {
			return zero, err
		}
		if state.AbortReason == playback.InitialAbortOwnerLostV3 && receipt.Stop.StopID != state.AbortID {
			return zero, playback.ErrInitialActivationConflictV3
		}
		if state.Phase == playback.InitialActivationAbortedV3 {
			if !reflect.DeepEqual(state.Terminal, &receipt) {
				return zero, playback.ErrInitialActivationInvalidV3
			}
			return state, nil
		}
		state.Phase = playback.InitialActivationAbortedV3
		state.Terminal = &receipt
		if _, err := tx.Exec(ctx, `UPDATE playback_v3_attempts SET control_state='stopped',expires_at=CASE WHEN $2 THEN GREATEST(expires_at,clock_timestamp()+$3*interval '1 microsecond') ELSE expires_at END WHERE playback_attempt_id=$1`, binding.Fence.AttemptID, state.AbortReason == playback.InitialAbortOwnerLostV3, playback.MaxTokenTTL.Microseconds()); err != nil {
			return zero, err
		}
		return state, saveInitialActivation(ctx, tx, state)
	})
}

func validInitialUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

var _ playback.InitialActivationStoreV3 = (*Postgres)(nil)

func (s *Postgres) PublishInitialActivation(ctx context.Context, binding playback.InitialActivationBindingV3, record playback.AttemptRecordV3) (playback.InitialActivationV3, error) {
	return s.withInitialActivation(ctx, binding, func(tx pgx.Tx, row *initialActivationRow) (playback.InitialActivationV3, error) {
		var zero playback.InitialActivationV3
		if !row.admitting || !row.live() || row.activation == nil {
			return zero, playback.ErrInitialActivationConflictV3
		}
		state := *row.activation
		if (state.Phase != playback.InitialActivationInstalledV3 || row.authority.State != playback.AttemptPreparingV3) && (state.Phase != playback.InitialActivationActivatedV3 || row.authority.State != playback.AttemptActiveV3) {
			return zero, playback.ErrInitialActivationConflictV3
		}
		if record.PlaybackAttemptID != binding.Fence.AttemptID || record.SessionID != binding.Scope.SessionID || record.UserID != binding.Source.AccountID || record.ProfileID != binding.Scope.ProfileID || record.CurrentReplanRequestID != "" || record.StartResponse.Outcome != playback.OutcomePlayableV3 || record.StartResponse.SessionID != record.SessionID || record.CurrentPlan.SessionID != record.SessionID || record.CurrentPlanID == "" || record.CurrentPlanID != record.CurrentPlan.PlanID || record.CurrentPlan.RequestedMediaFileID != record.RequestedMediaFileID || record.CurrentPlan.EffectiveMediaFileID != record.EffectiveMediaFileID || record.StartResponse.PlaybackPlan == nil || !reflect.DeepEqual(*record.StartResponse.PlaybackPlan, record.CurrentPlan) || !record.FrozenRecipe.ValidFor(record.CurrentPlan) {
			return zero, playback.ErrInitialActivationInvalidV3
		}
		plan, err := json.Marshal(record.CurrentPlan)
		if err != nil {
			return zero, err
		}
		recipe, err := json.Marshal(record.FrozenRecipe)
		if err != nil {
			return zero, err
		}
		response, err := json.Marshal(record.StartResponse)
		if err != nil {
			return zero, err
		}
		normalized, err := json.Marshal(record.NormalizedRequest)
		if err != nil {
			return zero, err
		}
		// Replay compares the durable decision as JSONB, preserving JSON semantics
		// after round trips through PostgreSQL rather than Go allocation details.
		if state.Phase == playback.InitialActivationActivatedV3 {
			var equal bool
			err := tx.QueryRow(ctx, `SELECT session_id=$2::uuid AND effective_media_file_id=$3 AND current_plan_id=$4 AND current_plan=$5::jsonb AND frozen_recipe=$6::jsonb AND start_response=$7::jsonb AND requested_media_file_id=$8 AND request_digest=$9 AND normalized_request=$10::jsonb FROM playback_v3_attempts WHERE playback_attempt_id=$1`, binding.Fence.AttemptID, record.SessionID, record.EffectiveMediaFileID, record.CurrentPlanID, plan, recipe, response, record.RequestedMediaFileID, record.RequestDigest, normalized).Scan(&equal)
			if err != nil {
				return zero, err
			}
			if !equal {
				return zero, playback.ErrInitialActivationConflictV3
			}
			return state, nil
		}
		tag, err := tx.Exec(ctx, `UPDATE playback_v3_attempts SET effective_media_file_id=$3,current_plan_id=$4,current_plan=$5,frozen_recipe=$6,start_response=$7,control_state='active',updated_at=clock_timestamp()
   WHERE playback_attempt_id=$1 AND session_id=$2::uuid AND requested_media_file_id=$8 AND request_digest=$9 AND normalized_request=$10::jsonb
   AND (control_route IS NULL OR (effective_media_file_id=$3 AND current_plan_id=$4 AND current_plan=$5::jsonb AND frozen_recipe=$6::jsonb))`, binding.Fence.AttemptID, record.SessionID, record.EffectiveMediaFileID, record.CurrentPlanID, plan, recipe, response, record.RequestedMediaFileID, record.RequestDigest, normalized)
		if err != nil {
			return zero, err
		}
		if tag.RowsAffected() != 1 {
			return zero, playback.ErrInitialActivationConflictV3
		}
		state.Phase = playback.InitialActivationActivatedV3
		return state, saveInitialActivation(ctx, tx, state)
	})
}

const initialActivationDocumentLimit = 256 * 1024

func validateStoredInitialActivation(state playback.InitialActivationV3) error {
	if state.AbortReason != "" && (state.AbortReason != playback.InitialAbortOwnerLostV3 || (state.Phase != playback.InitialActivationAbortingV3 && state.Phase != playback.InitialActivationAbortedV3)) {
		return playback.ErrInitialActivationInvalidV3
	}
	if err := state.Binding.Validate(); err != nil {
		return err
	}
	switch state.Phase {
	case playback.InitialActivationPendingV3:
		if state.Install != nil || state.Terminal != nil || state.AbortID != "" || state.StopID != "" || !state.DrainNotBefore.IsZero() {
			return playback.ErrInitialActivationInvalidV3
		}
	case playback.InitialActivationInstalledV3, playback.InitialActivationActivatedV3:
		if state.Install == nil || state.Terminal != nil || state.AbortID != "" || state.StopID != "" || !state.DrainNotBefore.IsZero() {
			return playback.ErrInitialActivationInvalidV3
		}
	case playback.InitialActivationAbortingV3, playback.InitialActivationAbortedV3:
		if state.StopID != "" || !validInitialUUID(state.AbortID) || state.DrainNotBefore.IsZero() || (state.Phase == playback.InitialActivationAbortedV3) != (state.Terminal != nil) {
			return playback.ErrInitialActivationInvalidV3
		}
	case playback.InitialActivationStoppingV3, playback.InitialActivationStoppedV3:
		if state.Install == nil || state.AbortID != "" || state.StopID == "" || state.DrainNotBefore.IsZero() || (state.Phase == playback.InitialActivationStoppedV3) != (state.Terminal != nil) {
			return playback.ErrInitialActivationInvalidV3
		}
		if state.Terminal != nil && (state.Terminal.Stop == nil || state.Terminal.Stop.StopID != state.StopID) {
			return playback.ErrInitialActivationInvalidV3
		}
	default:
		return playback.ErrInitialActivationInvalidV3
	}
	if state.Install != nil {
		if err := playback.ValidateInitialInstallV3(state.Binding, *state.Install); err != nil {
			return err
		}
	}
	if state.AbortReason == playback.InitialAbortOwnerLostV3 && state.Terminal != nil && (state.Terminal.Stop == nil || state.Terminal.Stop.StopID != state.AbortID) {
		return playback.ErrInitialActivationInvalidV3
	}
	if state.Terminal != nil {
		if err := playback.ValidateInitialTerminalV3(state.Binding, *state.Terminal); err != nil {
			return err
		}
	}
	return nil
}
