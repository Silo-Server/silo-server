package pgstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// preferenceSettingsExecutor is implemented by both pgxpool.Pool and pgx.Tx.
// Keeping the SQL helpers on this narrow interface lets ordinary store calls
// and the atomic legacy/canonical synchronization path share the same queries.
type preferenceSettingsExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type preferenceSettingsTx struct {
	exec   preferenceSettingsExecutor
	userID int
}

var _ userstore.PreferenceSettingsTransactioner = (*PostgresUserStore)(nil)

func (s *PostgresUserStore) WithPreferenceSettingsTransaction(
	ctx context.Context,
	fn func(userstore.PreferenceSettingsWriter) error,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning preference settings transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := fn(&preferenceSettingsTx{exec: tx, userID: s.userID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing preference settings transaction: %w", err)
	}
	return nil
}

func (tx *preferenceSettingsTx) UpdateProfile(
	ctx context.Context,
	id string,
	u userstore.UpdateProfileInput,
) error {
	return updateProfile(ctx, tx.exec, tx.userID, id, u)
}

func (tx *preferenceSettingsTx) SetSubtitlePreference(ctx context.Context, pref userstore.SubtitlePreference) error {
	return setSubtitlePreference(ctx, tx.exec, tx.userID, pref)
}

func (tx *preferenceSettingsTx) DeleteSubtitlePreference(ctx context.Context, profileID, seriesID string) error {
	return deleteSubtitlePreference(ctx, tx.exec, tx.userID, profileID, seriesID)
}

func (tx *preferenceSettingsTx) SetAudioPreference(ctx context.Context, pref userstore.AudioPreference) error {
	return setAudioPreference(ctx, tx.exec, tx.userID, pref)
}

func (tx *preferenceSettingsTx) DeleteAudioPreference(ctx context.Context, profileID, seriesID string) error {
	return deleteAudioPreference(ctx, tx.exec, tx.userID, profileID, seriesID)
}

func (tx *preferenceSettingsTx) UpsertLibraryPlaybackPreference(ctx context.Context, pref userstore.LibraryPlaybackPreference) error {
	return upsertLibraryPlaybackPreference(ctx, tx.exec, tx.userID, pref)
}

func (tx *preferenceSettingsTx) DeleteLibraryPlaybackPreference(ctx context.Context, profileID string, libraryID int) error {
	return deleteLibraryPlaybackPreference(ctx, tx.exec, tx.userID, profileID, libraryID)
}

func (tx *preferenceSettingsTx) UpsertSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
) (*userstore.SettingValue, error) {
	return upsertSettingValue(ctx, tx.exec, tx.userID, id, value)
}

func (tx *preferenceSettingsTx) DeleteSettingValue(ctx context.Context, id userstore.SettingIdentity) (bool, error) {
	return deleteSettingValue(ctx, tx.exec, tx.userID, id)
}
