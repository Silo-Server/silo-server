package userstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// LockImportedHistory takes, in tx, the profile's history lock: a
// profile-scoped database lock that serializes imported-history
// deduplication, history resets and hiding, cutoff-aware progress writes, and
// a restore's merge of plays and hides across API nodes. Hash collisions only serialize unrelated profiles.
func LockImportedHistory(ctx context.Context, tx pgx.Tx, userID int, profileID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::integer, hashtext('imported-history:' || $2))`, userID, profileID)
	if err != nil {
		return fmt.Errorf("lock imported history: %w", err)
	}
	return nil
}
