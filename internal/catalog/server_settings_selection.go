package catalog

import (
	"context"
	"fmt"
)

// UpdateValueAndSettingsAtomic coordinates a machine-managed value with user
// settings. The value lock comes first: waiting for a token refresh must not
// hold the general settings lock. The callback must not perform provider calls
// or request another database connection.
func (r *ServerSettingsRepo) UpdateValueAndSettingsAtomic(ctx context.Context, key string, update func(map[string]string) (map[string]string, error)) error {
	release, err := acquireSettingsValueSlot(ctx, r.pool, key)
	if err != nil {
		return err
	}
	defer release()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, lock := range []string{serverSettingsMutationLock + ":" + key, serverSettingsMutationLock} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lock); err != nil {
			return fmt.Errorf("server_settings acquire selection lock: %w", err)
		}
	}
	current, err := getAllServerSettings(ctx, tx)
	if err != nil {
		return err
	}
	writes, err := update(current)
	if err != nil {
		return err
	}
	if err := upsertServerSettings(ctx, tx, writes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
