package userdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// preferenceSettingsExecutor is implemented by both sql.DB and sql.Tx. The
// shared helpers keep transactional synchronization on the same SQL paths as
// ordinary store calls.
type preferenceSettingsExecutor interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
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

func (tx *preferenceSettingsTx) ListProfileIDs(_ context.Context) ([]string, error) {
	rows, err := tx.exec.Query("SELECT id FROM profiles ORDER BY created_at, id")
	if err != nil {
		return nil, fmt.Errorf("listing profile ids: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning profile id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (tx *preferenceSettingsTx) CreateProfile(_ context.Context, profile userstore.Profile) error {
	return createProfile(tx.exec, profile)
}

func (tx *preferenceSettingsTx) ListSettings(_ context.Context) ([]userstore.SettingEntry, error) {
	rows, err := tx.exec.Query("SELECT key, value FROM user_settings ORDER BY key")
	if err != nil {
		return nil, fmt.Errorf("listing settings: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var entries []userstore.SettingEntry
	for rows.Next() {
		var entry userstore.SettingEntry
		if err := rows.Scan(&entry.Key, &entry.Value); err != nil {
			return nil, fmt.Errorf("scanning setting: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
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

func (tx *preferenceSettingsTx) SetSetting(_ context.Context, key, value string) error {
	_, err := tx.exec.Exec(
		"INSERT INTO user_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	if err != nil {
		return fmt.Errorf("setting %q: %w", key, err)
	}
	return nil
}

func (tx *preferenceSettingsTx) DeleteSetting(_ context.Context, key string) error {
	_, err := tx.exec.Exec("DELETE FROM user_settings WHERE key = ?", key)
	if err != nil {
		return fmt.Errorf("deleting setting %q: %w", key, err)
	}
	return nil
}

func (tx *preferenceSettingsTx) SetDeviceSetting(_ context.Context, entry userstore.DeviceSettingEntry) error {
	if strings.TrimSpace(entry.ProfileID) != "" && strings.TrimSpace(entry.DeviceID) != "" {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.exec.Exec(`INSERT INTO user_devices
			(profile_id, device_id, device_name, device_platform, last_seen_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(profile_id, device_id) DO UPDATE SET
				device_name = CASE WHEN excluded.device_name <> '' THEN excluded.device_name ELSE user_devices.device_name END,
				device_platform = CASE WHEN excluded.device_platform <> '' THEN excluded.device_platform ELSE user_devices.device_platform END,
				last_seen_at = excluded.last_seen_at`,
			entry.ProfileID, entry.DeviceID, entry.DeviceName, entry.DevicePlatform, now); err != nil {
			return fmt.Errorf("registering device %q: %w", entry.DeviceID, err)
		}
		if _, err := tx.exec.Exec(`INSERT INTO user_device_settings
			(profile_id, device_id, key, value, device_name, device_platform, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(profile_id, device_id, key) DO UPDATE SET
				value = excluded.value, device_name = excluded.device_name,
				device_platform = excluded.device_platform, updated_at = excluded.updated_at`,
			entry.ProfileID, entry.DeviceID, entry.Key, entry.Value,
			entry.DeviceName, entry.DevicePlatform, now); err != nil {
			return fmt.Errorf("setting device setting %q for device %q: %w", entry.Key, entry.DeviceID, err)
		}
	}
	return nil
}

func (tx *preferenceSettingsTx) DeleteDeviceSetting(_ context.Context, profileID, deviceID, key string) error {
	_, err := tx.exec.Exec(
		"DELETE FROM user_device_settings WHERE profile_id = ? AND device_id = ? AND key = ?",
		profileID, deviceID, key,
	)
	if err != nil {
		return fmt.Errorf("deleting device setting %q for device %q: %w", key, deviceID, err)
	}
	return nil
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
