package requests

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const targetColumns = `t.id, t.request_id, t.integration_id, t.integration_kind,
	COALESCE(ri.name, ''), t.quality, t.is_anime, t.external_id, t.external_status,
	t.status, t.last_error, t.created_at, t.updated_at, COALESCE(t.route_id, ''), t.route_name,
	t.download_phase, t.download_bytes_total, t.download_bytes_left, t.download_eta,
	t.download_count, t.download_updated_at`

// clearTargetDownload empties a target's download progress columns.
const clearTargetDownload = `download_phase = NULL, download_bytes_total = NULL,
	download_bytes_left = NULL, download_eta = NULL, download_count = NULL,
	download_updated_at = NULL, download_checked_at = NULL`

// aggregateStatus derives a request's status/outcome from its targets.
func aggregateStatus(targets []Target) (Status, Outcome) {
	if len(targets) == 0 {
		return StatusApproved, OutcomeActive
	}
	failed, completed := 0, 0
	anyDownloading, anyQueued := false, false
	for _, t := range targets {
		switch t.Status {
		case StatusFailed:
			failed++
		case StatusCompleted:
			completed++
		case StatusDownloading:
			anyDownloading = true
		case StatusQueued:
			anyQueued = true
		}
	}
	if completed == len(targets) {
		return StatusCompleted, OutcomeActive
	}
	// Active targets keep the request active even with a failed sibling so the
	// in-flight targets can finish (partial failure stays active).
	if anyDownloading {
		return StatusDownloading, OutcomeActive
	}
	if anyQueued {
		return StatusQueued, OutcomeActive
	}
	// No active targets remain and at least one failed (all-failed, or a mix of
	// completed + failed) -> surface as failed so Retry can re-submit the failed
	// target while leaving completed ones untouched.
	if failed > 0 {
		return StatusQueued, OutcomeFailed
	}
	return StatusCompleted, OutcomeActive
}

func scanTarget(row requestScanner) (Target, error) {
	var t Target
	var integrationID, downloadPhase *string
	var bytesTotal, bytesLeft *int64
	var downloadCount *int
	var downloadETA, downloadUpdatedAt *time.Time
	if err := row.Scan(&t.ID, &t.RequestID, &integrationID, &t.IntegrationKind,
		&t.InstanceName, &t.Quality, &t.IsAnime, &t.ExternalID, &t.ExternalStatus,
		&t.Status, &t.LastError, &t.CreatedAt, &t.UpdatedAt, &t.RouteID, &t.RouteName,
		&downloadPhase, &bytesTotal, &bytesLeft, &downloadETA, &downloadCount, &downloadUpdatedAt); err != nil {
		return Target{}, err
	}
	if integrationID != nil {
		t.IntegrationID = *integrationID
	}
	if downloadPhase != nil {
		d := &DownloadProgress{Phase: DownloadPhase(*downloadPhase), EstimatedCompletion: downloadETA}
		if bytesTotal != nil {
			d.BytesTotal = *bytesTotal
		}
		if bytesLeft != nil {
			d.BytesLeft = *bytesLeft
		}
		if downloadCount != nil {
			d.Downloads = *downloadCount
		}
		if downloadUpdatedAt != nil {
			d.UpdatedAt = *downloadUpdatedAt
		}
		t.Download = d
	}
	return t, nil
}

func (r *Repository) ListTargets(ctx context.Context, requestID string) ([]Target, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+targetColumns+`
		FROM media_request_targets t
		LEFT JOIN request_integrations ri ON ri.id = t.integration_id
		WHERE t.request_id = $1 ORDER BY t.quality`, requestID)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	defer rows.Close()
	var out []Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTargetsForRequests reads the targets of many requests in one query.
func (r *Repository) ListTargetsForRequests(ctx context.Context, requestIDs []string) (map[string][]Target, error) {
	out := map[string][]Target{}
	if len(requestIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+targetColumns+`
		FROM media_request_targets t
		LEFT JOIN request_integrations ri ON ri.id = t.integration_id
		WHERE t.request_id = ANY($1) ORDER BY t.request_id, t.quality`, requestIDs)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out[t.RequestID] = append(out[t.RequestID], t)
	}
	return out, rows.Err()
}

