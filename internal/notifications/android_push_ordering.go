package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
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
	if !validInstallationKey(cmd.InstallationKey) {
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

// androidPushIntent digests what an ordered command asks for, so only an exact
// replay of the newest command matches the stored intent.
func androidPushIntent(cmd AndroidPushCommand) string {
	payload, _ := json.Marshal(struct {
		User                     int
		Profile, TokenHash, Mode string
		Remove                   bool
	}{cmd.UserID, cmd.ProfileID, fcmTokenHash(cmd.Token), cmd.PushMode, cmd.Remove})
	return pushDigest(payload)
}

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
	intent := androidPushIntent(cmd)
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
		if err = checkPushInstallationOwner(ctx, tx, cmd.DeviceID, PushPlatformAndroid, cmd.UserID); err != nil {
			return AndroidPushReceipt{}, err
		}
		if cmd.Remove {
			var foreign bool
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

// AndroidPushRetirement stops delivery to an installation without a login
// session. The installation key is the only authority: an app that removed
// this server or signed out has already cleared its login but still holds the key.
type AndroidPushRetirement struct {
	DeviceID        string
	InstallationKey string
	Generation      int64
}

func validateAndroidPushRetirement(cmd AndroidPushRetirement) error {
	if cmd.Generation <= 0 || cmd.DeviceID == "" || len(cmd.DeviceID) > 128 || cmd.DeviceID != strings.TrimSpace(cmd.DeviceID) {
		return ErrPushDeviceInvalid
	}
	if !validInstallationKey(cmd.InstallationKey) {
		return ErrPushInstallationProof
	}
	return nil
}

// RetireAndroidPush applies an ordered removal at the command's generation on
// behalf of the installation's stored owner, authorized by the key hash instead
// of a login. It deletes the installation's Android rows, whose pending attempts
// cascade, and records the owner's removal intent, so the owner's own removal at
// that generation replays and a registration there conflicts. A later
// registration with a higher generation and the same key resumes through
// ApplyAndroidPush. An installation with no ordered record has no key to check;
// the call then answers removed and writes nothing.
func (r *PushDeviceRepository) RetireAndroidPush(ctx context.Context, cmd AndroidPushRetirement) (AndroidPushReceipt, error) {
	if r == nil || r.pool == nil {
		return AndroidPushReceipt{}, ErrPushDeviceUnavailable
	}
	if err := validateAndroidPushRetirement(cmd); err != nil {
		return AndroidPushReceipt{}, err
	}
	retired := AndroidPushReceipt{Generation: cmd.Generation, Removed: true}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return AndroidPushReceipt{}, err
	}
	// Rolling back also drops the placeholder row the lock inserts for an
	// unknown installation.
	defer func() { _ = tx.Rollback(ctx) }()
	generation, err := lockAndroidPushInstallation(ctx, tx, cmd.DeviceID)
	if err != nil {
		return AndroidPushReceipt{}, err
	}
	if generation == 0 {
		return retired, nil
	}
	var storedKey, profile string
	var user int
	var removed bool
	if err = tx.QueryRow(ctx, `SELECT installation_key_hash,user_id,profile_id,removed FROM android_push_installations WHERE device_id=$1`, cmd.DeviceID).Scan(&storedKey, &user, &profile, &removed); err != nil {
		return AndroidPushReceipt{}, err
	}
	if !hmac.Equal([]byte(storedKey), []byte(pushDigest([]byte(cmd.InstallationKey)))) {
		return AndroidPushReceipt{}, ErrPushInstallationProof
	}
	if cmd.Generation < generation || (cmd.Generation == generation && !removed) {
		return AndroidPushReceipt{}, ErrPushGenerationConflict
	}
	if cmd.Generation == generation {
		return retired, nil
	}
	// Deleting the rows retires queued attempts through their FK. A provider
	// request already dispatched cannot be recalled.
	if _, err = tx.Exec(ctx, `DELETE FROM push_devices WHERE device_id=$1 AND platform=$2`, cmd.DeviceID, PushPlatformAndroid); err != nil {
		return AndroidPushReceipt{}, err
	}
	intent := androidPushIntent(AndroidPushCommand{UserID: user, ProfileID: profile, Remove: true})
	if _, err = tx.Exec(ctx, `UPDATE android_push_installations SET generation=$2,intent_hash=$3,registration_id='',server_device_id='',push_mode='',removed=true WHERE device_id=$1`, cmd.DeviceID, cmd.Generation, intent); err != nil {
		return AndroidPushReceipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AndroidPushReceipt{}, fmt.Errorf("commit Android push retirement: %w", err)
	}
	return retired, nil
}

type androidPushRetiringStore interface {
	RetireAndroidPush(context.Context, AndroidPushRetirement) (AndroidPushReceipt, error)
}

// AndroidPushRetirementAvailable reports whether ordered Android storage can
// accept a session-less retirement.
func (s *PushDeviceService) AndroidPushRetirementAvailable() bool {
	if !s.Available() {
		return false
	}
	_, ok := s.store.(androidPushRetiringStore)
	return ok
}

func (s *PushDeviceService) RetireAndroidPush(ctx context.Context, cmd AndroidPushRetirement) (AndroidPushReceipt, error) {
	if !s.Available() {
		return AndroidPushReceipt{}, ErrPushDeviceUnavailable
	}
	store, ok := s.store.(androidPushRetiringStore)
	if !ok {
		return AndroidPushReceipt{}, ErrPushDeviceUnavailable
	}
	return store.RetireAndroidPush(ctx, cmd)
}
