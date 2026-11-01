package adminjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// JobTypeBackupRestore restores chosen sections of a backup. It is never
// requeued when stale: RequeueStaleRunning marks it failed as interrupted,
// and the per-item fence keeps a slow original worker from writing after.
const JobTypeBackupRestore = "backup_restore"

// BackupRestoreInterrupted is the error a stale restore job records.
const BackupRestoreInterrupted = "interrupted"

// backupRestoreTimeout bounds one restore run.
const backupRestoreTimeout = 24 * time.Hour

// FencedUpdate is what a fenced transaction records on the job row in the
// same transaction as its writes.
type FencedUpdate struct {
	Result  any
	Current int
	Total   int
	Message string
}

// BackupRestoreRun is one claimed restore job.
type BackupRestoreRun struct {
	JobID           string
	CreatedByUserID int
	Payload         json.RawMessage
	// Fenced runs fn in a transaction that first locks the job row,
	// requiring it running under this worker's claim, holds that lock
	// through commit, and records fn's update in the same transaction. It
	// returns ErrJobNotFound, having written nothing, once the job is no
	// longer this worker's.
	Fenced func(ctx context.Context, fn func(pgx.Tx) (FencedUpdate, error)) error
	// Published announces the job row's current state, after a commit.
	Published func(ctx context.Context)
}

type backupRestoreExecutor interface {
	ExecuteBackupRestore(ctx context.Context, run BackupRestoreRun) (result any, err error)
}

// SetBackupRestoreExecutor wires the restore job. A server without one
// fails a claimed restore with a clear message.
func (r *Runner) SetBackupRestoreExecutor(executor backupRestoreExecutor) {
	r.backupRestore = executor
}

var (
	errBackupRestoreCancelRequested = errors.New("backup restore cancellation requested")
	errBackupRestoreClaimLost       = errors.New("backup restore claim lost")
)

func (r *Runner) executeBackupRestore(job *models.AdminJob) {
	if r.backupRestore == nil {
		r.failJob(job.ID, 0, 0, "Restore failed", "this server cannot run restores")
		return
	}
	timeoutCtx, timeoutCancel := context.WithTimeout(r.executionContext(), backupRestoreTimeout)
	defer timeoutCancel()
	ctx, stop := context.WithCancelCause(timeoutCtx)
	defer stop(nil)
	unregister := r.cancelRegistry.Register(job.ID, func() { stop(errBackupRestoreCancelRequested) })
	defer unregister()
	go func() {
		ticker := time.NewTicker(r.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cancelRequested, claim, err := r.repo.CancelState(ctx, job.ID)
				switch {
				case err != nil:
				case claim != job.ClaimGeneration:
					stop(errBackupRestoreClaimLost)
					return
				case cancelRequested:
					stop(errBackupRestoreCancelRequested)
					return
				}
			}
		}
	}()
	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)

	result, err := r.backupRestore.ExecuteBackupRestore(ctx, BackupRestoreRun{
		JobID: job.ID, CreatedByUserID: job.CreatedByUserID, Payload: job.RequestPayload,
		Fenced: func(ctx context.Context, fn func(pgx.Tx) (FencedUpdate, error)) error {
			return r.repo.RunFenced(ctx, job.ID, fn)
		},
		Published: func(ctx context.Context) { r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID) },
	})

	finishCtx, finishCancel := r.finalizeContext()
	defer finishCancel()
	// Each fenced item recorded the progress; the outcome keeps it.
	current, total := 0, 0
	if row, getErr := r.repo.GetByID(finishCtx, job.ID); getErr == nil {
		current, total = row.ProgressCurrent, row.ProgressTotal
	}
	cause := context.Cause(ctx)
	switch {
	case errors.Is(err, ErrJobNotFound), errors.Is(cause, errBackupRestoreClaimLost):
		// Stale recovery marked the job interrupted; the fence kept this
		// worker from writing after that, and the row is no longer its own.
		slog.Warn("admin jobs: backup restore lost its claim; the job was marked interrupted", "job_id", job.ID)
	case errors.Is(cause, errBackupRestoreCancelRequested):
		canceled, cancelErr := r.repo.CancelWithResult(finishCtx, job.ID, current, total, "Restore canceled", result, time.Now().UTC().Add(r.retention))
		if cancelErr != nil {
			slog.Warn("admin jobs: failed to record restore cancellation", "job_id", job.ID, "error", cancelErr)
			return
		}
		r.publishJob(finishCtx, notifications.TypeJobCancelled, canceled)
	case err != nil:
		r.failJobWithResult(job.ID, current, total, "Restore failed", err.Error(), result)
	default:
		if err := r.repo.Complete(finishCtx, job.ID, CompleteJobInput{
			ResultPayload: result, Message: "Restore finished", CanceledMessage: "Restore canceled",
			ProgressCurrent: current, ProgressTotal: total,
			ExpiresAt: time.Now().UTC().Add(r.retention),
		}); err != nil {
			slog.Warn("admin jobs: failed to complete restore", "job_id", job.ID, "error", err)
			return
		}
		r.publishJobByID(finishCtx, notifications.TypeJobCompleted, job.ID)
	}
}

// RunFenced runs fn in a transaction that begins by locking the job row,
// requiring status running and this repository's claim, and holds that lock
// through commit. The stale-job updater blocks on the same row, so it cannot
// mark the job interrupted between the check and the commit. fn's update is
// written to the row in the same transaction. When the check fails nothing
// is written and ErrJobNotFound is returned.
func (r *Repository) RunFenced(ctx context.Context, id string, fn func(pgx.Tx) (FencedUpdate, error)) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning fenced job transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var locked string
	err = tx.QueryRow(ctx, `
		SELECT id FROM admin_jobs
		WHERE id = $1 AND status = 'running' AND ($2::bigint IS NULL OR claim_generation = $2)
		FOR UPDATE`, id, r.claim).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrJobNotFound
	}
	if err != nil {
		return fmt.Errorf("locking the job row: %w", err)
	}
	update, err := fn(tx)
	if err != nil {
		return err
	}
	payload, err := marshalPayload(update.Result)
	if err != nil {
		return fmt.Errorf("marshaling admin job result payload: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE admin_jobs
		SET result_payload = $2, progress_current = $3, progress_total = $4, message = $5,
			heartbeat_at = NOW(), updated_at = NOW()
		WHERE id = $1`, id, payload, update.Current, update.Total, update.Message); err != nil {
		return fmt.Errorf("recording the job result: %w", err)
	}
	return tx.Commit(ctx)
}