func (r *Repository) CreateTarget(ctx context.Context, t Target) (Target, error) {
	return insertTarget(ctx, r.pool, t)
}

func insertTarget(ctx context.Context, exec requestExecutor, t Target) (Target, error) {
	var integrationID, routeID any
	if t.IntegrationID != "" {
		integrationID = t.IntegrationID
	}
	if t.RouteID != "" {
		routeID = t.RouteID
	}
	row := exec.QueryRow(ctx, `
		INSERT INTO media_request_targets
			(request_id, integration_id, integration_kind, quality, is_anime,
			 external_id, external_status, status, last_error, updated_at, route_id, route_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now(), $10, $11)
		RETURNING id`,
		t.RequestID, integrationID, t.IntegrationKind, t.Quality, t.IsAnime,
		t.ExternalID, t.ExternalStatus, t.Status, t.LastError, routeID, t.RouteName)
	if err := row.Scan(&t.ID); err != nil {
		return Target{}, fmt.Errorf("create target: %w", err)
	}
	return t, nil
}

func (r *Repository) RecordSubmission(ctx context.Context, id string, leaseUntil time.Time, targets []Target, actor Viewer) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin submission record transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Releasing the claim also locks the row, so a withdrawal or a newer
	// claim cannot slip in before the targets land.
	var claimedID string
	if err := tx.QueryRow(ctx, `
		UPDATE media_requests
		SET submit_lease_until = NULL
		WHERE id = $1
		  AND status = 'approved'
		  AND outcome = 'active'
		  AND submit_lease_until = $2
		RETURNING id`, id, leaseUntil).Scan(&claimedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, guardMiss(ctx, tx, id)
		}
		return nil, fmt.Errorf("release request submission claim: %w", err)
	}
	for _, t := range targets {
		t.RequestID = id
		if _, err := insertTarget(ctx, tx, t); err != nil {
			return nil, err
		}
	}
	req, err := r.recomputeAggregate(ctx, tx, id, actor)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit submission record transaction: %w", err)
	}
	return req, nil
}

func (r *Repository) DeleteTarget(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM media_request_targets WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete target: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateTargetStatus updates one target and recomputes the parent request's
// aggregate status/outcome, all in one transaction. A target that completes
// or fails loses its download progress.
func (r *Repository) UpdateTargetStatus(ctx context.Context, targetID int64, status Status,
	externalID, externalStatus, lastErr string, actor Viewer) (*Request, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin target update: %w", err)
	}
	defer tx.Rollback(ctx)

	var requestID string
	if err := tx.QueryRow(ctx, `
		UPDATE media_request_targets
		SET status=$2,
		    external_id = CASE WHEN $3 = '' THEN external_id ELSE $3 END,
		    external_status = CASE WHEN $4 = '' THEN external_status ELSE $4 END,
		    last_error=$5, updated_at=now()
		WHERE id=$1 RETURNING request_id`,
		targetID, status, externalID, externalStatus, lastErr).Scan(&requestID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update target: %w", err)
	}
	if status == StatusCompleted || status == StatusFailed {
		if _, err := tx.Exec(ctx, `UPDATE media_request_targets SET `+clearTargetDownload+` WHERE id = $1`, targetID); err != nil {
			return nil, fmt.Errorf("clear target download progress: %w", err)
		}
	}

	req, err := r.recomputeAggregate(ctx, tx, requestID, actor)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit target update: %w", err)
	}
	return req, nil
}

// UpdateTargetDownload stores a target's download progress, stamped now as
// both heard from and asked about, or clears it when progress is nil. It
// writes only while the target is queued or downloading, so a late report
// cannot give a finished target progress again, and it touches nothing else:
// not updated_at, which dates status changes for the stalled-target backstop,
// not the request, and not its history.
func (r *Repository) UpdateTargetDownload(ctx context.Context, targetID int64, progress *DownloadProgress) error {
	const live = ` WHERE id = $1 AND status IN ('queued', 'downloading')`
	var err error
	if progress == nil {
		_, err = r.pool.Exec(ctx, `UPDATE media_request_targets SET `+clearTargetDownload+live, targetID)
	} else {
		_, err = r.pool.Exec(ctx, `
			UPDATE media_request_targets
			SET download_phase = $2, download_bytes_total = $3, download_bytes_left = $4,
			    download_eta = $5, download_count = $6, download_updated_at = now(),
			    download_checked_at = now()`+live,
			targetID, string(progress.Phase), progress.BytesTotal, progress.BytesLeft,
			progress.EstimatedCompletion, progress.Downloads)
	}
	if err != nil {
		return fmt.Errorf("update target download progress: %w", err)
	}
	return nil
}

