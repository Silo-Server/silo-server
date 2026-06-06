package pgstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *PostgresUserStore) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		"SELECT value FROM user_settings WHERE user_id = $1 AND key = $2",
		s.userID, key,
	).Scan(&value)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting setting %q: %w", key, err)
	}
	return value, nil
}

func (s *PostgresUserStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)
		 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`,
		s.userID, key, value,
	)
	if err != nil {
		return fmt.Errorf("setting %q: %w", key, err)
	}
	return nil
}

func (s *PostgresUserStore) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx,
		"DELETE FROM user_settings WHERE user_id = $1 AND key = $2",
		s.userID, key,
	)
	if err != nil {
		return fmt.Errorf("deleting setting %q: %w", key, err)
	}
	return nil
}

func (s *PostgresUserStore) ListSettings(ctx context.Context) ([]userstore.SettingEntry, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT key, value FROM user_settings WHERE user_id = $1 ORDER BY key",
		s.userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing settings: %w", err)
	}
	defer rows.Close()

	var entries []userstore.SettingEntry
	for rows.Next() {
		var e userstore.SettingEntry
		if err := rows.Scan(&e.Key, &e.Value); err != nil {
			return nil, fmt.Errorf("scanning setting: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresUserStore) GetDeviceSetting(ctx context.Context, profileID, deviceID, key string) (*userstore.DeviceSettingEntry, error) {
	var entry userstore.DeviceSettingEntry
	err := s.pool.QueryRow(ctx,
		`SELECT profile_id, device_id, device_name, device_platform, key, value, updated_at::text
		 FROM user_device_settings
		 WHERE user_id = $1 AND profile_id = $2 AND device_id = $3 AND key = $4`,
		s.userID, profileID, deviceID, key,
	).Scan(&entry.ProfileID, &entry.DeviceID, &entry.DeviceName, &entry.DevicePlatform, &entry.Key, &entry.Value, &entry.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting device setting %q for device %q: %w", key, deviceID, err)
	}
	return &entry, nil
}

func (s *PostgresUserStore) RegisterDevice(ctx context.Context, entry userstore.DeviceEntry) error {
	if strings.TrimSpace(entry.ProfileID) == "" || strings.TrimSpace(entry.DeviceID) == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_devices
			(user_id, profile_id, device_id, device_name, device_platform, last_seen_at)
		 VALUES ($1, $2, $3, $4, $5, NOW())
		 ON CONFLICT(user_id, profile_id, device_id) DO UPDATE SET
			device_name = CASE
				WHEN excluded.device_name <> '' THEN excluded.device_name
				ELSE user_devices.device_name
			END,
			device_platform = CASE
				WHEN excluded.device_platform <> '' THEN excluded.device_platform
				ELSE user_devices.device_platform
			END,
			last_seen_at = NOW()`,
		s.userID, entry.ProfileID, entry.DeviceID, entry.DeviceName, entry.DevicePlatform,
	)
	if err != nil {
		return fmt.Errorf("registering device %q: %w", entry.DeviceID, err)
	}
	return nil
}

func (s *PostgresUserStore) ListDevices(ctx context.Context) ([]userstore.DeviceEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT profile_id, device_id, device_name, device_platform, last_seen_at::text
		 FROM user_devices
		 WHERE user_id = $1
		 ORDER BY last_seen_at DESC, profile_id ASC, device_name ASC, device_id ASC`,
		s.userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing devices: %w", err)
	}
	defer rows.Close()

	var entries []userstore.DeviceEntry
	for rows.Next() {
		var entry userstore.DeviceEntry
		if err := rows.Scan(&entry.ProfileID, &entry.DeviceID, &entry.DeviceName, &entry.DevicePlatform, &entry.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scanning device: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *PostgresUserStore) SetDeviceSetting(ctx context.Context, entry userstore.DeviceSettingEntry) error {
	if err := s.RegisterDevice(ctx, userstore.DeviceEntry{
		ProfileID:      entry.ProfileID,
		DeviceID:       entry.DeviceID,
		DeviceName:     entry.DeviceName,
		DevicePlatform: entry.DevicePlatform,
	}); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_device_settings
			(user_id, profile_id, device_id, key, value, device_name, device_platform, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		 ON CONFLICT(user_id, profile_id, device_id, key) DO UPDATE SET
			value = excluded.value,
			device_name = excluded.device_name,
			device_platform = excluded.device_platform,
			updated_at = NOW()`,
		s.userID, entry.ProfileID, entry.DeviceID, entry.Key, entry.Value, entry.DeviceName, entry.DevicePlatform,
	)
	if err != nil {
		return fmt.Errorf("setting device setting %q for device %q: %w", entry.Key, entry.DeviceID, err)
	}
	return nil
}

func (s *PostgresUserStore) DeleteDeviceSetting(ctx context.Context, profileID, deviceID, key string) error {
	_, err := s.pool.Exec(ctx,
		"DELETE FROM user_device_settings WHERE user_id = $1 AND profile_id = $2 AND device_id = $3 AND key = $4",
		s.userID, profileID, deviceID, key,
	)
	if err != nil {
		return fmt.Errorf("deleting device setting %q for device %q: %w", key, deviceID, err)
	}
	return nil
}

func (s *PostgresUserStore) DeleteAllDeviceSettings(ctx context.Context, profileID, deviceID string) error {
	_, err := s.pool.Exec(ctx,
		"DELETE FROM user_device_settings WHERE user_id = $1 AND profile_id = $2 AND device_id = $3",
		s.userID, profileID, deviceID,
	)
	if err != nil {
		return fmt.Errorf("deleting all device settings for device %q: %w", deviceID, err)
	}
	return nil
}

func (s *PostgresUserStore) DeleteDeviceSettingsByKey(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx,
		"DELETE FROM user_device_settings WHERE user_id = $1 AND key = $2",
		s.userID, key,
	)
	if err != nil {
		return fmt.Errorf("deleting device settings for key %q: %w", key, err)
	}
	return nil
}

func (s *PostgresUserStore) ListDeviceSettings(ctx context.Context, key string) ([]userstore.DeviceSettingEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT profile_id, device_id, device_name, device_platform, key, value, updated_at::text
		 FROM user_device_settings
		 WHERE user_id = $1 AND key = $2
		 ORDER BY updated_at DESC, profile_id ASC, device_name ASC, device_id ASC`,
		s.userID, key,
	)
	if err != nil {
		return nil, fmt.Errorf("listing device settings for key %q: %w", key, err)
	}
	defer rows.Close()

	var entries []userstore.DeviceSettingEntry
	for rows.Next() {
		var entry userstore.DeviceSettingEntry
		if err := rows.Scan(&entry.ProfileID, &entry.DeviceID, &entry.DeviceName, &entry.DevicePlatform, &entry.Key, &entry.Value, &entry.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning device setting: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *PostgresUserStore) ListAllDeviceSettings(ctx context.Context) ([]userstore.DeviceSettingEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT profile_id, device_id, device_name, device_platform, key, value, updated_at::text
		 FROM user_device_settings
		 WHERE user_id = $1
		 ORDER BY updated_at DESC, profile_id ASC, key ASC, device_name ASC, device_id ASC`,
		s.userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing all device settings: %w", err)
	}
	defer rows.Close()

	var entries []userstore.DeviceSettingEntry
	for rows.Next() {
		var entry userstore.DeviceSettingEntry
		if err := rows.Scan(&entry.ProfileID, &entry.DeviceID, &entry.DeviceName, &entry.DevicePlatform, &entry.Key, &entry.Value, &entry.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning device setting: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
