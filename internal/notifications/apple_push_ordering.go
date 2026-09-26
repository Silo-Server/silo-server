package notifications

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5"
)

type ApplePushCommand struct {
	UserID          int
	ProfileID       string
	InstallationKey string
	Generation      int64
	ApplePushRegistrationInput
}

// ApplePushReceipt describes the last accepted intent and current referenced
// row. Missing/disabled rows never become enabled merely to renew credentials.
type ApplePushReceipt struct {
	Generation     int64
	RegistrationID string
	ServerDeviceID string
	PushMode       string
	Enabled        bool
	Removed        bool
}
type orderedApplePushStore interface {
	ApplyApplePush(context.Context, ApplePushCommand, *secret.Cipher) (ApplePushReceipt, error)
}

func (s *PushDeviceService) ApplyApplePush(ctx context.Context, cmd ApplePushCommand) (ApplePushReceipt, error) {
	if !s.Available() {
		return ApplePushReceipt{}, ErrPushDeviceUnavailable
	}
	store, ok := s.store.(orderedApplePushStore)
	if !ok {
		return ApplePushReceipt{}, ErrPushDeviceUnavailable
	}
	return store.ApplyApplePush(ctx, cmd, s.cipher)
}
func validateApplePushCommand(cmd ApplePushCommand) (ApplePushDeviceRegistration, error) {
	if cmd.UserID <= 0 || strings.TrimSpace(cmd.ProfileID) == "" || cmd.Generation <= 0 || cmd.DeviceID != strings.TrimSpace(cmd.DeviceID) {
		return ApplePushDeviceRegistration{}, ErrPushDeviceInvalid
	}
	key, err := base64.RawURLEncoding.DecodeString(cmd.InstallationKey)
	if err != nil || len(key) != 32 || base64.RawURLEncoding.EncodeToString(key) != cmd.InstallationKey {
		return ApplePushDeviceRegistration{}, ErrPushInstallationProof
	}
	registration, err := normalizeApplePushRegistration(cmd.ApplePushRegistrationInput)
	if err != nil {
		return registration, err
	}
	registration.UserID, registration.ProfileID = cmd.UserID, cmd.ProfileID
	return registration, nil
}
func lockApplePushInstallation(ctx context.Context, tx pgx.Tx, device string) (int64, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO apple_push_installations(device_id) VALUES($1) ON CONFLICT DO NOTHING`, device); err != nil {
		return 0, err
	}
	var generation int64
	err := tx.QueryRow(ctx, `SELECT generation FROM apple_push_installations WHERE device_id=$1 FOR UPDATE`, device).Scan(&generation)
	return generation, err
}

// ApplyApplePush commits each newer APNs intent and row replacement atomically.
// Exact replay preserves provider outcomes and does not resurrect a deleted row.
func (r *PushDeviceRepository) ApplyApplePush(ctx context.Context, cmd ApplePushCommand, cipher *secret.Cipher) (ApplePushReceipt, error) {
	return r.ApplyApplePushAndFinalize(ctx, cmd, cipher, nil)
}

// ApplyApplePushAndFinalize invokes finalize while installation and device locks
// are held. It must not perform external delivery or publish a result before
// this method successfully commits. An error rolls back the registration.
func (r *PushDeviceRepository) ApplyApplePushAndFinalize(ctx context.Context, cmd ApplePushCommand, cipher *secret.Cipher, finalize func(ApplePushReceipt) error) (ApplePushReceipt, error) {
	var result ApplePushReceipt
	if r == nil || r.pool == nil || cipher == nil {
		return result, ErrPushDeviceUnavailable
	}
	registration, err := validateApplePushCommand(cmd)
	if err != nil {
		return result, err
	}
	payload, _ := json.Marshal(struct {
		User                                         int
		Profile, TokenHash, Environment, Topic, Mode string
	}{registration.UserID, registration.ProfileID, apnsTokenHash(registration.APNsToken), registration.APNsEnvironment, registration.APNsTopic, registration.PushMode})
	intent, keyHash := pushDigest(payload), pushDigest([]byte(cmd.InstallationKey))
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	generation, err := lockApplePushInstallation(ctx, tx, registration.DeviceID)
	if err != nil {
		return result, err
	}
	var storedKey, storedIntent string
	err = tx.QueryRow(ctx, `SELECT installation_key_hash,intent_hash,generation,registration_id,server_device_id,push_mode FROM apple_push_installations WHERE device_id=$1`, registration.DeviceID).Scan(&storedKey, &storedIntent, &result.Generation, &result.RegistrationID, &result.ServerDeviceID, &result.PushMode)
	if err != nil {
		return result, err
	}
	if generation > 0 {
		if !hmac.Equal([]byte(storedKey), []byte(keyHash)) {
			return ApplePushReceipt{}, ErrPushInstallationProof
		}
		if cmd.Generation < generation || (cmd.Generation == generation && intent != storedIntent) {
			return ApplePushReceipt{}, ErrPushGenerationConflict
		}
		if cmd.Generation == generation {
			// Read the current row under lock. Provider failure or profile cleanup
			// can disable/delete it; replay must preserve either outcome.
			err = tx.QueryRow(ctx, `SELECT enabled FROM push_devices WHERE id=$1 AND user_id=$2 AND profile_id=$3 AND device_id=$4 AND platform=$5 FOR UPDATE`, result.RegistrationID, cmd.UserID, cmd.ProfileID, cmd.DeviceID, PushPlatformApple).Scan(&result.Enabled)
			if errors.Is(err, pgx.ErrNoRows) {
				result.Removed = true
			} else if err != nil {
				return ApplePushReceipt{}, err
			}
			if finalize != nil {
				if err = finalize(result); err != nil {
					return ApplePushReceipt{}, err
				}
			}
			return result, tx.Commit(ctx)
		}
	} else {
		var foreign bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM push_devices WHERE device_id=$1 AND platform=$2 AND user_id<>$3)`, cmd.DeviceID, PushPlatformApple, cmd.UserID).Scan(&foreign)
		if err != nil {
			return result, err
		}
		if foreign {
			return ApplePushReceipt{}, ErrPushGenerationConflict
		}
	}
	// Fresh row identity also retires pending attempts through their existing FK.
	// An external request already dispatched cannot be recalled by this transaction.
	if _, err = tx.Exec(ctx, `DELETE FROM push_devices WHERE device_id=$1 AND platform=$2`, cmd.DeviceID, PushPlatformApple); err != nil {
		return result, err
	}
	device, err := r.insertApple(ctx, tx, registration, cipher)
	if err != nil {
		return result, err
	}
	result = ApplePushReceipt{Generation: cmd.Generation, RegistrationID: device.ID, ServerDeviceID: device.ServerDeviceID, PushMode: device.PushMode, Enabled: device.Enabled}
	_, err = tx.Exec(ctx, `UPDATE apple_push_installations SET installation_key_hash=$2,generation=$3,user_id=$4,profile_id=$5,intent_hash=$6,registration_id=$7,server_device_id=$8,push_mode=$9 WHERE device_id=$1`, cmd.DeviceID, keyHash, cmd.Generation, cmd.UserID, cmd.ProfileID, intent, result.RegistrationID, result.ServerDeviceID, result.PushMode)
	if err != nil {
		return result, err
	}
	if finalize != nil {
		if err = finalize(result); err != nil {
			return ApplePushReceipt{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

type applePushFinalizingStore interface {
	ApplyApplePushAndFinalize(context.Context, ApplePushCommand, *secret.Cipher, func(ApplePushReceipt) error) (ApplePushReceipt, error)
}

func (s *PushDeviceService) OrderedAppleAvailable() bool {
	if !s.Available() {
		return false
	}
	_, ok := s.store.(applePushFinalizingStore)
	return ok
}
func (s *PushDeviceService) ApplyApplePushAndFinalize(ctx context.Context, cmd ApplePushCommand, finalize func(ApplePushReceipt) error) (ApplePushReceipt, error) {
	if !s.OrderedAppleAvailable() || finalize == nil {
		return ApplePushReceipt{}, ErrPushDeviceUnavailable
	}
	store, ok := s.store.(applePushFinalizingStore)
	if !ok {
		return ApplePushReceipt{}, ErrPushDeviceUnavailable
	}
	return store.ApplyApplePushAndFinalize(ctx, cmd, s.cipher, finalize)
}
