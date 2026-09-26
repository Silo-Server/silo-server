package webhooksync

import (
	"context"
	"fmt"
	"time"
)

// PageKey is a stable descending timestamp and resource identity.
type PageKey struct {
	At time.Time
	ID string
}

// ListConnectionsPage reads persisted account configuration without contacting providers.
func (s *Service) ListConnectionsPage(ctx context.Context, userID int, after *PageKey, limit int) ([]Connection, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	var at any
	var id any
	if after != nil {
		at = after.At
		id = after.ID
	}
	rows, err := s.repo.pool.Query(ctx, `
 SELECT c.id,c.user_id,c.provider,c.server_id,c.server_name,c.base_url,c.access_token,c.default_profile_id,c.webhook_secret,c.account_discovery_available,c.last_webhook_received_at,c.last_webhook_error_at,COALESCE(c.last_webhook_error_message,''),c.created_at,c.updated_at,
 (SELECT count(*)::int FROM webhook_sync_profile_mappings m WHERE m.connection_id=c.id)
 FROM webhook_sync_connections c WHERE c.user_id=$1 AND ($2::timestamptz IS NULL OR (c.created_at,c.id)<($2,$3::uuid))
 ORDER BY c.created_at DESC,c.id DESC LIMIT $4`, userID, at, id, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list webhook connections: %w", err)
	}
	defer rows.Close()
	out := make([]Connection, 0)
	for rows.Next() {
		var c Connection
		if err := rows.Scan(&c.ID, &c.UserID, &c.Provider, &c.ServerID, &c.ServerName, &c.BaseURL, &c.AccessToken, &c.DefaultProfileID, &c.WebhookSecret, &c.AccountDiscoveryAvailable, &c.LastWebhookReceivedAt, &c.LastWebhookErrorAt, &c.LastWebhookErrorMessage, &c.CreatedAt, &c.UpdatedAt, &c.UserCount); err != nil {
			return nil, false, err
		}
		// Access tokens are deliberately not decrypted for configuration listings.
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

func (s *Service) ListEventLogsPage(ctx context.Context, userID int, connectionID string, after *PageKey, limit int) ([]WebhookEventLog, bool, error) {
	if _, err := s.repo.GetConnection(ctx, userID, connectionID); err != nil {
		return nil, false, err
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	var at any
	var id any
	if after != nil {
		at = after.At
		id = after.ID
	}
	rows, err := s.repo.pool.Query(ctx, `
 SELECT id,connection_id,received_at,COALESCE(request_id,''),http_status,outcome,summary,COALESCE(error_message,''),COALESCE(body_excerpt,''),attrs
 FROM webhook_sync_event_logs WHERE connection_id=$1 AND ($2::timestamptz IS NULL OR (received_at,id)<($2,$3::bigint))
 ORDER BY received_at DESC,id DESC LIMIT $4`, connectionID, at, id, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := make([]WebhookEventLog, 0)
	for rows.Next() {
		entry, err := scanWebhookEventLog(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}
