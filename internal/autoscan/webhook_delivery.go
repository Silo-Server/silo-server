package autoscan

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const webhookDeliveryLease = 5 * time.Minute

func postgresInterval(d time.Duration) string {
	return fmt.Sprintf("%f seconds", d.Seconds())
}

func scanWebhookDelivery(row interface{ Scan(...any) error }) (WebhookDelivery, error) {
	var (
		delivery WebhookDelivery
		changes  []byte
	)
	if err := row.Scan(
		&delivery.ID,
		&delivery.SourceID,
		&delivery.ProviderEventType,
		&changes,
		&delivery.ReceivedAt,
		&delivery.AttemptCount,
		&delivery.LockedBy,
	); err != nil {
		return WebhookDelivery{}, err
	}
	if err := json.Unmarshal(changes, &delivery.Changes); err != nil {
		return WebhookDelivery{}, fmt.Errorf("decode autoscan webhook delivery changes: %w", err)
	}
	return delivery, nil
}

// CreateWebhookDelivery durably accepts a delivery and leases it to the caller
// for an immediate ingest attempt. If the caller exits before finalizing it, a
// retry worker can reclaim it after webhookDeliveryLease.
func (r *Repository) CreateWebhookDelivery(ctx context.Context, in ChangeIngest) (WebhookDelivery, error) {
	changes, err := json.Marshal(in.Changes)
	if err != nil {
		return WebhookDelivery{}, fmt.Errorf("encode autoscan webhook delivery changes: %w", err)
	}
	receivedAt := in.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	lockedBy := uuid.NewString()
	row := r.pool.QueryRow(ctx, `
		INSERT INTO autoscan_webhook_deliveries (
			source_id, provider_event_type, changes, received_at,
			attempt_count, next_attempt_at, locked_at, locked_by
		)
		VALUES ($1, $2, $3, $4, 1, now(), now(), $5)
		RETURNING id, source_id, provider_event_type, changes, received_at,
			attempt_count, locked_by`,
		in.SourceID, in.ProviderEventType, changes, receivedAt, lockedBy)
	delivery, err := scanWebhookDelivery(row)
	if err != nil {
		return WebhookDelivery{}, fmt.Errorf("create autoscan webhook delivery: %w", err)
	}
	return delivery, nil
}

// ClaimWebhookDeliveries leases due or abandoned deliveries to one worker.
// FOR UPDATE SKIP LOCKED keeps concurrent nodes from ingesting the same row.
func (r *Repository) ClaimWebhookDeliveries(ctx context.Context, workerID string, limit int) ([]WebhookDelivery, error) {
	if limit <= 0 {
		return []WebhookDelivery{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		WITH due AS (
			SELECT id
			FROM autoscan_webhook_deliveries
			WHERE next_attempt_at <= now()
			  AND (locked_at IS NULL OR locked_at < now() - $2::interval)
			ORDER BY next_attempt_at ASC, id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE autoscan_webhook_deliveries d
		SET attempt_count = d.attempt_count + 1,
			locked_at = now(),
			locked_by = $3,
			updated_at = now()
		FROM due
		WHERE d.id = due.id
		RETURNING d.id, d.source_id, d.provider_event_type, d.changes,
			d.received_at, d.attempt_count, d.locked_by`,
		limit, postgresInterval(webhookDeliveryLease), workerID)
	if err != nil {
		return nil, fmt.Errorf("claim autoscan webhook deliveries: %w", err)
	}
	defer rows.Close()
	deliveries := make([]WebhookDelivery, 0, limit)
	for rows.Next() {
		delivery, err := scanWebhookDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate autoscan webhook deliveries: %w", err)
	}
	return deliveries, nil
}

// CompleteWebhookDelivery removes a successfully consumed delivery. The lease
// owner guard prevents stale workers from deleting a row another node reclaimed.
func (r *Repository) CompleteWebhookDelivery(ctx context.Context, id int64, lockedBy string) error {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM autoscan_webhook_deliveries
		WHERE id = $1 AND locked_by = $2`, id, lockedBy)
	if err != nil {
		return fmt.Errorf("complete autoscan webhook delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: webhook delivery %d lease", ErrNotFound, id)
	}
	return nil
}

// RetryWebhookDelivery releases a failed delivery back to the durable queue
// after a bounded delay. The lease owner guard makes late workers harmless.
func (r *Repository) RetryWebhookDelivery(ctx context.Context, id int64, lockedBy string, delay time.Duration, msg string) error {
	if delay < 0 {
		delay = 0
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE autoscan_webhook_deliveries
		SET next_attempt_at = now() + $3::interval,
			locked_at = NULL,
			locked_by = '',
			last_error = $4,
			updated_at = now()
		WHERE id = $1 AND locked_by = $2`,
		id, lockedBy, postgresInterval(delay), truncateUTF8(msg, maxLastErrorLen))
	if err != nil {
		return fmt.Errorf("retry autoscan webhook delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: webhook delivery %d lease", ErrNotFound, id)
	}
	return nil
}
