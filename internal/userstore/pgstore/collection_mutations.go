package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *PostgresUserStore) CollectionOrderRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM user_collection_order_revisions WHERE user_id=$1),1)`, s.userID).Scan(&revision)
	return revision, err
}
func collectionMutationTxOptions(expected *int64) pgx.TxOptions {
	if expected != nil {
		return pgx.TxOptions{IsoLevel: pgx.Serializable}
	}
	return pgx.TxOptions{}
}

// The checks take no counter locks before a target row is mutated. PostgreSQL
// SSI prevents a version read from validating a concurrent committed mutation,
// while existing row->revision trigger ordering remains intact for old writers.
func (s *PostgresUserStore) checkCollectionRevision(ctx context.Context, tx pgx.Tx, id string, expected *int64) error {
	if expected == nil {
		return nil
	}
	var revision int64
	err := tx.QueryRow(ctx, `SELECT r.revision FROM user_collection_revisions r JOIN user_personal_collections c ON c.user_id=r.user_id AND c.id=r.collection_id WHERE r.user_id=$1 AND r.collection_id=$2`, s.userID, id).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return userstore.ErrCollectionRevisionMismatch
	}
	if err != nil {
		return err
	}
	if *expected != -1 && revision != *expected {
		return userstore.ErrCollectionRevisionMismatch
	}
	return nil
}
func (s *PostgresUserStore) checkCollectionOrderRevision(ctx context.Context, tx pgx.Tx, expected *int64) error {
	if expected == nil {
		return nil
	}
	var revision int64
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM user_collection_order_revisions WHERE user_id=$1),1)`, s.userID).Scan(&revision)
	if err != nil {
		return err
	}
	if *expected != -1 && revision != *expected {
		return userstore.ErrCollectionRevisionMismatch
	}
	return nil
}

// Consume a guarded no-op as well as changed rows, but only after target-row
// writes. A deleted collection retains its revision tombstone.
func (s *PostgresUserStore) finishCollectionRevision(ctx context.Context, tx pgx.Tx, id string, expected *int64) error {
	if expected == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE user_collection_revisions SET revision=revision+1 WHERE user_id=$1 AND collection_id=$2`, s.userID, id)
	return err
}
func (s *PostgresUserStore) finishCollectionOrderRevision(ctx context.Context, tx pgx.Tx, expected *int64) error {
	if expected == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO user_collection_order_revisions(user_id,revision) VALUES($1,2) ON CONFLICT(user_id) DO UPDATE SET revision=user_collection_order_revisions.revision+1`, s.userID)
	return err
}

// runCollectionMutation retries only a rolled-back serialization failure, never
// a deadlock, and never substitutes a newer expected revision. The callback is
// an entire database transaction with no external effects. An exact witness is
// reported stale only after a fresh committed read proves it changed; wildcard
// and unrelated SSI conflicts retry at most twice before preserving the error.
func (s *PostgresUserStore) runCollectionMutation(ctx context.Context, id string, expected int64, mutate func() error) error {
	for attempt := range 3 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := mutate()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pgErr, ok := errors.AsType[*pgconn.PgError](err)
		if !ok || pgErr.Code != "40001" {
			return err
		}
		if expected != -1 {
			var current int64
			var readErr error
			if id == "" {
				current, readErr = s.CollectionOrderRevision(ctx)
			} else {
				current, readErr = s.CollectionRevision(ctx, id)
			}
			if errors.Is(readErr, userstore.ErrCollectionNotFound) || (readErr == nil && current != expected) {
				return fmt.Errorf("%w: %w", userstore.ErrCollectionRevisionMismatch, err)
			}
			if readErr != nil {
				return readErr
			}
		}
		if attempt == 2 {
			return err
		}
	}
	panic("unreachable collection mutation retry")
}
