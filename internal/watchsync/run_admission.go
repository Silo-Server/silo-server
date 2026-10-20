package watchsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const manualSyncTrigger = "manual"

// syncRunAdmission is returned only after the reservation transaction commits.
// Cooldown is an outcome so retiring stale rows commits before it is reported.
type syncRunAdmission struct {
	Connection        Connection
	Run               SyncRun
	Created           bool
	RetryAfterSeconds int
}

// AdmitSyncRun serializes manual admission, scheduled insertion and completion
// on the connection row. The lock never spans provider work. Scheduled runs
// skip a live manual reservation, but retain their existing ability to run
// alongside other scheduled runs, without a new timeout or takeover policy.
func (r *PostgresRepository) AdmitSyncRun(ctx context.Context, expected Connection, run SyncRun) (syncRunAdmission, error) {
	var admitted syncRunAdmission
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		conn, err := r.scanConnection(tx.QueryRow(ctx, `
			SELECT `+connectionColumns+`
			FROM watch_provider_connections
			WHERE provider = $1 AND user_id = $2 AND profile_id = $3
			FOR NO KEY UPDATE
		`, expected.Provider, expected.UserID, expected.ProfileID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConnectionNotFound
		}
		if err != nil {
			return fmt.Errorf("lock watch provider sync connection: %w", err)
		}
		if expected.ID != "" && expected.ID != conn.ID {
			return ErrConnectionNotFound
		}
		admitted.Connection = conn
		// Sample after the row lock: settings or another admission can have
		// held it across a timeout, deferral or cooldown boundary.
		now := r.now().UTC()
		if conn.RateLimitedUntil != nil {
			if remaining := conn.RateLimitedUntil.Sub(now); remaining > 0 {
				admitted.RetryAfterSeconds = ceilSeconds(remaining)
				return nil
			}
		}
		manual := run.Trigger == manualSyncTrigger
		for {
			active, found, err := activeSyncRun(ctx, tx, conn.ID, !manual)
			if err != nil {
				return err
			}
			if !found {
				break
			}
			if !active.StartedAt.IsZero() && now.Sub(active.StartedAt) <= manualSyncTimeout {
				admitted.Run = active
				return nil
			}
			if !manual {
				// Scheduled work neither retires old rows nor waits forever on
				// an orphan manual reservation past its existing deadline.
				break
			}
			active.Status = string(SyncRunStatusFailed)
			active.Error = "watch provider sync timed out before completion"
			active.CompletedAt = new(now)
			if _, err := completeSyncRun(ctx, tx, active); err != nil {
				return err
			}
		}
		if manual {
			admitted.RetryAfterSeconds, err = manualSyncRetryAfter(ctx, tx, conn.ID, now)
			if err != nil || admitted.RetryAfterSeconds > 0 {
				return err
			}
		}
		run.ConnectionID, run.Provider, run.StartedAt = conn.ID, conn.Provider, now
		admitted.Run, err = createSyncRun(ctx, tx, run)
		admitted.Created = err == nil
		return err
	})
	if err != nil {
		return syncRunAdmission{}, fmt.Errorf("admit watch provider sync run: %w", err)
	}
	return admitted, nil
}

func manualSyncRetryAfter(ctx context.Context, tx pgx.Tx, connectionID string, now time.Time) (int, error) {
	latest, err := scanSyncRun(tx.QueryRow(ctx, `
		SELECT `+syncRunColumns+`
		FROM watch_provider_sync_runs
		WHERE connection_id = $1::uuid
		ORDER BY started_at DESC, created_at DESC
		LIMIT 1
	`, connectionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get latest watch provider sync run for admission: %w", err)
	}
	reference := latest.StartedAt
	if latest.CompletedAt != nil {
		reference = *latest.CompletedAt
	}
	return retryAfterSeconds(now, reference, manualSyncCooldown), nil
}

func activeSyncRun(ctx context.Context, tx pgx.Tx, connectionID string, manualOnly bool) (SyncRun, bool, error) {
	run, err := scanSyncRun(tx.QueryRow(ctx, `
		SELECT `+syncRunColumns+`
		FROM watch_provider_sync_runs
		WHERE connection_id = $1::uuid AND status IN ('queued', 'running')
		  AND (NOT $2::boolean OR trigger = 'manual')
		ORDER BY started_at DESC, created_at DESC
		LIMIT 1
	`, connectionID, manualOnly))
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncRun{}, false, nil
	}
	if err != nil {
		return SyncRun{}, false, fmt.Errorf("get active watch provider sync run for admission: %w", err)
	}
	return run, true, nil
}

func (r *PostgresRepository) CompleteSyncRun(ctx context.Context, run SyncRun) (SyncRun, error) {
	var completed SyncRun
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM watch_provider_connections
			WHERE id = $1::uuid FOR NO KEY UPDATE
		`, run.ConnectionID).Scan(&id); err != nil {
			return fmt.Errorf("complete watch provider sync run: %w", err)
		}
		var err error
		// Preserve actual late completion results, including a scheduled run
		// previously marked timed out by manual admission.
		completed, err = completeSyncRun(ctx, tx, run)
		return err
	})
	return completed, err
}
