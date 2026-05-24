package webhooksync

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConnectionNotFound = errors.New("webhook sync connection not found")

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) ProfileExistsForUser(ctx context.Context, userID int, profileID string) (bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM user_profiles WHERE user_id = $1 AND id = $2)`,
		userID, profileID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking profile ownership: %w", err)
	}
	return exists, nil
}

func (r *Repository) CreateConnection(ctx context.Context, conn Connection) (*Connection, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO webhook_sync_connections (
			id, user_id, provider, server_id, server_name, base_url, access_token, default_profile_id, webhook_secret
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, user_id, provider, server_id, server_name, base_url, access_token, default_profile_id,
		          webhook_secret, account_discovery_available,
		          last_webhook_received_at, last_webhook_error_at, COALESCE(last_webhook_error_message, ''),
		          created_at, updated_at`,
		conn.ID, conn.UserID, conn.Provider, conn.ServerID, conn.ServerName, conn.BaseURL, conn.AccessToken, conn.DefaultProfileID, conn.WebhookSecret,
	)
	return scanConnection(row)
}

func (r *Repository) ListConnections(ctx context.Context, userID int) ([]Connection, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.user_id, c.provider, c.server_id, c.server_name, c.base_url, c.access_token, c.default_profile_id,
		       c.webhook_secret, c.account_discovery_available,
		       c.last_webhook_received_at, c.last_webhook_error_at, COALESCE(c.last_webhook_error_message, ''),
		       c.created_at, c.updated_at,
		       COUNT(m.id)::integer AS user_count
		FROM webhook_sync_connections c
		LEFT JOIN webhook_sync_profile_mappings m ON m.connection_id = c.id
		WHERE c.user_id = $1
		GROUP BY c.id
		ORDER BY c.created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing webhook sync connections: %w", err)
	}
	defer rows.Close()

	var out []Connection
	for rows.Next() {
		var c Connection
		if err := rows.Scan(
			&c.ID, &c.UserID, &c.Provider, &c.ServerID, &c.ServerName, &c.BaseURL, &c.AccessToken, &c.DefaultProfileID,
			&c.WebhookSecret, &c.AccountDiscoveryAvailable,
			&c.LastWebhookReceivedAt, &c.LastWebhookErrorAt, &c.LastWebhookErrorMessage,
			&c.CreatedAt, &c.UpdatedAt, &c.UserCount,
		); err != nil {
			return nil, fmt.Errorf("scanning webhook sync connection: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating webhook sync connections: %w", err)
	}
	return out, nil
}

func (r *Repository) GetConnection(ctx context.Context, userID int, id string) (*Connection, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, provider, server_id, server_name, base_url, access_token, default_profile_id,
		       webhook_secret, account_discovery_available,
		       last_webhook_received_at, last_webhook_error_at, COALESCE(last_webhook_error_message, ''),
		       created_at, updated_at
		FROM webhook_sync_connections
		WHERE id = $1 AND user_id = $2`, id, userID)
	return scanConnection(row)
}

func (r *Repository) GetConnectionBySecret(ctx context.Context, secret string) (*Connection, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, provider, server_id, server_name, base_url, access_token, default_profile_id,
		       webhook_secret, account_discovery_available,
		       last_webhook_received_at, last_webhook_error_at, COALESCE(last_webhook_error_message, ''),
		       created_at, updated_at
		FROM webhook_sync_connections
		WHERE webhook_secret = $1`, secret)
	return scanConnection(row)
}

func scanConnection(row pgx.Row) (*Connection, error) {
	var c Connection
	if err := row.Scan(
		&c.ID, &c.UserID, &c.Provider, &c.ServerID, &c.ServerName, &c.BaseURL, &c.AccessToken, &c.DefaultProfileID,
		&c.WebhookSecret, &c.AccountDiscoveryAvailable,
		&c.LastWebhookReceivedAt, &c.LastWebhookErrorAt, &c.LastWebhookErrorMessage,
		&c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}
		return nil, fmt.Errorf("scanning webhook sync connection: %w", err)
	}
	return &c, nil
}

