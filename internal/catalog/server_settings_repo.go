package catalog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ServerSettingsRepo provides CRUD access to the server_settings table.
type ServerSettingsRepo struct {
	pool *pgxpool.Pool
}

// NewServerSettingsRepo creates a new ServerSettingsRepo.
func NewServerSettingsRepo(pool *pgxpool.Pool) *ServerSettingsRepo {
	return &ServerSettingsRepo{pool: pool}
}

// Get retrieves a single setting by key. Returns empty string if not found.
func (r *ServerSettingsRepo) Get(ctx context.Context, key string) (string, error) {
	var value string
	err := r.pool.QueryRow(ctx,
		`SELECT value FROM server_settings WHERE key = $1`, key,
	).Scan(&value)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return "", nil
		}
		return "", fmt.Errorf("server_settings get %q: %w", key, err)
	}
	return value, nil
}

// Set upserts a setting.
func (r *ServerSettingsRepo) Set(ctx context.Context, key, value string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO server_settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("server_settings set %q: %w", key, err)
	}
	return nil
}

// SetMany atomically upserts a related group of settings. Credential bundles
// use this so readers can never observe a URL, identifier, or secret from
// different relay generations after a partial write.
func (r *ServerSettingsRepo) SetMany(ctx context.Context, values map[string]string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("server_settings begin batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for key, value := range values {
		if _, err := tx.Exec(ctx,
			`INSERT INTO server_settings (key, value) VALUES ($1, $2)
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
			key, value,
		); err != nil {
			return fmt.Errorf("server_settings batch set %q: %w", key, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("server_settings commit batch: %w", err)
	}
	return nil
}

// SetIfAbsent inserts a setting only when the key has no value yet (absent or
// empty), reporting whether this call won the write. Generated credentials
// (e.g. the web push VAPID keypair) must be provisioned single-writer across
// concurrent nodes: exactly one generated value may ever land.
func (r *ServerSettingsRepo) SetIfAbsent(ctx context.Context, key, value string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO server_settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
		 WHERE server_settings.value = ''`,
		key, value,
	)
	if err != nil {
		return false, fmt.Errorf("server_settings set-if-absent %q: %w", key, err)
	}
	return tag.RowsAffected() > 0, nil
}

// GetAll retrieves all settings as a map.
func (r *ServerSettingsRepo) GetAll(ctx context.Context) (map[string]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT key, value FROM server_settings`)
	if err != nil {
		return nil, fmt.Errorf("server_settings get all: %w", err)
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("server_settings scan: %w", err)
		}
		settings[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("server_settings iterate: %w", err)
	}
	return settings, nil
}
