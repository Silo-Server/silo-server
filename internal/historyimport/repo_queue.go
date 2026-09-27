package historyimport

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrSourceDisabled           = errors.New("history import source is disabled")
	ErrRunNotCancelable         = errors.New("history import run is already completed or failed")
	ErrRunClaimLost             = errors.New("history import execution no longer owns this run")
	ErrRunConfigurationChanged  = errors.New("history import source or mapping changed; start a new run after reviewing the configuration")
	ErrRunCancellationRequested = errors.New("history import cancellation requested")
	ErrBulkRunTooLarge          = errors.New("source has more than 200 mappings; start individual mapping runs")
)

const MaxBulkAdminRuns = 200

const LegacyDispatchUnavailableMessage = "Import cannot resume because durable dispatch metadata is unavailable. Review the source and start a new run."

// RunClaim is execution authority, never part of the public run representation.
// Generation zero is reserved for non-durable personal executions.
type RunClaim struct {
	DispatchKind    string
	RunID           string
	Generation      int64
	SourceID        int
	SourceRevision  int64
	MappingID       int
	MappingRevision int64
	ExternalUserID  string
}

// EnqueueAdminRun commits immutable intent; it never contacts an upstream server.
func (r *Repository) EnqueueAdminRun(ctx context.Context, mappingID int) (*Run, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var sourceID int
	if err = tx.QueryRow(ctx, `SELECT source_id FROM history_import_user_mappings WHERE id=$1`, mappingID).Scan(&sourceID); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMappingNotFound
	} else if err != nil {
		return nil, err
	}
	// Same source -> mapping lock order as configuration editors. Source SHARE
	// also prevents token/config changes between snapshotting and commit.
	var sourceType string
	var sourceRevision int64
	var enabled, hasToken bool
	err = tx.QueryRow(ctx, `SELECT source_type,revision,enabled,COALESCE(admin_token,'')<>'' FROM history_import_sources WHERE id=$1 FOR SHARE`, sourceID).Scan(&sourceType, &sourceRevision, &enabled, &hasToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrSourceDisabled
	}
	if !hasToken {
		return nil, ErrNoAdminToken
	}
	if sourceType != SourceTypeEmby && sourceType != SourceTypeJellyfin && sourceType != SourceTypePlex {
		return nil, fmt.Errorf("unsupported source type")
	}
	var userID, lockedSourceID int
	var profileID, externalID string
	var mappingRevision int64
	err = tx.QueryRow(ctx, `SELECT source_id,silo_user_id,silo_profile_id,external_user_id,revision FROM history_import_user_mappings WHERE id=$1 FOR UPDATE`, mappingID).Scan(&lockedSourceID, &userID, &profileID, &externalID, &mappingRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMappingNotFound
	}
	if err != nil {
		return nil, err
	}
	if sourceID != lockedSourceID {
		return nil, ErrRunConfigurationChanged
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM history_import_runs WHERE mapping_id=$1 AND status IN ('queued','running'))`, mappingID).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, ErrActiveRunExists
	}
	id := uuid.NewString()
	_, err = tx.Exec(ctx, `INSERT INTO history_import_runs(id,user_id,profile_id,source_type,connection_mode,status,mapping_id,
 dispatch_version,dispatch_source_id,dispatch_source_revision,dispatch_mapping_id,dispatch_mapping_revision,dispatch_external_user_id)
 VALUES($1,$2,$3,$4,'admin_token','queued',$5,1,$6,$7,$5,$8,$9)`, id, userID, profileID, sourceType, mappingID, sourceID, sourceRevision, mappingRevision, externalID)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return nil, ErrActiveRunExists
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.GetRunByID(ctx, id)
}

func (r *Repository) claimAdminRun(ctx context.Context) (*Run, RunClaim, error) {
	return r.claimDispatchRun(ctx, false)
}

func (r *Repository) claimQueuedRun(ctx context.Context) (*Run, RunClaim, error) {
	return r.claimDispatchRun(ctx, true)
}

func (r *Repository) claimDispatchRun(ctx context.Context, includePersonal bool) (*Run, RunClaim, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, RunClaim{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	run := &Run{}
	claim := RunClaim{}
	err = tx.QueryRow(ctx, `SELECT id,user_id,profile_id,source_type,connection_mode,mapping_id,COALESCE(dispatch_source_id,0),COALESCE(dispatch_source_revision,0),
 COALESCE(dispatch_mapping_id,0),COALESCE(dispatch_mapping_revision,0),COALESCE(dispatch_external_user_id,''),dispatch_kind,created_at FROM history_import_runs
 WHERE status='queued' AND ((dispatch_kind='admin' AND dispatch_version=1) OR ($1 AND dispatch_kind='personal' AND dispatch_version=2)) AND cancel_requested_at IS NULL
 ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, includePersonal).Scan(&run.ID, &run.UserID, &run.ProfileID, &run.SourceType, &run.ConnectionMode, &run.MappingID, &claim.SourceID, &claim.SourceRevision, &claim.MappingID, &claim.MappingRevision, &claim.ExternalUserID, &claim.DispatchKind, &run.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, RunClaim{}, nil
	}
	if err != nil {
		return nil, RunClaim{}, err
	}
	claim.RunID = run.ID
	if claim.DispatchKind == dispatchKindPersonal {
		var usable bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM history_import_run_credentials WHERE run_id=$1 AND envelope_version=$2)`, run.ID, personalCredentialVersion).Scan(&usable)
		if err != nil {
			return nil, RunClaim{}, err
		}
		if !usable {
			// Constraints prevent ordinary writers creating this state. Quarantine a
			// damaged queued envelope without decrypting or starving later work.
			run.Status = RunStatusFailed
			run.ErrorMessage = ErrPersonalCredentialsUnavailable.Error()
			err = tx.QueryRow(ctx, `UPDATE history_import_runs SET status='failed',error_message=$2,completed_at=now() WHERE id=$1 RETURNING completed_at`, run.ID, run.ErrorMessage).Scan(&run.CompletedAt)
			if err != nil {
				return nil, RunClaim{}, err
			}
			if err = tx.Commit(ctx); err != nil {
				return nil, RunClaim{}, err
			}
			return run, claim, nil
		}
	}
	run.Status = RunStatusRunning
	err = tx.QueryRow(ctx, `UPDATE history_import_runs SET status='running',started_at=now(),last_heartbeat_at=now(),claim_generation=claim_generation+1 WHERE id=$1 RETURNING claim_generation,started_at`, run.ID).Scan(&claim.Generation, &run.StartedAt)
	if err != nil {
		return nil, RunClaim{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, RunClaim{}, err
	}
	return run, claim, nil
}

// validateRunClaim is deliberately checked before effects and on each heartbeat.
// It cannot make cross-store effects atomic with configuration changes/cancellation.
func (r *Repository) validateRunClaim(ctx context.Context, claim RunClaim) error {
	var status, kind string
	var generation int64
	var canceled, valid, credentialsValid bool
	err := r.pool.QueryRow(ctx, `SELECT r.status,r.claim_generation,r.dispatch_kind,r.cancel_requested_at IS NOT NULL,
 CASE WHEN $2=0 THEN r.dispatch_version IS NULL
 WHEN r.dispatch_kind='personal' THEN r.dispatch_version=2
 AND EXISTS(SELECT 1 FROM users u JOIN user_profiles p ON p.user_id=u.id WHERE u.id=r.user_id AND p.id=r.profile_id)
 AND (r.dispatch_source_id IS NULL OR EXISTS(SELECT 1 FROM history_import_sources s
 WHERE s.id=r.dispatch_source_id AND s.revision=r.dispatch_source_revision AND s.enabled AND s.source_type=r.source_type))
 ELSE r.dispatch_version=1 AND EXISTS(
 SELECT 1 FROM history_import_sources s JOIN history_import_user_mappings m ON m.source_id=s.id
 WHERE s.id=r.dispatch_source_id AND s.revision=r.dispatch_source_revision AND s.enabled
 AND COALESCE(s.admin_token,'')<>'' AND m.id=r.dispatch_mapping_id AND m.revision=r.dispatch_mapping_revision
 AND m.silo_user_id=r.user_id AND m.silo_profile_id=r.profile_id AND m.external_user_id=r.dispatch_external_user_id
 ) END,
 r.dispatch_kind<>'personal' OR EXISTS(SELECT 1 FROM history_import_run_credentials c WHERE c.run_id=r.id AND c.envelope_version=1)
 FROM history_import_runs r WHERE r.id=$1`, claim.RunID, claim.Generation).Scan(&status, &generation, &kind, &canceled, &valid, &credentialsValid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunClaimLost
	}
	if err != nil {
		return err
	}
	if status != RunStatusRunning || generation != claim.Generation || (claim.Generation != 0 && kind != claim.DispatchKind) {
		return ErrRunClaimLost
	}
	if canceled {
		return ErrRunCancellationRequested
	}
	if !valid {
		return ErrRunConfigurationChanged
	}
	if !credentialsValid {
		return ErrPersonalCredentialsUnavailable
	}
	return nil
}

func (r *Repository) reconcileUndispatchedRuns(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `UPDATE history_import_runs SET status='failed',completed_at=now(),
 error_message=$1
 WHERE status='queued' AND connection_mode='admin_token' AND (dispatch_version IS DISTINCT FROM 1 OR dispatch_source_id IS NULL OR dispatch_source_revision IS NULL OR dispatch_mapping_id IS NULL OR dispatch_mapping_revision IS NULL OR dispatch_external_user_id IS NULL)`, LegacyDispatchUnavailableMessage)
	return err
}

func (r *Repository) activeRunForMapping(ctx context.Context, id int) (*Run, error) {
	var runID string
	err := r.pool.QueryRow(ctx, `SELECT id FROM history_import_runs WHERE mapping_id=$1 AND status IN ('queued','running') ORDER BY created_at,id LIMIT 1`, id).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.GetRunByID(ctx, runID)
}

func (r *Repository) bulkMappingIDs(ctx context.Context, sourceID int) ([]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM history_import_user_mappings WHERE source_id=$1 ORDER BY external_user_name,id LIMIT $2`, sourceID, MaxBulkAdminRuns+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int{}
	for rows.Next() {
		var id int
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > MaxBulkAdminRuns {
		return nil, ErrBulkRunTooLarge
	}
	return ids, nil
}

