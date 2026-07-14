package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

const (
	PushPlatformApple      = "apple"
	PushPlatformAndroid    = "android"
	PushProviderSiloRelay  = "silo_relay"
	PushModeOff            = "off"
	PushModeInAppOnly      = "in_app_only"
	PushModePrivatePush    = "private_push"
	APNsEnvironmentProd    = "production"
	APNsEnvironmentSandbox = "sandbox"
	ApplePushTopicSilo     = "org.siloserver.silo"
)

var (
	ErrPushDeviceUnavailable = errors.New("push device registration unavailable")
	ErrPushDeviceInvalid     = errors.New("invalid push device registration")
	ErrPushDeviceUnsupported = errors.New("unsupported push device registration")

	apnsTokenHexPattern = regexp.MustCompile(`^[0-9a-f]+$`)
	// The relay validates FCM registration tokens against the same shape.
	fcmTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{64,512}$`)
)

// PushDevice represents one profile-scoped notification endpoint.
type PushDevice struct {
	ID                  string
	UserID              int
	ProfileID           string
	DeviceID            string
	Platform            string
	Provider            string
	APNsEnvironment     string
	APNsTopic           string
	APNsTokenCiphertext string
	APNsTokenHash       string
	FCMTokenCiphertext  string
	FCMTokenHash        string
	ServerDeviceID      string
	PushMode            string
	Enabled             bool
	LastSeenAt          *time.Time
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	LastFailureCode     *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ApplePushRegistrationInput struct {
	DeviceID        string
	APNsToken       string
	APNsEnvironment string
	APNsTopic       string
	PushMode        string
}

type ApplePushDeviceRegistration struct {
	UserID          int
	ProfileID       string
	DeviceID        string
	APNsToken       string
	APNsEnvironment string
	APNsTopic       string
	PushMode        string
}

type FCMPushRegistrationInput struct {
	DeviceID string
	FCMToken string
	PushMode string
}

type FCMPushDeviceRegistration struct {
	UserID    int
	ProfileID string
	DeviceID  string
	FCMToken  string
	PushMode  string
}

type PushDeviceStore interface {
	UpsertApple(ctx context.Context, registration ApplePushDeviceRegistration, cipher *secret.Cipher) (*PushDevice, error)
	UpsertFCM(ctx context.Context, registration FCMPushDeviceRegistration, cipher *secret.Cipher) (*PushDevice, error)
	DeleteByProfileDevice(ctx context.Context, profileID, deviceID string) error
}

type PushDeviceRepository struct {
	pool *pgxpool.Pool
}

func NewPushDeviceRepository(pool *pgxpool.Pool) *PushDeviceRepository {
	return &PushDeviceRepository{pool: pool}
}

type PushDeviceService struct {
	store  PushDeviceStore
	cipher *secret.Cipher
}

func NewPushDeviceService(store PushDeviceStore, cipher *secret.Cipher) *PushDeviceService {
	return &PushDeviceService{store: store, cipher: cipher}
}

func (s *PushDeviceService) Available() bool {
	return s != nil && s.store != nil && s.cipher != nil
}

func (s *PushDeviceService) RegisterApple(ctx context.Context, userID int, profileID string, input ApplePushRegistrationInput) (*PushDevice, error) {
	if !s.Available() {
		return nil, ErrPushDeviceUnavailable
	}
	if userID <= 0 {
		return nil, fmt.Errorf("%w: user_id is required", ErrPushDeviceInvalid)
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, fmt.Errorf("%w: profile_id is required", ErrPushDeviceInvalid)
	}
	registration, err := normalizeApplePushRegistration(input)
	if err != nil {
		return nil, err
	}
	registration.UserID = userID
	registration.ProfileID = profileID
	return s.store.UpsertApple(ctx, registration, s.cipher)
}

func (s *PushDeviceService) RegisterFCM(ctx context.Context, userID int, profileID string, input FCMPushRegistrationInput) (*PushDevice, error) {
	if !s.Available() {
		return nil, ErrPushDeviceUnavailable
	}
	if userID <= 0 {
		return nil, fmt.Errorf("%w: user_id is required", ErrPushDeviceInvalid)
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, fmt.Errorf("%w: profile_id is required", ErrPushDeviceInvalid)
	}
	registration, err := normalizeFCMPushRegistration(input)
	if err != nil {
		return nil, err
	}
	registration.UserID = userID
	registration.ProfileID = profileID
	return s.store.UpsertFCM(ctx, registration, s.cipher)
}

// Unregister removes every registration this device install holds for the
// profile, regardless of platform.
func (s *PushDeviceService) Unregister(ctx context.Context, profileID, deviceID string) error {
	if !s.Available() {
		return ErrPushDeviceUnavailable
	}
	profileID = strings.TrimSpace(profileID)
	deviceID = strings.TrimSpace(deviceID)
	if profileID == "" || deviceID == "" {
		return fmt.Errorf("%w: profile_id and device_id are required", ErrPushDeviceInvalid)
	}
	return s.store.DeleteByProfileDevice(ctx, profileID, deviceID)
}

func normalizeApplePushRegistration(input ApplePushRegistrationInput) (ApplePushDeviceRegistration, error) {
	deviceID := strings.TrimSpace(input.DeviceID)
	if deviceID == "" {
		return ApplePushDeviceRegistration{}, fmt.Errorf("%w: device_id is required", ErrPushDeviceInvalid)
	}
	if len(deviceID) > 128 {
		return ApplePushDeviceRegistration{}, fmt.Errorf("%w: device_id is too long", ErrPushDeviceInvalid)
	}

	token := strings.ToLower(strings.TrimSpace(input.APNsToken))
	if token == "" {
		return ApplePushDeviceRegistration{}, fmt.Errorf("%w: apns_token is required", ErrPushDeviceInvalid)
	}
	if len(token) < 64 || len(token) > 256 || !apnsTokenHexPattern.MatchString(token) {
		return ApplePushDeviceRegistration{}, fmt.Errorf("%w: apns_token must be hex encoded", ErrPushDeviceInvalid)
	}

	environment := strings.ToLower(strings.TrimSpace(input.APNsEnvironment))
	if environment != APNsEnvironmentProd && environment != APNsEnvironmentSandbox {
		return ApplePushDeviceRegistration{}, fmt.Errorf("%w: apns_environment must be production or sandbox", ErrPushDeviceInvalid)
	}

	topic := strings.TrimSpace(input.APNsTopic)
	if topic == "" {
		return ApplePushDeviceRegistration{}, fmt.Errorf("%w: apns_topic is required", ErrPushDeviceInvalid)
	}
	if topic != ApplePushTopicSilo {
		return ApplePushDeviceRegistration{}, fmt.Errorf("%w: apns_topic is not supported", ErrPushDeviceUnsupported)
	}

	pushMode := strings.TrimSpace(input.PushMode)
	if pushMode == "" {
		pushMode = PushModePrivatePush
	}
	switch pushMode {
	case PushModeOff, PushModeInAppOnly, PushModePrivatePush:
	default:
		return ApplePushDeviceRegistration{}, fmt.Errorf("%w: push_mode is not supported", ErrPushDeviceUnsupported)
	}

	return ApplePushDeviceRegistration{
		DeviceID:        deviceID,
		APNsToken:       token,
		APNsEnvironment: environment,
		APNsTopic:       topic,
		PushMode:        pushMode,
	}, nil
}

func normalizeFCMPushRegistration(input FCMPushRegistrationInput) (FCMPushDeviceRegistration, error) {
	deviceID := strings.TrimSpace(input.DeviceID)
	if deviceID == "" {
		return FCMPushDeviceRegistration{}, fmt.Errorf("%w: device_id is required", ErrPushDeviceInvalid)
	}
	if len(deviceID) > 128 {
		return FCMPushDeviceRegistration{}, fmt.Errorf("%w: device_id is too long", ErrPushDeviceInvalid)
	}

	// FCM registration tokens are case-sensitive; only trim surrounding space.
	token := strings.TrimSpace(input.FCMToken)
	if token == "" {
		return FCMPushDeviceRegistration{}, fmt.Errorf("%w: token is required", ErrPushDeviceInvalid)
	}
	if !fcmTokenPattern.MatchString(token) {
		return FCMPushDeviceRegistration{}, fmt.Errorf("%w: token is not a plausible FCM registration token", ErrPushDeviceInvalid)
	}

	pushMode := strings.TrimSpace(input.PushMode)
	if pushMode == "" {
		pushMode = PushModePrivatePush
	}
	switch pushMode {
	case PushModeOff, PushModeInAppOnly, PushModePrivatePush:
	default:
		return FCMPushDeviceRegistration{}, fmt.Errorf("%w: push_mode is not supported", ErrPushDeviceUnsupported)
	}

	return FCMPushDeviceRegistration{
		DeviceID: deviceID,
		FCMToken: token,
		PushMode: pushMode,
	}, nil
}

func apnsTokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(token))))
	return hex.EncodeToString(sum[:])
}

func fcmTokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func pushDeviceAPNsTokenAAD(id string) string {
	return secret.RowAAD("push_devices", "apns_token", id)
}

func pushDeviceFCMTokenAAD(id string) string {
	return secret.RowAAD("push_devices", "fcm_token", id)
}

// Platform-specific token columns are NULL for the other platform's rows;
// COALESCE keeps the scan targets plain strings.
const pushDeviceColumns = `
	id,
	user_id,
	profile_id,
	device_id,
	platform,
	provider,
	COALESCE(apns_environment, ''),
	COALESCE(apns_topic, ''),
	COALESCE(apns_token_ciphertext, ''),
	COALESCE(apns_token_hash, ''),
	COALESCE(fcm_token_ciphertext, ''),
	COALESCE(fcm_token_hash, ''),
	server_device_id,
	push_mode,
	enabled,
	last_seen_at,
	last_success_at,
	last_failure_at,
	last_failure_code,
	created_at,
	updated_at`

func (r *PushDeviceRepository) UpsertApple(ctx context.Context, registration ApplePushDeviceRegistration, cipher *secret.Cipher) (*PushDevice, error) {
	if r == nil || r.pool == nil || cipher == nil {
		return nil, ErrPushDeviceUnavailable
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin push device upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A device install registers for the profile it is currently signed into.
	// Purge the same install's registrations under other profiles (attempts
	// cascade with them) so a profile switch on a shared device doesn't leave
	// the previous profile's notifications flowing to it.
	if _, err := tx.Exec(ctx,
		`DELETE FROM push_devices WHERE device_id = $1 AND platform = $2 AND profile_id <> $3`,
		registration.DeviceID, PushPlatformApple, registration.ProfileID); err != nil {
		return nil, fmt.Errorf("purge reassigned push device: %w", err)
	}

	device, err := r.selectForUpdate(ctx, tx, registration.ProfileID, registration.DeviceID, PushPlatformApple)
	if err != nil {
		return nil, err
	}

	if device == nil {
		device, err = r.insertApple(ctx, tx, registration, cipher)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if device != nil {
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("commit push device insert: %w", err)
			}
			return device, nil
		}

		device, err = r.selectForUpdate(ctx, tx, registration.ProfileID, registration.DeviceID, PushPlatformApple)
		if err != nil {
			return nil, err
		}
		if device == nil {
			return nil, fmt.Errorf("push device upsert conflict row missing")
		}
	}

	device, err = r.updateApple(ctx, tx, registration, cipher, device)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit push device update: %w", err)
	}
	return device, nil
}

func (r *PushDeviceRepository) UpsertFCM(ctx context.Context, registration FCMPushDeviceRegistration, cipher *secret.Cipher) (*PushDevice, error) {
	if r == nil || r.pool == nil || cipher == nil {
		return nil, ErrPushDeviceUnavailable
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin push device upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Same profile-switch cleanup as UpsertApple: one install notifies one
	// profile at a time.
	if _, err := tx.Exec(ctx,
		`DELETE FROM push_devices WHERE device_id = $1 AND platform = $2 AND profile_id <> $3`,
		registration.DeviceID, PushPlatformAndroid, registration.ProfileID); err != nil {
		return nil, fmt.Errorf("purge reassigned push device: %w", err)
	}

	device, err := r.selectForUpdate(ctx, tx, registration.ProfileID, registration.DeviceID, PushPlatformAndroid)
	if err != nil {
		return nil, err
	}

	if device == nil {
		device, err = r.insertFCM(ctx, tx, registration, cipher)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if device != nil {
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("commit push device insert: %w", err)
			}
			return device, nil
		}

		device, err = r.selectForUpdate(ctx, tx, registration.ProfileID, registration.DeviceID, PushPlatformAndroid)
		if err != nil {
			return nil, err
		}
		if device == nil {
			return nil, fmt.Errorf("push device upsert conflict row missing")
		}
	}

	device, err = r.updateFCM(ctx, tx, registration, cipher, device)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit push device update: %w", err)
	}
	return device, nil
}

// DeleteAllForProfile removes push registrations for a deleted profile.
func (r *PushDeviceRepository) DeleteAllForProfile(ctx context.Context, profileID string) error {
	if r == nil || r.pool == nil {
		return nil
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM push_devices WHERE profile_id = $1`, profileID)
	return err
}

