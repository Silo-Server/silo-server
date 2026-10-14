package nodepool

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// The SILO/WORK namespace is reserved for worker protocol classification. Keep
// these values aligned with worker_registry_classification_fence.sql.
const (
	ConfigurationLockNamespace int32 = 1397312591
	WorkerRegistryLockKey      int32 = 1464816203
)

// LockConfiguration must precede parent, enrollment-ticket and configuration
// generation row locks. Database triggers take the same lock for direct SQL.
func LockConfiguration(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1,$2)", ConfigurationLockNamespace, WorkerRegistryLockKey)
	return err
}
