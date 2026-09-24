package jellycompat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	data, err := encodeDeviceProfile(profile)
	if err != nil {
		return err
	}
	deviceID = deviceProfileStorageID(deviceID)
	now := s.now()
	if s.pool == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		prefix := token + "\x00"
		count := 0
		for key, stored := range s.profiles {
			if strings.HasPrefix(key, prefix) {
				if !stored.expiresAt.After(now) {
					delete(s.profiles, key)
				} else {
					count++
				}
			}
		}
		if _, exists := s.profiles[prefix+deviceID]; !exists && count >= maxDeviceProfilesPerToken {
			return deviceProfileQuotaError()
		}
		s.profiles[prefix+deviceID] = storedDeviceProfile{profile: profile, expiresAt: now.Add(s.ttl)}
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", errDeviceProfileStore, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tokenHash := deviceProfileTokenHash(token)
	// Serialize the quota check with registration on every API process. An
	// existing device can still refresh its profile when the token is at capacity.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('jellycompat-device-profiles:' || $1, 0))`, tokenHash); err != nil {
		return fmt.Errorf("%w: %w", errDeviceProfileStore, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM jellycompat_device_profiles WHERE token_hash=$1 AND expires_at<=$2`, tokenHash, now); err != nil {
		return fmt.Errorf("%w: %w", errDeviceProfileStore, err)
	}
	var count int
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(bool_or(device_id=$2),false) FROM jellycompat_device_profiles WHERE token_hash=$1`, tokenHash, deviceID).Scan(&count, &exists); err != nil {
		return fmt.Errorf("%w: %w", errDeviceProfileStore, err)
	}
	if !exists && count >= maxDeviceProfilesPerToken {
		return deviceProfileQuotaError()
	}
	_, err = tx.Exec(ctx, `INSERT INTO jellycompat_device_profiles(token_hash,device_id,profile,expires_at) VALUES($1,$2,$3,$4)
 ON CONFLICT(token_hash,device_id) DO UPDATE SET profile=EXCLUDED.profile,expires_at=EXCLUDED.expires_at`, tokenHash, deviceID, data, now.Add(s.ttl))
	if err != nil {
		return fmt.Errorf("%w: %w", errDeviceProfileStore, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: %w", errDeviceProfileStore, err)
	}
	return nil
}

func (s *DeviceProfileStore) GetForDevice(ctx context.Context, token, deviceID string) (DeviceProfile, bool, error) {
	deviceID = deviceProfileStorageID(deviceID)
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

const deviceProfileExpiryBatchSize = 1000

// DeleteExpired removes at most 1,000 expired registrations per call.
// SKIP LOCKED lets API nodes share the work without contending
// with one another or a client refreshing its registration.
func (s *DeviceProfileStore) DeleteExpired(ctx context.Context) (int64, error) {
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
				if deleted == deviceProfileExpiryBatchSize {
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
 WHERE profile.token_hash = expired.token_hash AND profile.device_id = expired.device_id`, now, deviceProfileExpiryBatchSize)
	if err != nil {
		return 0, fmt.Errorf("expire device profiles: %w", err)
	}
	return tag.RowsAffected(), nil
}
