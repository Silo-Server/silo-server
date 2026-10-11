package downloads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

// Administrator controls over the preparation queue. They act on jobs, not
// on download rows: one job can serve many downloads.
//
// A pause keeps the job's queued status and claim position but hides it from
// ClaimNext. Pausing a running job takes its lease away, so the worker's next
// heartbeat (or, on the replica that took the request, an immediate local
// abort) stops the encode; the interrupted attempt is not counted against the
// job. A cancel deletes the job and fails the downloads waiting on it.

// PreparationOutcome reports what an administrator action did to one job.
type PreparationOutcome string

const (
	// PreparationApplied means the action took effect.
	PreparationApplied PreparationOutcome = "applied"
	// PreparationUnchanged means the job was already in the requested state.
	PreparationUnchanged PreparationOutcome = "unchanged"
	// PreparationNotFound means no such job is being prepared: it never
	// existed, finished preparing, or was canceled.
	PreparationNotFound PreparationOutcome = "not_found"
	// PreparationNotApplicable means the action does not apply to the job's
	// state: a failed job cannot be paused or resumed.
	PreparationNotApplicable PreparationOutcome = "not_applicable"
)

// PreparationResult is the outcome of an administrator action on one job.
type PreparationResult struct {
	ArtifactID string
	Outcome    PreparationOutcome
}

// PreparationCanceledMessage is recorded on downloads whose job an
// administrator canceled.
const PreparationCanceledMessage = "Canceled by an administrator"

// pauseClaimGrace keeps a job paused mid-encode from being claimed again
// while the worker that lost it can still be running: a worker on another
// replica notices only at a successful heartbeat, and a second attempt would
// share its local output path. It matches the lease, the same bound after
// which ClaimNext treats a silent worker as gone.
const pauseClaimGrace = artifactLease

// stoppedArtifact is a job an administrator action took from its worker or
// queue. Running is true when an attempt held the job at the time.
type stoppedArtifact struct {
	ID      string
	Running bool
}

const runningArtifactStatuses = `('running', 'tone_map_running', 'audio_v2_running', 'tracks_v1_running')`

// PausePreparations pauses every listed job that is queued, retrying or
// running and not already paused. A running job returns to its queued status
// with its attempt refunded and its live state cleared. Its lease_expires_at
// moves to now + grace without an owner: the stopped worker may still run
// until then, so ClaimNext leaves the row alone that long even if it is
// resumed sooner.
func (r *ArtifactRepository) PausePreparations(ctx context.Context, ids []string, grace time.Duration) ([]stoppedArtifact, error) {
	rows, err := r.pool.Query(ctx,
		`WITH target AS (
		     SELECT id, status IN `+runningArtifactStatuses+` AS running
		     FROM download_artifacts
		     WHERE id = ANY($1) AND paused_at IS NULL
		       AND status IN ('queued', 'tone_map_queued', 'audio_v2_queued', 'tracks_v1_queued',
		                      'running', 'tone_map_running', 'audio_v2_running', 'tracks_v1_running')
		     FOR UPDATE
		 )
		 UPDATE download_artifacts a
		 SET paused_at = now(),
		     status = CASE a.status
		                  WHEN 'tracks_v1_running' THEN 'tracks_v1_queued'
		                  WHEN 'audio_v2_running' THEN 'audio_v2_queued'
		                  WHEN 'tone_map_running' THEN 'tone_map_queued'
		                  WHEN 'running' THEN 'queued'
		                  ELSE a.status END,
		     attempts = CASE WHEN t.running THEN GREATEST(a.attempts - 1, 0) ELSE a.attempts END,
		     started_at = CASE WHEN t.running THEN NULL ELSE a.started_at END,
		     worker_kind = CASE WHEN t.running THEN '' ELSE a.worker_kind END,
		     worker_node_id = CASE WHEN t.running THEN NULL ELSE a.worker_node_id END,
		     worker_name = CASE WHEN t.running THEN '' ELSE a.worker_name END,
		     progress_encoded_seconds = CASE WHEN t.running THEN NULL ELSE a.progress_encoded_seconds END,
		     progress_duration_seconds = CASE WHEN t.running THEN NULL ELSE a.progress_duration_seconds END,
		     progress_speed = CASE WHEN t.running THEN NULL ELSE a.progress_speed END,
		     progress_updated_at = CASE WHEN t.running THEN NULL ELSE a.progress_updated_at END,
		     progress_unavailable = CASE WHEN t.running THEN false ELSE a.progress_unavailable END,
		     lease_owner = NULL,
		     lease_expires_at = CASE WHEN t.running THEN now() + make_interval(secs => $2) ELSE NULL END
		 FROM target t
		 WHERE a.id = t.id
		 RETURNING a.id, t.running`,
		ids, grace.Seconds(),
	)
	if err != nil {
		return nil, fmt.Errorf("pausing preparations: %w", err)
	}
	return scanStoppedArtifacts(rows)
}

