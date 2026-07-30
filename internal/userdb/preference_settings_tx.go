package userdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// preferenceSettingsExecutor is implemented by both sql.DB and sql.Tx. The
// shared helpers keep transactional synchronization on the same SQL paths as
// ordinary store calls.
type preferenceSettingsExecutor interface {
	Exec(string, ...any) (sql.Result, error)
	QueryRow(string, ...any) *sql.Row
}

type preferenceSettingsTx struct {
	exec preferenceSettingsExecutor
}

var _ userstore.PreferenceSettingsTransactioner = (*SQLiteUserStore)(nil)

func (s *SQLiteUserStore) WithPreferenceSettingsTransaction(
	ctx context.Context,
	fn func(userstore.PreferenceSettingsWriter) error,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning preference settings transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := fn(&preferenceSettingsTx{exec: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing preference settings transaction: %w", err)
	}
	return nil
}

func (tx *preferenceSettingsTx) UpdateProfile(
	_ context.Context,
	id string,
	u userstore.UpdateProfileInput,
) error {
	return updateProfile(tx.exec, id, u)
}

func (tx *preferenceSettingsTx) SetSubtitlePreference(_ context.Context, pref userstore.SubtitlePreference) error {
	return setSubtitlePreference(tx.exec, pref)
}

func (tx *preferenceSettingsTx) DeleteSubtitlePreference(_ context.Context, profileID, seriesID string) error {
	return deleteSubtitlePreference(tx.exec, profileID, seriesID)
}

func (tx *preferenceSettingsTx) SetAudioPreference(_ context.Context, pref userstore.AudioPreference) error {
	return setAudioPreference(tx.exec, pref)
}

func (tx *preferenceSettingsTx) DeleteAudioPreference(_ context.Context, profileID, seriesID string) error {
	return deleteAudioPreference(tx.exec, profileID, seriesID)
}

func (tx *preferenceSettingsTx) UpsertLibraryPlaybackPreference(_ context.Context, pref userstore.LibraryPlaybackPreference) error {
	return upsertLibraryPlaybackPreference(tx.exec, pref)
}

func (tx *preferenceSettingsTx) DeleteLibraryPlaybackPreference(_ context.Context, profileID string, libraryID int) error {
	return deleteLibraryPlaybackPreference(tx.exec, profileID, libraryID)
}

func (tx *preferenceSettingsTx) UpsertSettingValue(
	_ context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
) (*userstore.SettingValue, error) {
	return upsertSettingValue(tx.exec, id, value)
}

func (tx *preferenceSettingsTx) DeleteSettingValue(_ context.Context, id userstore.SettingIdentity) (bool, error) {
	return deleteSettingValue(tx.exec, id)
}
