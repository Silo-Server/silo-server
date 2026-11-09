package historyimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// maxRunResumesWithoutProgress stops retrying a run that keeps dying
	// before it gets past the same page, such as one that crashes the server.
	maxRunResumesWithoutProgress = 5
	// largeImportThreshold is when a run counts as very large and must hold
	// the one slot for such runs, so several cannot starve playback of a small
	// server's Postgres between them.
	largeImportThreshold = 50_000
	// largeImportPollInterval is how often a run waiting for the slot retries.
	largeImportPollInterval = 15 * time.Second
)

// errRunInterrupted marks a failure of this server rather than of the import,
// so the run goes back to the queue. The resume limit stops it repeating.
var errRunInterrupted = errors.New("history import interrupted")

// runCheckpoint is where a run continues after a restart, and the totals it
// continues from.
type runCheckpoint struct {
	Position StreamPosition   `json:"position"`
	Summary  ExecutionSummary `json:"summary"`
}

// snapshot copies the summary so later records do not change it. Everything
// in it is capped, so the copy is small.
func (s ExecutionSummary) snapshot() ExecutionSummary {
	s.Warnings = slices.Clone(s.Warnings)
	s.UnmatchedSamples = slices.Clone(s.UnmatchedSamples)
	s.UnmatchedReasonCounts = maps.Clone(s.UnmatchedReasonCounts)
	return s
}

// saveRunCheckpoint records where the run stands. Getting past a page clears
// the run's count of restarts without progress.
func (r *Repository) saveRunCheckpoint(ctx context.Context, claim RunClaim, checkpoint runCheckpoint) error {
	payload, _ := json.Marshal(checkpoint)
	_, err := r.pool.Exec(ctx, `UPDATE history_import_runs SET
		resume_attempts = CASE WHEN checkpoint->'position' IS DISTINCT FROM $3::jsonb->'position' THEN 0 ELSE resume_attempts END,
		checkpoint = $3::jsonb
		WHERE id=$1 AND status='running' AND claim_generation=$2`, claim.RunID, claim.Generation, payload)
	if err != nil {
		return fmt.Errorf("saving run %s checkpoint: %w", claim.RunID, err)
	}
	return nil
}

// loadRunCheckpoint returns the run's checkpoint, or nil for a fresh run. A
// checkpoint that cannot be read starts the run over, which is safe.
func (r *Repository) loadRunCheckpoint(ctx context.Context, claim RunClaim) (*runCheckpoint, error) {
	var payload []byte
	err := r.pool.QueryRow(ctx, `SELECT checkpoint FROM history_import_runs WHERE id=$1 AND claim_generation=$2`, claim.RunID, claim.Generation).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRunClaimLost
	}
	if err != nil || payload == nil {
		return nil, err
	}
	var checkpoint runCheckpoint
	if err := json.Unmarshal(payload, &checkpoint); err != nil || checkpoint.Position.Stage == "" {
		slog.WarnContext(ctx, "history import: unreadable checkpoint; restarting the run", "run_id", claim.RunID, "error", err)
		return nil, nil
	}
	return &checkpoint, nil
}

// requeueStaleRuns returns running runs whose worker died to the queue when
// they have a checkpoint to continue from.
func (r *Repository) requeueStaleRuns(ctx context.Context, staleBefore time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE history_import_runs SET status='queued',
		resume_attempts=resume_attempts+1, large_import=false, last_heartbeat_at=NULL
		WHERE status='running' AND completed_at IS NULL AND cancel_requested_at IS NULL
		AND dispatch_version IS NOT NULL AND checkpoint IS NOT NULL AND resume_attempts < $2
		AND COALESCE(last_heartbeat_at, started_at, created_at) < $1`, staleBefore, maxRunResumesWithoutProgress)
	if err != nil {
		return 0, fmt.Errorf("requeueing stale history import runs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// requeueInterruptedRun hands a run this process is abandoning at shutdown
// back to the queue, and reports whether it did. A canceled run stays
// canceled and a run with no checkpoint is left for the normal failure path.
func (r *Repository) requeueInterruptedRun(ctx context.Context, claim RunClaim) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `UPDATE history_import_runs SET status='queued',
		resume_attempts=resume_attempts+1, large_import=false, last_heartbeat_at=NULL
		WHERE id=$1 AND status='running' AND claim_generation=$2 AND claim_generation>0
		AND cancel_requested_at IS NULL AND checkpoint IS NOT NULL AND resume_attempts < $3`,
		claim.RunID, claim.Generation, maxRunResumesWithoutProgress)
	if err != nil {
		slog.WarnContext(ctx, "history import: requeue at shutdown failed", "run_id", claim.RunID, "error", err)
		return false
	}
	return tag.RowsAffected() > 0
}

// tryLargeImportSlot claims the single slot for very large runs. The partial
// unique index makes that atomic across nodes, and a dead holder frees it when
// its row stops being running. A run that lost its claim or was canceled while
// waiting gets ErrRunClaimLost rather than the slot.
func (r *Repository) tryLargeImportSlot(ctx context.Context, claim RunClaim) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE history_import_runs SET large_import=true
		WHERE id=$1 AND status='running' AND claim_generation=$2 AND cancel_requested_at IS NULL`, claim.RunID, claim.Generation)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claiming large import slot: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, ErrRunClaimLost
	}
	return true, nil
}

// awaitLargeSlot holds a run that has become very large until it owns the
// slot. The heartbeat keeps running while it waits.
func (a *runApplier) awaitLargeSlot(ctx context.Context) error {
	if a.largeSlot || a.claim.Generation == 0 || a.summary.Fetched < largeImportThreshold {
		return nil
	}
	logged := false
	for {
		acquired, err := a.service.repo.tryLargeImportSlot(ctx, a.claim)
		if err != nil {
			return err
		}
		if acquired {
			a.largeSlot = true
			return nil
		}
		if !logged {
			logged = true
			slog.InfoContext(ctx, "history import: waiting for the large import slot", "run_id", a.run.ID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(largeImportPollInterval):
		}
	}
}
