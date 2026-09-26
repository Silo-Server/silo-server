package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

var _ userstore.DeviceSettingsStore = (*PostgresUserStore)(nil)

func (s *PostgresUserStore) ListDeviceSettingsPage(ctx context.Context, opts userstore.DevicePageOptions) ([]userstore.DeviceSettingsEntry, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	args := []any{s.userID, opts.ProfileID}
	sqlText := `SELECT d.profile_id,d.device_id,d.device_name,d.device_platform,d.last_seen_at,COALESCE(p.name,''),COALESCE((SELECT json_agg(k.key) FROM user_setting_values k WHERE k.user_id=d.user_id AND k.scope='profile_device' AND k.profile_id=d.profile_id AND k.device_id=d.device_id), '[]')
 FROM user_devices d LEFT JOIN user_profiles p ON p.user_id=d.user_id AND p.id=d.profile_id
 WHERE d.user_id = $1 AND ($2='' OR d.profile_id=$2)`
	if opts.After != nil {
		sqlText += ` AND (d.last_seen_at < $3::timestamptz OR (d.last_seen_at = $3::timestamptz AND (d.profile_id,d.device_id) > ($4,$5)))`
		args = append(args, opts.After.LastSeenAt, opts.After.ProfileID, opts.After.DeviceID)
	}
	sqlText += " ORDER BY d.last_seen_at DESC,d.profile_id,d.device_id LIMIT " + fmt.Sprintf("$%d", len(args)+1)
	args = append(args, opts.Limit)
	rows, err := s.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []userstore.DeviceSettingsEntry{}
	for rows.Next() {
		var entry userstore.DeviceSettingsEntry
		var raw []byte
		var lastSeen time.Time
		if err := rows.Scan(&entry.ProfileID, &entry.DeviceID, &entry.DeviceName, &entry.DevicePlatform, &lastSeen, &entry.ProfileName, &raw); err != nil {
			return nil, err
		}
		entry.LastSeenAt = lastSeen.UTC().Format(time.RFC3339Nano)
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

func (s *PostgresUserStore) RemoveDeviceSettings(ctx context.Context, profileID, deviceID string, forget bool) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Reset participates in the same account lock as preference read/merge/write.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1,$2)", preferenceSettingsAdvisoryClass, int32(s.userID)); err != nil {
		return nil, err
	}
	var owned bool
	err = tx.QueryRow(ctx, `SELECT true FROM user_devices WHERE user_id=$1 AND profile_id=$2 AND device_id=$3`, s.userID, profileID, deviceID).Scan(&owned)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	rows, err := tx.Query(ctx, `DELETE FROM user_setting_values WHERE user_id=$1 AND profile_id=$2 AND device_id=$3 AND scope='profile_device' RETURNING key`, s.userID, profileID, deviceID)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !owned && len(keys) == 0 {
		return nil, userstore.ErrDeviceNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_device_settings WHERE user_id=$1 AND profile_id=$2 AND device_id=$3`, s.userID, profileID, deviceID); err != nil {
		return nil, err
	}
	if forget {
		if _, err := tx.Exec(ctx, `DELETE FROM user_devices WHERE user_id=$1 AND profile_id=$2 AND device_id=$3`, s.userID, profileID, deviceID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return keys, nil
}
