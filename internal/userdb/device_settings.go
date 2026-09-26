package userdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

var _ userstore.DeviceSettingsStore = (*SQLiteUserStore)(nil)

func (s *SQLiteUserStore) ListDeviceSettingsPage(ctx context.Context, opts userstore.DevicePageOptions) ([]userstore.DeviceSettingsEntry, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	args := []any{opts.ProfileID, opts.ProfileID}
	sqlText := `SELECT d.profile_id,d.device_id,d.device_name,d.device_platform,d.last_seen_at,COALESCE(p.name,''),COALESCE((SELECT json_group_array(k.key) FROM user_setting_values k WHERE k.scope='profile_device' AND k.profile_id=d.profile_id AND k.device_id=d.device_id), '[]')
 FROM user_devices d LEFT JOIN profiles p ON p.id=d.profile_id
 WHERE (?='' OR d.profile_id=?)`
	if opts.After != nil {
		sqlText += ` AND (d.last_seen_at < ? OR (d.last_seen_at = ? AND (d.profile_id,d.device_id) > (?,?)))`
		args = append(args, opts.After.LastSeenAt, opts.After.LastSeenAt, opts.After.ProfileID, opts.After.DeviceID)
	}
	sqlText += " ORDER BY d.last_seen_at DESC,d.profile_id,d.device_id LIMIT " + "?"
	args = append(args, opts.Limit)
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := []userstore.DeviceSettingsEntry{}
	for rows.Next() {
		var entry userstore.DeviceSettingsEntry
		var raw []byte
		if err := rows.Scan(&entry.ProfileID, &entry.DeviceID, &entry.DeviceName, &entry.DevicePlatform, &entry.LastSeenAt, &entry.ProfileName, &raw); err != nil {
			return nil, err
		}
		var keys []string
		if err := json.Unmarshal(raw, &keys); err != nil {
			return nil, err
		}
		logical := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			logical[settingscontract.LogicalKey(key)] = struct{}{}
		}
		entry.ChangedCount = len(logical)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *SQLiteUserStore) RemoveDeviceSettings(ctx context.Context, profileID, deviceID string, forget bool) ([]string, error) {
	keys := []string{}
	err := s.withImmediateSettingsTransaction(ctx, func(tx preferenceSettingsExecutor) error {
		var owned bool
		err := tx.QueryRowContext(ctx, `SELECT true FROM user_devices WHERE profile_id=? AND device_id=?`, profileID, deviceID).Scan(&owned)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rows, err := tx.QueryContext(ctx, `DELETE FROM user_setting_values WHERE profile_id=? AND device_id=? AND scope='profile_device' RETURNING key`, profileID, deviceID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				_ = rows.Close()
				return err
			}
			keys = append(keys, key)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !owned && len(keys) == 0 {
			return userstore.ErrDeviceNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_device_settings WHERE profile_id=? AND device_id=?`, profileID, deviceID); err != nil {
			return err
		}
		if forget {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user_devices WHERE profile_id=? AND device_id=?`, profileID, deviceID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}
