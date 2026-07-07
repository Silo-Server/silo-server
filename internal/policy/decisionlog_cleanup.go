package policy

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const decisionLogCleanupBatchSize = 10000

// DecisionLogSettingsStore is satisfied by *catalog.ServerSettingsRepo.
type DecisionLogSettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
}

// DecisionLogPartitionManager is satisfied by *partman.Manager.
type DecisionLogPartitionManager interface {
	EnsureFuturePartitions(ctx context.Context) error
	DropExpiredPartitions(ctx context.Context, cutoff time.Time) ([]string, error)
	DeleteExpiredRowsFromDefault(ctx context.Context, cutoff time.Time) (int64, error)
}

// CleanupDecisionLogsOnce runs one policy decision log retention pass. It
// returns the number of deleted rows plus the first error encountered, so
// callers (the task manager) can report a degraded pass instead of silently
// letting policy_decisions grow unbounded.
func CleanupDecisionLogsOnce(
	ctx context.Context,
	pool *pgxpool.Pool,
	store DecisionLogSettingsStore,
	pm DecisionLogPartitionManager,
) (int64, error) {
	var firstErr error
	days := DefaultDecisionLogRetentionDays
	if store != nil {
		if raw, err := store.Get(ctx, SettingDecisionLogRetentionDays); err == nil && raw != "" {
			if parsed := parsePositiveInt(raw); parsed > 0 {
				days = parsed
			}
		}
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	if pm != nil {
		if err := pm.EnsureFuturePartitions(ctx); err != nil {
			slog.Warn("policy decision log ensure future partitions error", "error", err)
			firstErr = err
		}

		partitionCleanupFailed := false
		totalDeleted := int64(0)
		if dropped, err := pm.DropExpiredPartitions(ctx, cutoff); err != nil {
			slog.Warn("policy decision log partition cleanup error", "error", err)
			partitionCleanupFailed = true
			if firstErr == nil {
				firstErr = err
			}
		} else if len(dropped) > 0 {
			slog.Info("policy decision log dropped expired partitions", "partitions", dropped)
		}

		if deleted, err := pm.DeleteExpiredRowsFromDefault(ctx, cutoff); err != nil {
			slog.Warn("policy decision log default partition cleanup error", "error", err)
			partitionCleanupFailed = true
			if firstErr == nil {
				firstErr = err
			}
		} else if deleted > 0 {
			totalDeleted += deleted
			slog.Info("policy decision log default partition cleanup completed", "deleted", deleted, "retention_days", days)
		}

		if !partitionCleanupFailed {
			return totalDeleted, firstErr
		}
		slog.Warn("policy decision log partition cleanup degraded, falling back to row deletes", "retention_days", days)
	}

	total, err := deleteExpiredDecisionRowsBefore(ctx, pool, cutoff)
	if err != nil && firstErr == nil {
		firstErr = err
	}
	if total > 0 {
		slog.Info("policy decision log cleanup completed", "deleted", total, "retention_days", days)
	}
	return total, firstErr
}

func deleteExpiredDecisionRowsBefore(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time) (int64, error) {
	if pool == nil {
		return 0, nil
	}
	total := int64(0)
	for {
		result, err := pool.Exec(ctx, `
			DELETE FROM policy_decisions
			WHERE (id, "timestamp") IN (
				SELECT id, "timestamp" FROM policy_decisions
				WHERE "timestamp" < $1
				LIMIT $2
			)
			`, cutoff, decisionLogCleanupBatchSize)
		if err != nil {
			slog.Warn("policy decision log cleanup error", "error", err)
			return total, err
		}
		deleted := result.RowsAffected()
		total += deleted
		if deleted < int64(decisionLogCleanupBatchSize) {
			break
		}
	}
	return total, nil
}

func parsePositiveInt(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return v
}
