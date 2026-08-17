package userdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// settingMutationConnExecutor pins every statement to one database/sql
// connection. SQLite cannot request BEGIN IMMEDIATE through sql.Tx without a
// DSN-wide policy, so this adapter lets the mutation transaction acquire its
// write reservation before it reads the receipt.
type settingMutationConnExecutor struct {
	ctx  context.Context
	conn *sql.Conn
}

func (e settingMutationConnExecutor) Exec(query string, args ...any) (sql.Result, error) {
	return e.conn.ExecContext(e.ctx, query, args...)
}

func (e settingMutationConnExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return e.conn.ExecContext(ctx, query, args...)
}

func (e settingMutationConnExecutor) Query(query string, args ...any) (*sql.Rows, error) {
	return e.conn.QueryContext(e.ctx, query, args...)
}

func (e settingMutationConnExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return e.conn.QueryContext(ctx, query, args...)
}

func (e settingMutationConnExecutor) QueryRow(query string, args ...any) *sql.Row {
	return e.conn.QueryRowContext(e.ctx, query, args...)
}

func (e settingMutationConnExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return e.conn.QueryRowContext(ctx, query, args...)
}

type sqliteSettingMutationWriter struct {
	exec preferenceSettingsExecutor
}

var _ userstore.SettingMutationTransactioner = (*SQLiteUserStore)(nil)
var _ userstore.SettingMutationWriter = (*sqliteSettingMutationWriter)(nil)

// WithSettingMutationTransaction uses BEGIN IMMEDIATE so same-database
// contenders serialize before checking mutation_id. Everything the callback
// writes then commits together; rollback covers callback errors and crashes.
//
// An empty mutationID is a mutation with no idempotency receipt. This backend
// needs no special case for it: BEGIN IMMEDIATE already serializes every writer
// against this user's database, so the atomicity guarantee is the same one and
// the only thing missing is a receipt to record.
func (s *SQLiteUserStore) WithSettingMutationTransaction(
	ctx context.Context,
	mutationID string,
	fn func(userstore.SettingMutationWriter) error,
) (err error) {
	_ = mutationID // serialization here is per-database, not per-mutation id
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("opening setting mutation connection: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	exec := settingMutationConnExecutor{ctx: ctx, conn: conn}
	if _, err := exec.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("beginning setting mutation transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx := context.WithoutCancel(ctx)
			if _, rollbackErr := exec.ExecContext(rollbackCtx, "ROLLBACK"); rollbackErr != nil {
				slog.ErrorContext(rollbackCtx, "rolling back setting mutation transaction failed",
					"component", "userdb.settings", "error", rollbackErr)
				// A failed rollback can leave SQLite's write reservation attached to
				// the pooled driver connection. Mark it bad so database/sql discards
				// it instead of returning a poisoned connection to the pool.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()

	if err := fn(&sqliteSettingMutationWriter{exec: exec}); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("committing setting mutation transaction: %w", err)
	}
	committed = true
	return nil
}

func (w *sqliteSettingMutationWriter) GetSettingValue(
	_ context.Context,
	id userstore.SettingIdentity,
) (*userstore.SettingValue, error) {
	return getSettingValue(w.exec, id)
}

func (w *sqliteSettingMutationWriter) UpsertSettingValue(
	_ context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
) (*userstore.SettingValue, error) {
	return upsertSettingValue(w.exec, id, value)
}

func (w *sqliteSettingMutationWriter) DeleteSettingValue(
	_ context.Context,
	id userstore.SettingIdentity,
) (bool, error) {
	return deleteSettingValue(w.exec, id)
}

func (w *sqliteSettingMutationWriter) UpdateProfile(
	_ context.Context,
	id string,
	u userstore.UpdateProfileInput,
) error {
	return updateProfile(w.exec, id, u)
}

func (w *sqliteSettingMutationWriter) CompareAndSetSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
	expectedRevision int64,
) (*userstore.SettingValue, error) {
	return compareAndSetSettingValue(ctx, w.exec, id, value, expectedRevision)
}

func (w *sqliteSettingMutationWriter) GetSettingMutation(
	_ context.Context,
	mutationID string,
) (*userstore.SettingMutationRecord, error) {
	return getSettingMutation(w.exec, mutationID)
}

func (w *sqliteSettingMutationWriter) PutSettingMutation(
	_ context.Context,
	record userstore.SettingMutationRecord,
) (userstore.SettingMutationRecord, bool, error) {
	return putSettingMutation(w.exec, record)
}