func (r *Repository) acknowledgeRunCancellation(ctx context.Context, claim RunClaim) error {
	result, err := r.pool.Exec(ctx, `UPDATE history_import_runs SET status='cancelled',completed_at=now(),error_message='Cancelled by admin'
 WHERE id=$1 AND claim_generation=$2 AND status='running' AND cancel_requested_at IS NOT NULL`, claim.RunID, claim.Generation)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrRunClaimLost
	}
	return nil
}

func (r *Repository) touchClaimHeartbeat(ctx context.Context, claim RunClaim) error {
	if err := r.validateRunClaim(ctx, claim); err != nil {
		return err
	}
	result, err := r.pool.Exec(ctx, `UPDATE history_import_runs SET last_heartbeat_at=now() WHERE id=$1 AND claim_generation=$2 AND status='running' AND cancel_requested_at IS NULL`, claim.RunID, claim.Generation)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrRunClaimLost
	}
	return nil
}

func (r *Repository) failStaleCancelledRuns(ctx context.Context, before time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE history_import_runs SET status='cancelled',completed_at=now(),error_message='Cancelled by admin; worker did not acknowledge before its lease expired'
 WHERE status='running' AND cancel_requested_at IS NOT NULL AND COALESCE(last_heartbeat_at,started_at,created_at)<$1`, before)
	return err
}

// ListAdminRunsPage preserves the source audit after a mapping is deleted.
func (r *Repository) ListAdminRunsPage(ctx context.Context, sourceID *int, after *RunKey, limit int) ([]Run, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	var stamp *time.Time
	var id string
	if after != nil {
		stamp = &after.CreatedAt
		id = after.ID
	}
	rows, err := r.pool.Query(ctx, `SELECT r.id,r.user_id,r.profile_id,r.source_type,r.connection_mode,r.status,r.mapping_id,
 r.fetched,r.matched,r.unmatched,r.progress_updated,r.history_created,r.watchlist_added,r.favorites_imported,r.skipped,
 r.warnings,r.unmatched_samples,COALESCE(r.error_message,''),r.created_at,r.started_at,r.completed_at,r.cancel_requested_at IS NOT NULL
 FROM history_import_runs r LEFT JOIN history_import_user_mappings m ON m.id=r.mapping_id
 WHERE ($1::integer IS NULL OR COALESCE(r.dispatch_source_id,m.source_id)=$1)
 AND ($2::timestamptz IS NULL OR (r.created_at,r.id)<($2,$3))
 ORDER BY r.created_at DESC,r.id DESC LIMIT $4`, sourceID, stamp, id, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	runs, err := scanAdminRuns(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(runs) > limit
	if more {
		runs = runs[:limit]
	}
	if runs == nil {
		runs = []Run{}
	}
	return runs, more, nil
}

func (r *Repository) touchCompletedMapping(ctx context.Context, claim RunClaim) error {
	_, err := r.pool.Exec(ctx, `UPDATE history_import_user_mappings m SET last_imported_at=now(),updated_at=now()
 WHERE m.id=$1 AND m.revision=$2 AND EXISTS(SELECT 1 FROM history_import_runs r
 WHERE r.id=$3 AND r.claim_generation=$4 AND r.status='completed' AND r.dispatch_mapping_id=m.id
 AND r.user_id=m.silo_user_id AND r.profile_id=m.silo_profile_id)`, claim.MappingID, claim.MappingRevision, claim.RunID, claim.Generation)
	return err
}