// DeleteByProfileDevice removes one install's registrations for a profile
// across every platform (attempts cascade with the device rows).
func (r *PushDeviceRepository) DeleteByProfileDevice(ctx context.Context, profileID, deviceID string) error {
	if r == nil || r.pool == nil {
		return ErrPushDeviceUnavailable
	}
	_, err := r.pool.Exec(ctx,
		`DELETE FROM push_devices WHERE profile_id = $1 AND device_id = $2`, profileID, deviceID)
	return err
}

func (r *PushDeviceRepository) selectForUpdate(ctx context.Context, tx pgx.Tx, profileID, deviceID, platform string) (*PushDevice, error) {
	row := tx.QueryRow(ctx, `SELECT `+pushDeviceColumns+` FROM push_devices WHERE profile_id = $1 AND device_id = $2 AND platform = $3 FOR UPDATE`, profileID, deviceID, platform)
	device, err := scanPushDevice(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select push device: %w", err)
	}
	return device, nil
}

func (r *PushDeviceRepository) insertApple(ctx context.Context, tx pgx.Tx, registration ApplePushDeviceRegistration, cipher *secret.Cipher) (*PushDevice, error) {
	id := ulid.Make().String()
	serverDeviceID := ulid.Make().String()
	ciphertext, err := cipher.Encrypt(registration.APNsToken, pushDeviceAPNsTokenAAD(id))
	if err != nil {
		return nil, fmt.Errorf("encrypt apns token: %w", err)
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO push_devices (
			id,
			user_id,
			profile_id,
			device_id,
			platform,
			provider,
			apns_environment,
			apns_topic,
			apns_token_ciphertext,
			apns_token_hash,
			server_device_id,
			push_mode,
			enabled,
			last_seen_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, true, now())
		ON CONFLICT (profile_id, device_id, platform) DO NOTHING
		RETURNING `+pushDeviceColumns,
		id,
		registration.UserID,
		registration.ProfileID,
		registration.DeviceID,
		PushPlatformApple,
		PushProviderSiloRelay,
		registration.APNsEnvironment,
		registration.APNsTopic,
		ciphertext,
		apnsTokenHash(registration.APNsToken),
		serverDeviceID,
		registration.PushMode,
	)
	device, err := scanPushDevice(row)
	if err != nil {
		return nil, err
	}
	return device, nil
}

func (r *PushDeviceRepository) updateApple(ctx context.Context, tx pgx.Tx, registration ApplePushDeviceRegistration, cipher *secret.Cipher, existing *PushDevice) (*PushDevice, error) {
	ciphertext, err := cipher.Encrypt(registration.APNsToken, pushDeviceAPNsTokenAAD(existing.ID))
	if err != nil {
		return nil, fmt.Errorf("encrypt apns token: %w", err)
	}

	row := tx.QueryRow(ctx, `
		UPDATE push_devices
		SET user_id = $1,
			provider = $2,
			apns_environment = $3,
			apns_topic = $4,
			apns_token_ciphertext = $5,
			apns_token_hash = $6,
			push_mode = $7,
			enabled = true,
			last_seen_at = now(),
			last_failure_at = NULL,
			last_failure_code = NULL,
			updated_at = now()
		WHERE id = $8
		RETURNING `+pushDeviceColumns,
		registration.UserID,
		PushProviderSiloRelay,
		registration.APNsEnvironment,
		registration.APNsTopic,
		ciphertext,
		apnsTokenHash(registration.APNsToken),
		registration.PushMode,
		existing.ID,
	)
	device, err := scanPushDevice(row)
	if err != nil {
		return nil, fmt.Errorf("update push device: %w", err)
	}
	return device, nil
}

func (r *PushDeviceRepository) insertFCM(ctx context.Context, tx pgx.Tx, registration FCMPushDeviceRegistration, cipher *secret.Cipher) (*PushDevice, error) {
	id := ulid.Make().String()
	serverDeviceID := ulid.Make().String()
	ciphertext, err := cipher.Encrypt(registration.FCMToken, pushDeviceFCMTokenAAD(id))
	if err != nil {
		return nil, fmt.Errorf("encrypt fcm token: %w", err)
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO push_devices (
			id,
			user_id,
			profile_id,
			device_id,
			platform,
			provider,
			fcm_token_ciphertext,
			fcm_token_hash,
			server_device_id,
			push_mode,
			enabled,
			last_seen_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, true, now())
		ON CONFLICT (profile_id, device_id, platform) DO NOTHING
		RETURNING `+pushDeviceColumns,
		id,
		registration.UserID,
		registration.ProfileID,
		registration.DeviceID,
		PushPlatformAndroid,
		PushProviderSiloRelay,
		ciphertext,
		fcmTokenHash(registration.FCMToken),
		serverDeviceID,
		registration.PushMode,
	)
	device, err := scanPushDevice(row)
	if err != nil {
		return nil, err
	}
	return device, nil
}

