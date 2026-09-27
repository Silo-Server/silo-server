package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5"
)

var (
	ErrPushGenerationConflict = errors.New("push registration generation conflicts with current installation")
	ErrPushInstallationProof  = errors.New("push installation proof is invalid")
	ErrPushLegacyWriter       = errors.New("this installation requires ordered push registration")
)

type AndroidPushCommand struct {
	UserID          int
	ProfileID       string
	DeviceID        string
	InstallationKey string
	Generation      int64
	Token           string
	PushMode        string
	Remove          bool
}
type AndroidPushReceipt struct {
	Generation     int64
	RegistrationID string
	ServerDeviceID string
	PushMode       string
	Removed        bool
}
type orderedAndroidPushStore interface {
	ApplyAndroidPush(context.Context, AndroidPushCommand, *secret.Cipher) (AndroidPushReceipt, error)
}

func (s *PushDeviceService) ApplyAndroidPush(ctx context.Context, cmd AndroidPushCommand) (AndroidPushReceipt, error) {
	if !s.Available() {
		return AndroidPushReceipt{}, ErrPushDeviceUnavailable
	}
	store, ok := s.store.(orderedAndroidPushStore)
	if !ok {
		return AndroidPushReceipt{}, ErrPushDeviceUnavailable
	}
	return store.ApplyAndroidPush(ctx, cmd, s.cipher)
}

