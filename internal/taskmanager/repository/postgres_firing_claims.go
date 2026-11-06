package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// firingClaimRetention is how long a claim is kept when its window is
// shorter: longer than a weekly trigger's period, so a slot is never claimed
// twice.
const firingClaimRetention = 8 * 24 * time.Hour

// PgFiringClaims implements taskmanager.FiringClaims on task_firing_claims.
type PgFiringClaims struct {
	pool *pgxpool.Pool
}

func NewPgFiringClaims(pool *pgxpool.Pool) *PgFiringClaims {
	return &PgFiringClaims{pool: pool}
}

// ClaimFiring takes a transaction-scoped advisory lock on the task, so claims
// of one task run one at a time, then inserts the claim unless one exists at
// slot or within window. The row, not the lock, is what a later firing on
// another server checks, so firings a few seconds apart are caught as well as
// concurrent ones. Claims of every task older than firingClaimRetention,
// which is longer than any window, are pruned in the same transaction, so a
// task that stopped firing (an uninstalled plugin's) doesn't keep its claims.
func (r *PgFiringClaims) ClaimFiring(ctx context.Context, key string, slot time.Time, window time.Duration) (bool, error) {
	window = max(window, 0)
	won := false
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('task_firing_claims:' || $1, 0))`, key); err != nil {
			return fmt.Errorf("locking task firing claims: %w", err)
		}
		var taken bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM task_firing_claims
				WHERE task_key = $1
				  AND (slot = $2 OR claimed_at > clock_timestamp() - $3 * interval '1 microsecond')
			)`, key, slot, window.Microseconds()).Scan(&taken); err != nil {
			return fmt.Errorf("reading task firing claims: %w", err)
		}
		if taken {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_firing_claims (task_key, slot, claimed_at) VALUES ($1, $2, clock_timestamp())`,
			key, slot); err != nil {
			return fmt.Errorf("claiming task firing: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM task_firing_claims
			WHERE claimed_at < clock_timestamp() - $1 * interval '1 microsecond'`,
			max(window, firingClaimRetention).Microseconds()); err != nil {
			return fmt.Errorf("pruning task firing claims: %w", err)
		}
		won = true
		return nil
	})
	return won, err
}