// ResumePreparations clears the pause on every listed paused job. Each job
// returns to the state it was paused in: queued at its old claim position, or
// waiting out a retry backoff that has not elapsed yet.
func (r *ArtifactRepository) ResumePreparations(ctx context.Context, ids []string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE download_artifacts SET paused_at = NULL
		 WHERE id = ANY($1) AND paused_at IS NOT NULL
		 RETURNING id`, ids)
	if err != nil {
		return nil, fmt.Errorf("resuming preparations: %w", err)
	}
	resumed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("resuming preparations: %w", err)
	}
	return resumed, nil
}

// CancelPreparations deletes every listed job that has not finished
// preparing and fails the downloads still waiting on them with errMsg, in one
// transaction. It returns the deleted jobs and the downloads it failed.
func (r *ArtifactRepository) CancelPreparations(ctx context.Context, ids []string, errMsg string) ([]stoppedArtifact, []*Download, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("beginning preparation cancel: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`WITH target AS (
		     SELECT id, status IN `+runningArtifactStatuses+` AS running
		     FROM download_artifacts
		     WHERE id = ANY($1) AND status NOT IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready')
		     FOR UPDATE
		 )
		 DELETE FROM download_artifacts a USING target t
		 WHERE a.id = t.id
		 RETURNING a.id, t.running`, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("canceling preparations: %w", err)
	}
	canceled, err := scanStoppedArtifacts(rows)
	if err != nil {
		return nil, nil, err
	}
	if len(canceled) == 0 {
		return nil, nil, nil
	}
	canceledIDs := make([]string, 0, len(canceled))
	for _, c := range canceled {
		canceledIDs = append(canceledIDs, c.ID)
	}
	downloadRows, err := tx.Query(ctx,
		`UPDATE downloads SET status = 'failed', error_message = $2, updated_at = now()
		 WHERE artifact_id = ANY($1) AND status = 'preparing'
		 RETURNING `+downloadColumns,
		canceledIDs, errMsg,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failing downloads of canceled preparations: %w", err)
	}
	failed, err := scanDownloads(downloadRows)
	downloadRows.Close()
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("committing preparation cancel: %w", err)
	}
	return canceled, failed, nil
}

func scanStoppedArtifacts(rows pgx.Rows) ([]stoppedArtifact, error) {
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stoppedArtifact, error) {
		var s stoppedArtifact
		err := row.Scan(&s.ID, &s.Running)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("scanning stopped preparations: %w", err)
	}
	return out, nil
}

// failedPreparations reads the listed jobs that have not finished preparing,
// mapped to whether each one failed. A job missing from the result is not
// being prepared.
func (r *ArtifactRepository) failedPreparations(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, status = 'failed' FROM download_artifacts
		 WHERE id = ANY($1) AND status NOT IN ('ready', 'tone_map_ready', 'audio_v2_ready', 'tracks_v1_ready')`, ids)
	if err != nil {
		return nil, fmt.Errorf("reading preparation states: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool, len(ids))
	for rows.Next() {
		var id string
		var failed bool
		if err := rows.Scan(&id, &failed); err != nil {
			return nil, fmt.Errorf("scanning preparation state: %w", err)
		}
		out[id] = failed
	}
	return out, rows.Err()
}

// pauseOrResumeResults reports one result per id, in request order. Jobs the
// action changed are applied; the rest are classified from their current
// state, read after the write, so a job that changed in between reports its
// newer state: gone or finished is not found, failed is not applicable, and
// anything else was already in the requested state.
func (r *ArtifactRepository) pauseOrResumeResults(ctx context.Context, ids []string, applied map[string]bool) ([]PreparationResult, error) {
	var rest []string
	for _, id := range ids {
		if !applied[id] {
			rest = append(rest, id)
		}
	}
	failed := map[string]bool{}
	if len(rest) > 0 {
		var err error
		if failed, err = r.failedPreparations(ctx, rest); err != nil {
			return nil, err
		}
	}
	return preparationResults(ids, func(id string) PreparationOutcome {
		isFailed, unfinished := failed[id]
		switch {
		case applied[id]:
			return PreparationApplied
		case !unfinished:
			return PreparationNotFound
		case isFailed:
			return PreparationNotApplicable
		default:
			return PreparationUnchanged
		}
	}), nil
}

// preparationResults pairs each id, in request order, with its outcome.
func preparationResults(ids []string, outcome func(id string) PreparationOutcome) []PreparationResult {
	out := make([]PreparationResult, 0, len(ids))
	for _, id := range ids {
		out = append(out, PreparationResult{ArtifactID: id, Outcome: outcome(id)})
	}
	return out
}

// uniqueIDs drops repeated ids, keeping the first occurrence's position.
func uniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// localAttempt is one attempt this replica runs.
type localAttempt struct{ cancel context.CancelFunc }

// trackLocalAttempt registers the cancel function of an attempt this
// replica runs, so an administrator action here can stop it at once instead
// of at its next heartbeat. The returned function unregisters it, unless a
// later attempt of the same job has replaced it.
func (m *ArtifactManager) trackLocalAttempt(id string, cancel context.CancelFunc) func() {
	attempt := &localAttempt{cancel: cancel}
	m.mu.Lock()
	if m.localAttempts == nil {
		m.localAttempts = make(map[string]*localAttempt)
	}
	m.localAttempts[id] = attempt
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		if m.localAttempts[id] == attempt {
			delete(m.localAttempts, id)
		}
		m.mu.Unlock()
	}
}