// MarkTargetDownloadChecked records that a pass asked about a target's
// download without getting its status back. It moves the target to the back
// of the download refresh rotation and leaves the progress, and when it was
// last heard from, alone. A target without progress, or no longer queued or
// downloading, is not written.
func (r *Repository) MarkTargetDownloadChecked(ctx context.Context, targetID int64) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE media_request_targets SET download_checked_at = now()
		WHERE id = $1 AND status IN ('queued', 'downloading') AND download_phase IS NOT NULL`, targetID); err != nil {
		return fmt.Errorf("mark target download checked: %w", err)
	}
	return nil
}

// UpdateTargetExternalStatus records the raw status a target's server last
// reported, while the target is queued or downloading. It leaves updated_at
// alone, since that dates the target's last status change, and writes nothing
// when the status is unchanged.
func (r *Repository) UpdateTargetExternalStatus(ctx context.Context, targetID int64, externalStatus string) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE media_request_targets SET external_status = $2
		WHERE id = $1 AND status IN ('queued', 'downloading') AND external_status <> $2`, targetID, externalStatus); err != nil {
		return fmt.Errorf("update target external status: %w", err)
	}
	return nil
}

func (r *Repository) recomputeAggregate(ctx context.Context, exec requestExecutor, requestID string, actor Viewer) (*Request, error) {
	// The request's history records what changed, once: a request with two
	// targets moving to queued is one event, not two.
	var prevStatus Status
	var prevOutcome Outcome
	if err := exec.QueryRow(ctx, `SELECT status, outcome FROM media_requests WHERE id = $1 FOR UPDATE`, requestID).
		Scan(&prevStatus, &prevOutcome); err != nil {
		return nil, fmt.Errorf("load request status: %w", err)
	}
	if prevOutcome == OutcomeDeclined || prevOutcome == OutcomeCancelled {
		// A closed request stays closed: a target reporting late (say, after
		// an admin closed a failed request) updates only itself.
		req, err := scanRequest(exec.QueryRow(ctx, requestSelectSQL()+` WHERE id = $1`, requestID))
		if err != nil {
			return nil, fmt.Errorf("load closed request: %w", err)
		}
		return req, nil
	}
	rows, err := exec.Query(ctx, `SELECT status FROM media_request_targets WHERE request_id = $1`, requestID)
	if err != nil {
		return nil, fmt.Errorf("load target statuses: %w", err)
	}
	var targets []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.Status); err != nil {
			rows.Close()
			return nil, err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	status, outcome := aggregateStatus(targets)

	var lastErr string
	for _, t := range targets {
		if t.Status == StatusFailed {
			lastErr = "one or more fulfillment targets failed"
			break
		}
	}
	req, err := scanRequest(exec.QueryRow(ctx, `
		UPDATE media_requests
		SET status=$2, outcome=$3,
		    last_error = CASE WHEN $3 = 'failed' THEN $4 ELSE '' END,
		    completed_at = CASE WHEN $2 = 'completed' AND completed_at IS NULL THEN now() ELSE completed_at END,
		    updated_at = now()
		WHERE id=$1 RETURNING `+requestColumns(), requestID, status, outcome, lastErr))
	if err != nil {
		return nil, fmt.Errorf("recompute aggregate: %w", err)
	}
	if status != prevStatus {
		_ = r.recordEvent(ctx, exec, requestID, "status_"+string(status), actor, req.ExternalStatus)
	}
	if outcome != prevOutcome {
		_ = r.recordEvent(ctx, exec, requestID, "outcome_"+string(outcome), actor, lastErr)
	}
	return req, nil
}