func (r *Repository) UpdateConnection(ctx context.Context, userID int, id string, input UpdateConnectionInput) (*Connection, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE webhook_sync_connections
		SET server_name = COALESCE($3, server_name),
		    default_profile_id = COALESCE($4, default_profile_id),
		    updated_at = NOW()
		WHERE id = $1 AND user_id = $2
		RETURNING id, user_id, provider, server_id, server_name, base_url, access_token, default_profile_id,
		          webhook_secret, account_discovery_available,
		          last_webhook_received_at, last_webhook_error_at, COALESCE(last_webhook_error_message, ''),
		          created_at, updated_at`,
		id, userID, input.ServerName, input.DefaultProfileID,
	)
	return scanConnection(row)
}

func (r *Repository) DeleteConnection(ctx context.Context, userID int, id string) error {
	result, err := r.pool.Exec(ctx, `DELETE FROM webhook_sync_connections WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("deleting webhook sync connection: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrConnectionNotFound
	}
	return nil
}

func (r *Repository) UpdateWebhookSecret(ctx context.Context, userID int, id, secret string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE webhook_sync_connections
		SET webhook_secret = $3, updated_at = NOW()
		WHERE id = $1 AND user_id = $2`, id, userID, secret)
	if err != nil {
		return fmt.Errorf("updating webhook secret: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrConnectionNotFound
	}
	return nil
}

func (r *Repository) SetDiscoveryAvailable(ctx context.Context, connectionID string, available bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE webhook_sync_connections
		SET account_discovery_available = $2, updated_at = NOW()
		WHERE id = $1`, connectionID, available)
	if err != nil {
		return fmt.Errorf("updating discovery availability: %w", err)
	}
	return nil
}

func (r *Repository) ListMappings(ctx context.Context, connectionID string) ([]ProfileMapping, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, connection_id, external_user_id, external_user_name, silo_profile_id,
		       last_seen_at, created_at, updated_at
		FROM webhook_sync_profile_mappings
		WHERE connection_id = $1
		ORDER BY external_user_name ASC, id ASC`, connectionID)
	if err != nil {
		return nil, fmt.Errorf("listing webhook sync mappings: %w", err)
	}
	defer rows.Close()
	var out []ProfileMapping
	for rows.Next() {
		var m ProfileMapping
		if err := rows.Scan(
			&m.ID, &m.ConnectionID, &m.ExternalUserID, &m.ExternalUserName, &m.SiloProfileID,
			&m.LastSeenAt, &m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning webhook sync mapping: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating webhook sync mappings: %w", err)
	}
	return out, nil
}

func (r *Repository) GetMappingByUser(ctx context.Context, connectionID, externalUserID string) (*ProfileMapping, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, connection_id, external_user_id, external_user_name, silo_profile_id,
		       last_seen_at, created_at, updated_at
		FROM webhook_sync_profile_mappings
		WHERE connection_id = $1 AND external_user_id = $2`, connectionID, externalUserID)
	var m ProfileMapping
	if err := row.Scan(
		&m.ID, &m.ConnectionID, &m.ExternalUserID, &m.ExternalUserName, &m.SiloProfileID,
		&m.LastSeenAt, &m.CreatedAt, &m.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting webhook sync mapping: %w", err)
	}
	return &m, nil
}