// removeCanceledLocalOutput deletes the local file of an attempt whose job
// was canceled while it ran: no row will ever point at it. A job that still
// exists lost its lease to another worker, which owns the path.
func (m *ArtifactManager) removeCanceledLocalOutput(ctx context.Context, artifactID string, prepared PreparedArtifact) {
	if prepared.Remote() || prepared.OutputPath == "" {
		return
	}
	if _, err := m.repo.GetByID(ctx, artifactID); !errors.Is(err, ErrNotFound) {
		return
	}
	if err := os.Remove(prepared.OutputPath); err != nil && !os.IsNotExist(err) {
		slog.WarnContext(ctx, "removing canceled artifact output failed", "component", "downloads", "artifact_id", artifactID, "error", err)
	}
}

// stopLocalAttempts aborts the attempts this replica runs for jobs an
// administrator stopped. Attempts on other replicas stop at their next
// heartbeat, which the action already fenced out.
func (m *ArtifactManager) stopLocalAttempts(stopped []stoppedArtifact) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range stopped {
		if attempt, ok := m.localAttempts[s.ID]; ok && s.Running {
			attempt.cancel()
		}
	}
}

// PausePreparations pauses the listed jobs and stops their running attempts.
func (m *ArtifactManager) PausePreparations(ctx context.Context, ids []string) ([]PreparationResult, error) {
	ids = uniqueIDs(ids)
	paused, err := m.repo.PausePreparations(ctx, ids, pauseClaimGrace)
	if err != nil {
		return nil, err
	}
	m.stopLocalAttempts(paused)
	applied := make(map[string]bool, len(paused))
	for _, p := range paused {
		applied[p.ID] = true
		m.notifyPreparationChanged(ctx, p.ID)
	}
	if len(paused) > 0 {
		slog.InfoContext(ctx, "download preparations paused", "component", "downloads", "count", len(paused))
	}
	return m.repo.pauseOrResumeResults(ctx, ids, applied)
}

// ResumePreparations resumes the listed paused jobs.
func (m *ArtifactManager) ResumePreparations(ctx context.Context, ids []string) ([]PreparationResult, error) {
	ids = uniqueIDs(ids)
	resumed, err := m.repo.ResumePreparations(ctx, ids)
	if err != nil {
		return nil, err
	}
	applied := make(map[string]bool, len(resumed))
	for _, id := range resumed {
		applied[id] = true
		m.notifyPreparationChanged(ctx, id)
	}
	if len(resumed) > 0 {
		slog.InfoContext(ctx, "download preparations resumed", "component", "downloads", "count", len(resumed))
		m.triggerDrain()
	}
	return m.repo.pauseOrResumeResults(ctx, ids, applied)
}

// CancelPreparations deletes the listed jobs, stops their running attempts,
// and fails the downloads waiting on them.
func (m *ArtifactManager) CancelPreparations(ctx context.Context, ids []string) ([]PreparationResult, error) {
	ids = uniqueIDs(ids)
	canceled, failed, err := m.repo.CancelPreparations(ctx, ids, PreparationCanceledMessage)
	if err != nil {
		return nil, err
	}
	m.stopLocalAttempts(canceled)
	for _, d := range failed {
		m.publish(ctx, d)
	}
	applied := make(map[string]bool, len(canceled))
	for _, c := range canceled {
		applied[c.ID] = true
		m.notifyPreparationChanged(ctx, c.ID)
	}
	if len(canceled) > 0 {
		slog.InfoContext(ctx, "download preparations canceled", "component", "downloads", "count", len(canceled), "downloads_failed", len(failed))
	}
	// Every job left alone had finished, was already gone, or never existed.
	return preparationResults(ids, func(id string) PreparationOutcome {
		if applied[id] {
			return PreparationApplied
		}
		return PreparationNotFound
	}), nil
}