func validateAndroidPushCommand(cmd AndroidPushCommand) (AndroidPushCommand, error) {
	if cmd.UserID <= 0 || strings.TrimSpace(cmd.ProfileID) == "" || cmd.DeviceID == "" || len(cmd.DeviceID) > 128 || cmd.DeviceID != strings.TrimSpace(cmd.DeviceID) || cmd.Generation <= 0 {
		return cmd, ErrPushDeviceInvalid
	}
	key, err := base64.RawURLEncoding.DecodeString(cmd.InstallationKey)
	if err != nil || len(key) != 32 || base64.RawURLEncoding.EncodeToString(key) != cmd.InstallationKey {
		return cmd, ErrPushInstallationProof
	}
	if cmd.Remove {
		if cmd.Token != "" || cmd.PushMode != "" {
			return cmd, ErrPushDeviceInvalid
		}
	} else {
		reg, err := normalizeFCMPushRegistration(FCMPushRegistrationInput{DeviceID: cmd.DeviceID, FCMToken: cmd.Token, PushMode: cmd.PushMode})
		if err != nil {
			return cmd, err
		}
		cmd.Token, cmd.PushMode = reg.FCMToken, reg.PushMode
	}
	return cmd, nil
}
func pushDigest(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

// lockAndroidPushInstallation serializes bridge and ordered writers across all
// accounts/profiles using this installation. Generation zero is bridge-only.
func lockAndroidPushInstallation(ctx context.Context, tx pgx.Tx, device string) (int64, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO android_push_installations(device_id) VALUES($1) ON CONFLICT DO NOTHING`, device); err != nil {
		return 0, err
	}
	var generation int64
	err := tx.QueryRow(ctx, `SELECT generation FROM android_push_installations WHERE device_id=$1 FOR UPDATE`, device).Scan(&generation)
	return generation, err
}

// ApplyAndroidPush commits the installation command and device change together.
// Only the latest exact intent can replay; a tombstone never recreates a device.
func (r *PushDeviceRepository) ApplyAndroidPush(ctx context.Context, cmd AndroidPushCommand, cipher *secret.Cipher) (AndroidPushReceipt, error) {
	var result AndroidPushReceipt
	if r == nil || r.pool == nil || cipher == nil {
		return result, ErrPushDeviceUnavailable
	}
	cmd, err := validateAndroidPushCommand(cmd)
	if err != nil {
		return result, err
	}
	payload, _ := json.Marshal(struct {
		User                     int
		Profile, TokenHash, Mode string
		Remove                   bool
	}{cmd.UserID, cmd.ProfileID, fcmTokenHash(cmd.Token), cmd.PushMode, cmd.Remove})
	intent := pushDigest(payload)
	keyHash := pushDigest([]byte(cmd.InstallationKey))
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	generation, err := lockAndroidPushInstallation(ctx, tx, cmd.DeviceID)
	if err != nil {
		return result, err
	}
	var storedKey, storedIntent, profile string
	var user int
	err = tx.QueryRow(ctx, `SELECT installation_key_hash,intent_hash,user_id,profile_id,generation,registration_id,server_device_id,push_mode,removed FROM android_push_installations WHERE device_id=$1`, cmd.DeviceID).Scan(&storedKey, &storedIntent, &user, &profile, &result.Generation, &result.RegistrationID, &result.ServerDeviceID, &result.PushMode, &result.Removed)
	if err != nil {
		return result, err
	}
	if generation > 0 {
		if !hmac.Equal([]byte(storedKey), []byte(keyHash)) {
			return AndroidPushReceipt{}, ErrPushInstallationProof
		}
		if cmd.Generation < generation || (cmd.Generation == generation && intent != storedIntent) {
			return AndroidPushReceipt{}, ErrPushGenerationConflict
		}
		if cmd.Generation == generation {
			return result, tx.Commit(ctx)
		}
		if cmd.Remove && (user != cmd.UserID || profile != cmd.ProfileID) {
			return AndroidPushReceipt{}, ErrPushGenerationConflict
		}
	} else {
		// Bootstrap cannot take over an existing legacy registration owned by a
		// different account. Bind while its existing account is still selected.
		var foreign bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM push_devices WHERE device_id=$1 AND platform=$2 AND user_id<>$3)`, cmd.DeviceID, PushPlatformAndroid, cmd.UserID).Scan(&foreign)
		if err != nil {
			return result, err
		}
		if foreign {
			return AndroidPushReceipt{}, ErrPushGenerationConflict
		}
		if cmd.Remove {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM push_devices WHERE device_id=$1 AND platform=$2 AND profile_id<>$3)`, cmd.DeviceID, PushPlatformAndroid, cmd.ProfileID).Scan(&foreign)
			if err != nil {
				return result, err
			}
			if foreign {
				return AndroidPushReceipt{}, ErrPushGenerationConflict
			}
		}
	}
	// New row identity fences outcomes from an already in-flight provider call.
	// Cascading deletion retires old pending/retry attempts; it cannot unsend an
	// external request that already left the server.
	if _, err = tx.Exec(ctx, `DELETE FROM push_devices WHERE device_id=$1 AND platform=$2`, cmd.DeviceID, PushPlatformAndroid); err != nil {
		return result, err
	}
	result = AndroidPushReceipt{Generation: cmd.Generation, Removed: cmd.Remove}
	if !cmd.Remove {
		device, err := r.insertFCM(ctx, tx, FCMPushDeviceRegistration{UserID: cmd.UserID, ProfileID: cmd.ProfileID, DeviceID: cmd.DeviceID, FCMToken: cmd.Token, PushMode: cmd.PushMode}, cipher)
		if err != nil {
			return result, err
		}
		result.RegistrationID, result.ServerDeviceID, result.PushMode = device.ID, device.ServerDeviceID, device.PushMode
	}
	_, err = tx.Exec(ctx, `UPDATE android_push_installations SET installation_key_hash=$2,generation=$3,user_id=$4,profile_id=$5,intent_hash=$6,registration_id=$7,server_device_id=$8,push_mode=$9,removed=$10 WHERE device_id=$1`, cmd.DeviceID, keyHash, cmd.Generation, cmd.UserID, cmd.ProfileID, intent, result.RegistrationID, result.ServerDeviceID, result.PushMode, result.Removed)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit ordered push registration: %w", err)
	}
	return result, nil
}

func (s *PushDeviceService) OrderedAndroidAvailable() bool {
	if !s.Available() {
		return false
	}
	_, ok := s.store.(orderedAndroidPushStore)
	return ok
}
