package pgstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const preferenceSettingsAdvisoryClass int32 = 0x53494c4f // "SILO"

// preferenceSettingsExecutor is implemented by both pgxpool.Pool and pgx.Tx.
// Keeping the SQL helpers on this narrow interface lets ordinary store calls
// and the atomic legacy/canonical synchronization path share the same queries.
type preferenceSettingsExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
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
	// Serialize legacy account-setting fan-out with profile creation for this
	// user across every server replica. Both paths run through this wrapper.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)",
		preferenceSettingsAdvisoryClass, int32(s.userID)); err != nil {
		return fmt.Errorf("locking preference settings transaction: %w", err)
	}

	if err := fn(&preferenceSettingsTx{exec: tx, userID: s.userID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing preference settings transaction: %w", err)
	}
	return nil
}

func (tx *preferenceSettingsTx) ListProfileIDs(ctx context.Context) ([]string, error) {
	rows, err := tx.exec.Query(ctx,
		"SELECT id FROM user_profiles WHERE user_id = $1 ORDER BY created_at, id", tx.userID)
	if err != nil {
		return nil, fmt.Errorf("listing profile ids: %w", err)
	}
	defer rows.Close()
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

func (tx *preferenceSettingsTx) CreateProfile(ctx context.Context, profile userstore.Profile) error {
	return createProfile(ctx, tx.exec, tx.userID, profile)
}

func (tx *preferenceSettingsTx) ListSettings(ctx context.Context) ([]userstore.SettingEntry, error) {
	rows, err := tx.exec.Query(ctx,
		"SELECT key, value FROM user_settings WHERE user_id = $1 ORDER BY key", tx.userID)
	if err != nil {
		return nil, fmt.Errorf("listing settings: %w", err)
	}
	defer rows.Close()
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

func (tx *preferenceSettingsTx) SetSetting(ctx context.Context, key, value string) error {
	_, err := tx.exec.Exec(ctx,
		`INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)
		 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`,
		tx.userID, key, value,
	)
	if err != nil {
		return fmt.Errorf("setting %q: %w", key, err)
	}
	return nil
}

func (tx *preferenceSettingsTx) DeleteSetting(ctx context.Context, key string) error {
	_, err := tx.exec.Exec(ctx, "DELETE FROM user_settings WHERE user_id = $1 AND key = $2", tx.userID, key)
	if err != nil {
		return fmt.Errorf("deleting setting %q: %w", key, err)
	}
	return nil
}

func (tx *preferenceSettingsTx) SetDeviceSetting(ctx context.Context, entry userstore.DeviceSettingEntry) error {
	if _, err := tx.exec.Exec(ctx, `INSERT INTO user_devices
		(user_id, profile_id, device_id, device_name, device_platform, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT(user_id, profile_id, device_id) DO UPDATE SET
			device_name = CASE WHEN excluded.device_name <> '' THEN excluded.device_name ELSE user_devices.device_name END,
			device_platform = CASE WHEN excluded.device_platform <> '' THEN excluded.device_platform ELSE user_devices.device_platform END,
			last_seen_at = NOW()`,
		tx.userID, entry.ProfileID, entry.DeviceID, entry.DeviceName, entry.DevicePlatform); err != nil {
		return fmt.Errorf("registering device %q: %w", entry.DeviceID, err)
	}
	_, err := tx.exec.Exec(ctx, `INSERT INTO user_device_settings
		(user_id, profile_id, device_id, key, value, device_name, device_platform, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT(user_id, profile_id, device_id, key) DO UPDATE SET
			value = excluded.value, device_name = excluded.device_name,
			device_platform = excluded.device_platform, updated_at = NOW()`,
		tx.userID, entry.ProfileID, entry.DeviceID, entry.Key, entry.Value,
		entry.DeviceName, entry.DevicePlatform,
	)
	if err != nil {
		return fmt.Errorf("setting device setting %q for device %q: %w", entry.Key, entry.DeviceID, err)
	}
	return nil
}

func (tx *preferenceSettingsTx) DeleteDeviceSetting(ctx context.Context, profileID, deviceID, key string) error {
	_, err := tx.exec.Exec(ctx,
		"DELETE FROM user_device_settings WHERE user_id = $1 AND profile_id = $2 AND device_id = $3 AND key = $4",
		tx.userID, profileID, deviceID, key,
	)
	if err != nil {
		return fmt.Errorf("deleting device setting %q for device %q: %w", key, deviceID, err)
	}
	return nil
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
