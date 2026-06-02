package requests

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const targetColumns = `t.id, t.request_id, t.integration_id, t.integration_kind,
	COALESCE(ri.name, ''), t.quality, t.is_anime, t.external_id, t.external_status,
	t.status, t.last_error, t.created_at, t.updated_at`

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
	var integrationID *string
	if err := row.Scan(&t.ID, &t.RequestID, &integrationID, &t.IntegrationKind,
		&t.InstanceName, &t.Quality, &t.IsAnime, &t.ExternalID, &t.ExternalStatus,
		&t.Status, &t.LastError, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Target{}, err
	}
	if integrationID != nil {
		t.IntegrationID = *integrationID
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

func (r *Repository) CreateTarget(ctx context.Context, t Target) (Target, error) {
	var integrationID any
	if t.IntegrationID != "" {
		integrationID = t.IntegrationID
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO media_request_targets
			(request_id, integration_id, integration_kind, quality, is_anime,
			 external_id, external_status, status, last_error, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now())
		RETURNING id`,
		t.RequestID, integrationID, t.IntegrationKind, t.Quality, t.IsAnime,
		t.ExternalID, t.ExternalStatus, t.Status, t.LastError)
	if err := row.Scan(&t.ID); err != nil {
		return Target{}, fmt.Errorf("create target: %w", err)
	}
	return t, nil
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
// aggregate status/outcome, all in one transaction.
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

	req, err := r.recomputeAggregate(ctx, tx, requestID, actor)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit target update: %w", err)
	}
	return req, nil
}

func (r *Repository) recomputeAggregate(ctx context.Context, exec requestExecutor, requestID string, actor Viewer) (*Request, error) {
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
	_ = r.recordEvent(ctx, exec, requestID, "status_"+string(status), actor, string(req.ExternalStatus))
	return req, nil
}
