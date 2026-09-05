package jellycompat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errDeviceProfileStore = errors.New("device profile store unavailable")

// WithDB makes registration authoritative across API processes. Negotiation
// never falls back to a permissive cached profile on a database failure.
func (s *DeviceProfileStore) WithDB(pool *pgxpool.Pool) *DeviceProfileStore { s.pool = pool; return s }

func deviceProfileTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *DeviceProfileStore) PutForDevice(ctx context.Context, token, deviceID string, profile DeviceProfile) error {
	if s.pool == nil {
		s.Put(token+"\x00"+deviceID, profile)
		return nil
	}
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO jellycompat_device_profiles(token_hash,device_id,profile,expires_at) VALUES($1,$2,$3,$4)
 ON CONFLICT(token_hash,device_id) DO UPDATE SET profile=EXCLUDED.profile,expires_at=EXCLUDED.expires_at`, deviceProfileTokenHash(token), deviceID, data, s.now().Add(s.ttl))
	if err != nil {
		return fmt.Errorf("%w: %w", errDeviceProfileStore, err)
	}
	return nil
}

func (s *DeviceProfileStore) GetForDevice(ctx context.Context, token, deviceID string) (DeviceProfile, bool, error) {
	if s.pool == nil {
		profile, ok := s.Get(token + "\x00" + deviceID)
		// Old callers had no device identity. Only equally anonymous requests can
		// reuse those registrations; an API key shared by devices never does.
		if !ok && deviceID == "" {
			profile, ok = s.Get(token)
		}
		return profile, ok, nil
	}
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT profile FROM jellycompat_device_profiles WHERE token_hash=$1 AND device_id=$2 AND expires_at>$3`, deviceProfileTokenHash(token), deviceID, s.now()).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeviceProfile{}, false, nil
	}
	if err != nil {
		return DeviceProfile{}, false, fmt.Errorf("%w: %w", errDeviceProfileStore, err)
	}
	var profile DeviceProfile
	if err = json.Unmarshal(data, &profile); err != nil {
		return profile, false, fmt.Errorf("%w: %w", errDeviceProfileStore, err)
	}
	return profile, true, nil
}

// DeleteExpired removes at most 1,000 expired registrations per hourly compat
// cleanup tick. SKIP LOCKED lets API nodes share the work without contending
// with one another or a client refreshing its registration.
func (s *DeviceProfileStore) DeleteExpired(ctx context.Context) (int64, error) {
	const batchSize = 1000
	now := s.now()
	if s.pool == nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		var deleted int64
		for key, profile := range s.profiles {
			if !profile.expiresAt.After(now) {
				delete(s.profiles, key)
				deleted++
				if deleted == batchSize {
					break
				}
			}
		}
		return deleted, nil
	}
	tag, err := s.pool.Exec(ctx, `WITH expired AS (
 SELECT token_hash, device_id FROM jellycompat_device_profiles
 WHERE expires_at <= $1 ORDER BY expires_at
 LIMIT $2 FOR UPDATE SKIP LOCKED
 ) DELETE FROM jellycompat_device_profiles AS profile USING expired
 WHERE profile.token_hash = expired.token_hash AND profile.device_id = expired.device_id`, now, batchSize)
	if err != nil {
		return 0, fmt.Errorf("expire device profiles: %w", err)
	}
	return tag.RowsAffected(), nil
}