func (r *PushDeviceRepository) updateFCM(ctx context.Context, tx pgx.Tx, registration FCMPushDeviceRegistration, cipher *secret.Cipher, existing *PushDevice) (*PushDevice, error) {
	ciphertext, err := cipher.Encrypt(registration.FCMToken, pushDeviceFCMTokenAAD(existing.ID))
	if err != nil {
		return nil, fmt.Errorf("encrypt fcm token: %w", err)
	}

	row := tx.QueryRow(ctx, `
		UPDATE push_devices
		SET user_id = $1,
			provider = $2,
			fcm_token_ciphertext = $3,
			fcm_token_hash = $4,
			push_mode = $5,
			enabled = true,
			last_seen_at = now(),
			last_failure_at = NULL,
			last_failure_code = NULL,
			updated_at = now()
		WHERE id = $6
		RETURNING `+pushDeviceColumns,
		registration.UserID,
		PushProviderSiloRelay,
		ciphertext,
		fcmTokenHash(registration.FCMToken),
		registration.PushMode,
		existing.ID,
	)
	device, err := scanPushDevice(row)
	if err != nil {
		return nil, fmt.Errorf("update push device: %w", err)
	}
	return device, nil
}

func scanPushDevice(row pgx.Row) (*PushDevice, error) {
	var device PushDevice
	if err := row.Scan(
		&device.ID,
		&device.UserID,
		&device.ProfileID,
		&device.DeviceID,
		&device.Platform,
		&device.Provider,
		&device.APNsEnvironment,
		&device.APNsTopic,
		&device.APNsTokenCiphertext,
		&device.APNsTokenHash,
		&device.FCMTokenCiphertext,
		&device.FCMTokenHash,
		&device.ServerDeviceID,
		&device.PushMode,
		&device.Enabled,
		&device.LastSeenAt,
		&device.LastSuccessAt,
		&device.LastFailureAt,
		&device.LastFailureCode,
		&device.CreatedAt,
		&device.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &device, nil
}
