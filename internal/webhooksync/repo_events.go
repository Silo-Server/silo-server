package webhooksync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	defaultWebhookEventLogLimit = 50
	maxWebhookEventLogLimit     = 200
	webhookEventLogRetention    = 200
)

func (r *Repository) CreateEventLog(ctx context.Context, entry WebhookEventLog) (*WebhookEventLog, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create webhook event log: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	attrsJSON, err := json.Marshal(entry.Attrs)
	if err != nil {
		return nil, fmt.Errorf("marshal webhook event log attrs: %w", err)
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO webhook_sync_event_logs (
			connection_id, request_id, http_status, outcome, summary, error_message, body_excerpt, attrs
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
		RETURNING id, connection_id, received_at, COALESCE(request_id, ''), http_status, outcome,
		          summary, COALESCE(error_message, ''), COALESCE(body_excerpt, ''), attrs`,
		entry.ConnectionID,
		entry.RequestID,
		entry.HTTPStatus,
		entry.Outcome,
		entry.Summary,
		entry.ErrorMessage,
		entry.BodyExcerpt,
		string(attrsJSON),
	)

	created, err := scanWebhookEventLog(row)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM webhook_sync_event_logs
		WHERE connection_id = $1
		  AND id NOT IN (
		    SELECT id
		    FROM webhook_sync_event_logs
		    WHERE connection_id = $1
		    ORDER BY received_at DESC, id DESC
		    LIMIT $2
		  )`,
		entry.ConnectionID,
		webhookEventLogRetention,
	); err != nil {
		return nil, fmt.Errorf("trim webhook event logs: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create webhook event log: %w", err)
	}
	return created, nil
}

func (r *Repository) ListEventLogs(ctx context.Context, connectionID string, limit int) ([]WebhookEventLog, error) {
	if limit <= 0 {
		limit = defaultWebhookEventLogLimit
	}
	if limit > maxWebhookEventLogLimit {
		limit = maxWebhookEventLogLimit
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, connection_id, received_at, COALESCE(request_id, ''), http_status, outcome,
		       summary, COALESCE(error_message, ''), COALESCE(body_excerpt, ''), attrs
		FROM webhook_sync_event_logs
		WHERE connection_id = $1
		ORDER BY received_at DESC, id DESC
		LIMIT $2`,
		connectionID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list webhook event logs: %w", err)
	}
	defer rows.Close()

	var out []WebhookEventLog
	for rows.Next() {
		entry, err := scanWebhookEventLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate webhook event logs: %w", err)
	}
	return out, nil
}

func scanWebhookEventLog(row interface {
	Scan(dest ...any) error
}) (*WebhookEventLog, error) {
	var entry WebhookEventLog
	var attrsJSON []byte
	if err := row.Scan(
		&entry.ID,
		&entry.ConnectionID,
		&entry.ReceivedAt,
		&entry.RequestID,
		&entry.HTTPStatus,
		&entry.Outcome,
		&entry.Summary,
		&entry.ErrorMessage,
		&entry.BodyExcerpt,
		&attrsJSON,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}
		return nil, fmt.Errorf("scan webhook event log: %w", err)
	}
	if len(attrsJSON) > 0 {
		if err := json.Unmarshal(attrsJSON, &entry.Attrs); err != nil {
			return nil, fmt.Errorf("decode webhook event log attrs: %w", err)
		}
	}
	return &entry, nil
}
