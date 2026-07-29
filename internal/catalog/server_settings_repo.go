package catalog

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const serverSettingsMutationLock = "silo:server_settings:mutation"

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
	return r.withMutationTransaction(ctx, func(tx pgx.Tx) error {
		return upsertServerSettings(ctx, tx, map[string]string{key: value})
	})
}

// SetMany atomically upserts a related group of settings. Credential bundles
// use this so readers can never observe a URL, identifier, or secret from
// different relay generations after a partial write.
func (r *ServerSettingsRepo) SetMany(ctx context.Context, values map[string]string) error {
	return r.withMutationTransaction(ctx, func(tx pgx.Tx) error {
		return upsertServerSettings(ctx, tx, values)
	})
}

// UpdateAtomic serializes a read/validate/write settings mutation across every
// Silo process sharing the database. The callback receives the current
// snapshot while the transaction-scoped advisory lock is held and returns the
// subset of values to upsert.
func (r *ServerSettingsRepo) UpdateAtomic(
	ctx context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	return r.withMutationTransaction(ctx, func(tx pgx.Tx) error {
		current, err := getAllServerSettings(ctx, tx)
		if err != nil {
			return err
		}
		writes, err := update(current)
		if err != nil {
			return err
		}
		return upsertServerSettings(ctx, tx, writes)
	})
}

func (r *ServerSettingsRepo) withMutationTransaction(
	ctx context.Context,
	mutate func(tx pgx.Tx) error,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("server_settings begin mutation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		serverSettingsMutationLock,
	); err != nil {
		return fmt.Errorf("server_settings acquire mutation lock: %w", err)
	}
	if err := mutate(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("server_settings commit mutation: %w", err)
	}
	return nil
}

type serverSettingsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func upsertServerSettings(ctx context.Context, tx pgx.Tx, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.Exec(ctx,
			`INSERT INTO server_settings (key, value) VALUES ($1, $2)
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
			key, values[key],
		); err != nil {
			return fmt.Errorf("server_settings set %q: %w", key, err)
		}
	}
	return nil
}

func getAllServerSettings(ctx context.Context, querier serverSettingsQuerier) (map[string]string, error) {
	rows, err := querier.Query(ctx, `SELECT key, value FROM server_settings`)
	if err != nil {
		return nil, fmt.Errorf("server_settings get all: %w", err)
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("server_settings scan: %w", err)
		}
		settings[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("server_settings iterate: %w", err)
	}
	return settings, nil
}

// SetIfAbsent inserts a setting only when the key has no value yet (absent or
// empty), reporting whether this call won the write. Generated credentials
// (e.g. the web push VAPID keypair) must be provisioned single-writer across
// concurrent nodes: exactly one generated value may ever land.
func (r *ServerSettingsRepo) SetIfAbsent(ctx context.Context, key, value string) (bool, error) {
	var inserted bool
	err := r.withMutationTransaction(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO server_settings (key, value) VALUES ($1, $2)
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
			 WHERE server_settings.value = ''`,
			key, value,
		)
		if err != nil {
			return fmt.Errorf("server_settings set-if-absent %q: %w", key, err)
		}
		inserted = tag.RowsAffected() > 0
		return nil
	})
	return inserted, err
}

// GetAll retrieves all settings as a map.
func (r *ServerSettingsRepo) GetAll(ctx context.Context) (map[string]string, error) {
	return getAllServerSettings(ctx, r.pool)
}
