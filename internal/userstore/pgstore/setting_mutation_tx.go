package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const settingMutationAdvisoryClass int32 = 0x534d5554 // "SMUT"

type postgresSettingMutationWriter struct {
	exec   preferenceSettingsExecutor
	userID int
}

var _ userstore.SettingMutationTransactioner = (*PostgresUserStore)(nil)
var _ userstore.SettingMutationWriter = (*postgresSettingMutationWriter)(nil)

// WithSettingMutationTransaction takes a transaction-scoped advisory lock
// before reading mutation_id. The hash includes the user, so unrelated
// accounts and mutation ids remain concurrent; a hash collision only causes
// harmless extra serialization. Everything the callback writes commits together.
//
// An empty mutationID means the caller has no receipt to guard, so there is
// nothing to serialize on and the lock is skipped: two writes to one identity
// still order themselves on that row's own lock, which is what makes a mirrored
// pair written in a fixed key order safe to run concurrently.
func (s *PostgresUserStore) WithSettingMutationTransaction(
	ctx context.Context,
	mutationID string,
	fn func(userstore.SettingMutationWriter) error,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning setting mutation transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if mutationID != "" {
		lockHash := fnv.New32a()
		_, _ = lockHash.Write([]byte(strconv.Itoa(s.userID)))
		_, _ = lockHash.Write([]byte{0})
		_, _ = lockHash.Write([]byte(mutationID))
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)",
			settingMutationAdvisoryClass, int32(lockHash.Sum32())); err != nil {
			return fmt.Errorf("locking setting mutation transaction: %w", err)
		}
	}

	writer := &postgresSettingMutationWriter{exec: tx, userID: s.userID}
	if err := fn(writer); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing setting mutation transaction: %w", err)
	}
	return nil
}

func (w *postgresSettingMutationWriter) GetSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
) (*userstore.SettingValue, error) {
	return getSettingValue(ctx, w.exec, w.userID, id)
}

func (w *postgresSettingMutationWriter) UpsertSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
) (*userstore.SettingValue, error) {
	return upsertSettingValue(ctx, w.exec, w.userID, id, value)
}

func (w *postgresSettingMutationWriter) DeleteSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
) (bool, error) {
	return deleteSettingValue(ctx, w.exec, w.userID, id)
}

func (w *postgresSettingMutationWriter) UpdateProfile(
	ctx context.Context,
	id string,
	u userstore.UpdateProfileInput,
) error {
	return updateProfile(ctx, w.exec, w.userID, id, u)
}

func (w *postgresSettingMutationWriter) CompareAndSetSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
	expectedRevision int64,
) (*userstore.SettingValue, error) {
	return compareAndSetSettingValue(ctx, w.exec, w.userID, id, value, expectedRevision)
}

func (w *postgresSettingMutationWriter) GetSettingMutation(
	ctx context.Context,
	mutationID string,
) (*userstore.SettingMutationRecord, error) {
	return getSettingMutation(ctx, w.exec, w.userID, mutationID)
}

func (w *postgresSettingMutationWriter) PutSettingMutation(
	ctx context.Context,
	record userstore.SettingMutationRecord,
) (userstore.SettingMutationRecord, bool, error) {
	return putSettingMutation(ctx, w.exec, w.userID, record)
}
