package userdb

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// GetSetting retrieves a setting value by key. Returns empty string if not found.
func GetSetting(db *sql.DB, key string) (string, error) {
	var value string
	err := db.QueryRow("SELECT value FROM user_settings WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting setting %q: %w", key, err)
	}
	return value, nil
}

// SetSetting creates or updates a setting.
func SetSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		"INSERT INTO user_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	if err != nil {
		return fmt.Errorf("setting %q: %w", key, err)
	}
	return nil
}

// DeleteSetting removes a setting by key.
func DeleteSetting(db *sql.DB, key string) error {
	_, err := db.Exec("DELETE FROM user_settings WHERE key = ?", key)
	if err != nil {
		return fmt.Errorf("deleting setting %q: %w", key, err)
	}
	return nil
}

// SettingEntry is an alias for the canonical type in userstore.
type SettingEntry = userstore.SettingEntry

// ListSettings returns all user settings.
func ListSettings(db *sql.DB) ([]SettingEntry, error) {
	rows, err := db.Query("SELECT key, value FROM user_settings ORDER BY key")
	if err != nil {
		return nil, fmt.Errorf("listing settings: %w", err)
	}
	defer rows.Close()

	var entries []SettingEntry
	for rows.Next() {
		var e SettingEntry
		if err := rows.Scan(&e.Key, &e.Value); err != nil {
			return nil, fmt.Errorf("scanning setting: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func GetDeviceSetting(db *sql.DB, profileID, deviceID, key string) (*userstore.DeviceSettingEntry, error) {
	var entry userstore.DeviceSettingEntry
	err := db.QueryRow(
		`SELECT profile_id, device_id, device_name, device_platform, key, value, updated_at
		 FROM user_device_settings
		 WHERE profile_id = ? AND device_id = ? AND key = ?`,
		profileID, deviceID, key,
	).Scan(&entry.ProfileID, &entry.DeviceID, &entry.DeviceName, &entry.DevicePlatform, &entry.Key, &entry.Value, &entry.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting device setting %q for device %q: %w", key, deviceID, err)
	}
	return &entry, nil
}

func RegisterDevice(db *sql.DB, entry userstore.DeviceEntry) error {
	if strings.TrimSpace(entry.ProfileID) == "" || strings.TrimSpace(entry.DeviceID) == "" {
		return nil
	}
	lastSeenAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO user_devices
			(profile_id, device_id, device_name, device_platform, last_seen_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(profile_id, device_id) DO UPDATE SET
			device_name = CASE
				WHEN excluded.device_name <> '' THEN excluded.device_name
				ELSE user_devices.device_name
			END,
			device_platform = CASE
				WHEN excluded.device_platform <> '' THEN excluded.device_platform
				ELSE user_devices.device_platform
			END,
			last_seen_at = excluded.last_seen_at`,
		entry.ProfileID, entry.DeviceID, entry.DeviceName, entry.DevicePlatform, lastSeenAt,
	)
	if err != nil {
		return fmt.Errorf("registering device %q: %w", entry.DeviceID, err)
	}
	return nil
}

func ListDevices(db *sql.DB) ([]userstore.DeviceEntry, error) {
	rows, err := db.Query(
		`SELECT profile_id, device_id, device_name, device_platform, last_seen_at
		 FROM user_devices
		 ORDER BY last_seen_at DESC, profile_id ASC, device_name ASC, device_id ASC`,
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

func SetDeviceSetting(db *sql.DB, entry userstore.DeviceSettingEntry) error {
	if err := RegisterDevice(db, userstore.DeviceEntry{
		ProfileID:      entry.ProfileID,
		DeviceID:       entry.DeviceID,
		DeviceName:     entry.DeviceName,
		DevicePlatform: entry.DevicePlatform,
	}); err != nil {
		return err
	}
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO user_device_settings
			(profile_id, device_id, key, value, device_name, device_platform, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(profile_id, device_id, key) DO UPDATE SET
			value = excluded.value,
			device_name = excluded.device_name,
			device_platform = excluded.device_platform,
			updated_at = excluded.updated_at`,
		entry.ProfileID, entry.DeviceID, entry.Key, entry.Value, entry.DeviceName, entry.DevicePlatform, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("setting device setting %q for device %q: %w", entry.Key, entry.DeviceID, err)
	}
	return nil
}

func DeleteDeviceSetting(db *sql.DB, profileID, deviceID, key string) error {
	_, err := db.Exec("DELETE FROM user_device_settings WHERE profile_id = ? AND device_id = ? AND key = ?", profileID, deviceID, key)
	if err != nil {
		return fmt.Errorf("deleting device setting %q for device %q: %w", key, deviceID, err)
	}
	return nil
}

func DeleteAllDeviceSettings(db *sql.DB, profileID, deviceID string) error {
	_, err := db.Exec("DELETE FROM user_device_settings WHERE profile_id = ? AND device_id = ?", profileID, deviceID)
	if err != nil {
		return fmt.Errorf("deleting all device settings for device %q: %w", deviceID, err)
	}
	return nil
}

func DeleteDeviceSettingsByKey(db *sql.DB, key string) error {
	_, err := db.Exec("DELETE FROM user_device_settings WHERE key = ?", key)
	if err != nil {
		return fmt.Errorf("deleting device settings for key %q: %w", key, err)
	}
	return nil
}

func ListDeviceSettings(db *sql.DB, key string) ([]userstore.DeviceSettingEntry, error) {
	rows, err := db.Query(
		`SELECT profile_id, device_id, device_name, device_platform, key, value, updated_at
		 FROM user_device_settings
		 WHERE key = ?
		 ORDER BY updated_at DESC, profile_id ASC, device_name ASC, device_id ASC`,
		key,
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

func ListAllDeviceSettings(db *sql.DB) ([]userstore.DeviceSettingEntry, error) {
	rows, err := db.Query(
		`SELECT profile_id, device_id, device_name, device_platform, key, value, updated_at
		 FROM user_device_settings
		 ORDER BY updated_at DESC, profile_id ASC, key ASC, device_name ASC, device_id ASC`,
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