func (r *Repository) ReplaceMappings(ctx context.Context, connectionID string, mappings []UpdateProfileMapping) ([]ProfileMapping, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin replace webhook mappings: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM webhook_sync_profile_mappings WHERE connection_id = $1`, connectionID); err != nil {
		return nil, fmt.Errorf("deleting webhook sync mappings: %w", err)
	}

	out := make([]ProfileMapping, 0, len(mappings))
	for _, input := range mappings {
		row := tx.QueryRow(ctx, `
			INSERT INTO webhook_sync_profile_mappings (
				connection_id, external_user_id, external_user_name, silo_profile_id, last_seen_at
			) VALUES ($1, $2, $3, $4, NOW())
			RETURNING id, connection_id, external_user_id, external_user_name, silo_profile_id,
			          last_seen_at, created_at, updated_at`,
			connectionID, input.ExternalUserID, input.ExternalUserName, input.SiloProfileID,
		)
		var m ProfileMapping
		if err := row.Scan(
			&m.ID, &m.ConnectionID, &m.ExternalUserID, &m.ExternalUserName, &m.SiloProfileID,
			&m.LastSeenAt, &m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("creating webhook sync mapping: %w", err)
		}
		out = append(out, m)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit replace webhook mappings: %w", err)
	}
	return out, nil
}

func (r *Repository) CreateDefaultMapping(ctx context.Context, connectionID, externalUserID, externalUserName, profileID string) (*ProfileMapping, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO webhook_sync_profile_mappings (
			connection_id, external_user_id, external_user_name, silo_profile_id, last_seen_at
		) VALUES ($1, $2, $3, $4, NOW())
		RETURNING id, connection_id, external_user_id, external_user_name, silo_profile_id,
		          last_seen_at, created_at, updated_at`,
		connectionID, externalUserID, externalUserName, &profileID,
	)
	var m ProfileMapping
	if err := row.Scan(
		&m.ID, &m.ConnectionID, &m.ExternalUserID, &m.ExternalUserName, &m.SiloProfileID,
		&m.LastSeenAt, &m.CreatedAt, &m.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("creating default webhook mapping: %w", err)
	}
	return &m, nil
}

func (r *Repository) UpsertSeenUser(ctx context.Context, connectionID, externalUserID, externalUserName string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO webhook_sync_profile_mappings (
			connection_id, external_user_id, external_user_name, last_seen_at
		) VALUES ($1, $2, $3, NOW())
		ON CONFLICT (connection_id, external_user_id) DO UPDATE SET
			external_user_name = EXCLUDED.external_user_name,
			last_seen_at = NOW(),
			updated_at = NOW()`,
		connectionID, externalUserID, externalUserName,
	)
	if err != nil {
		return fmt.Errorf("upserting seen external user: %w", err)
	}
	return nil
}

func (r *Repository) GetItemState(ctx context.Context, connectionID, externalUserID, externalItemID string) (*ItemState, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT connection_id, external_user_id, external_item_id, COALESCE(media_item_id, ''),
		       last_event_at, last_completed, last_position_seconds, updated_at
		FROM webhook_sync_item_state
		WHERE connection_id = $1 AND external_user_id = $2 AND external_item_id = $3`,
		connectionID, externalUserID, externalItemID,
	)
	var state ItemState
	if err := row.Scan(
		&state.ConnectionID, &state.ExternalUserID, &state.ExternalItemID, &state.MediaItemID,
		&state.LastEventAt, &state.LastCompleted, &state.LastPositionSecond, &state.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting webhook item state: %w", err)
	}
	return &state, nil
}

func (r *Repository) UpsertItemState(ctx context.Context, state ItemState) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO webhook_sync_item_state (
			connection_id, external_user_id, external_item_id, media_item_id,
			last_event_at, last_completed, last_position_seconds, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (connection_id, external_user_id, external_item_id) DO UPDATE SET
			media_item_id = EXCLUDED.media_item_id,
			last_event_at = EXCLUDED.last_event_at,
			last_completed = EXCLUDED.last_completed,
			last_position_seconds = EXCLUDED.last_position_seconds,
			updated_at = NOW()`,
		state.ConnectionID, state.ExternalUserID, state.ExternalItemID, state.MediaItemID,
		state.LastEventAt, state.LastCompleted, state.LastPositionSecond,
	)
	if err != nil {
		return fmt.Errorf("upserting webhook item state: %w", err)
	}
	return nil
}

func (r *Repository) MarkWebhookReceived(ctx context.Context, connectionID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE webhook_sync_connections
		SET last_webhook_received_at = NOW(),
		    last_webhook_error_at = NULL,
		    last_webhook_error_message = NULL,
		    updated_at = NOW()
		WHERE id = $1`, connectionID)
	if err != nil {
		return fmt.Errorf("marking webhook receipt: %w", err)
	}
	return nil
}

func (r *Repository) MarkWebhookError(ctx context.Context, connectionID, message string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE webhook_sync_connections
		SET last_webhook_error_at = NOW(), last_webhook_error_message = $2, updated_at = NOW()
		WHERE id = $1`, connectionID, message)
	if err != nil {
		return fmt.Errorf("marking webhook error: %w", err)
	}
	return nil
}
