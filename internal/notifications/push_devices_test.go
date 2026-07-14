package notifications

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakePushDeviceStore struct {
	got     ApplePushDeviceRegistration
	gotFCM  FCMPushDeviceRegistration
	calls   int
	device  *PushDevice
	err     error
	deleted []string
}

func (f *fakePushDeviceStore) UpsertApple(ctx context.Context, registration ApplePushDeviceRegistration, cipher *secret.Cipher) (*PushDevice, error) {
	f.calls++
	f.got = registration
	if f.err != nil {
		return nil, f.err
	}
	if f.device != nil {
		return f.device, nil
	}
	return &PushDevice{
		ID:             "device-row",
		ServerDeviceID: "server-device",
		Enabled:        true,
		PushMode:       registration.PushMode,
	}, nil
}

func (f *fakePushDeviceStore) UpsertFCM(ctx context.Context, registration FCMPushDeviceRegistration, cipher *secret.Cipher) (*PushDevice, error) {
	f.calls++
	f.gotFCM = registration
	if f.err != nil {
		return nil, f.err
	}
	if f.device != nil {
		return f.device, nil
	}
	return &PushDevice{
		ID:             "device-row",
		Platform:       PushPlatformAndroid,
		ServerDeviceID: "server-device",
		Enabled:        true,
		PushMode:       registration.PushMode,
	}, nil
}

func (f *fakePushDeviceStore) DeleteByProfileDevice(ctx context.Context, profileID, deviceID string) error {
	f.deleted = append(f.deleted, profileID+"/"+deviceID)
	return f.err
}

func testPushCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return cipher
}

func validApplePushInput() ApplePushRegistrationInput {
	return ApplePushRegistrationInput{
		DeviceID:        "local-device",
		APNsToken:       strings.Repeat("a", 64),
		APNsEnvironment: APNsEnvironmentProd,
		APNsTopic:       ApplePushTopicSilo,
		PushMode:        PushModePrivatePush,
	}
}

func TestPushDeviceServiceRegisterAppleNormalizesAndStores(t *testing.T) {
	store := &fakePushDeviceStore{}
	service := NewPushDeviceService(store, testPushCipher(t))

	input := validApplePushInput()
	input.DeviceID = " local-device "
	input.APNsToken = strings.ToUpper(input.APNsToken)
	input.APNsEnvironment = "Production"
	input.PushMode = ""

	device, err := service.RegisterApple(context.Background(), 42, " profile-1 ", input)
	if err != nil {
		t.Fatalf("register apple: %v", err)
	}
	if device.ServerDeviceID != "server-device" || !device.Enabled || device.PushMode != PushModePrivatePush {
		t.Fatalf("unexpected device response: %+v", device)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
	if store.got.UserID != 42 || store.got.ProfileID != "profile-1" {
		t.Fatalf("unexpected owner scope: %+v", store.got)
	}
	if store.got.DeviceID != "local-device" {
		t.Fatalf("device_id = %q", store.got.DeviceID)
	}
	if store.got.APNsToken != strings.Repeat("a", 64) {
		t.Fatalf("apns token was not canonicalized")
	}
	if store.got.APNsEnvironment != APNsEnvironmentProd {
		t.Fatalf("environment = %q", store.got.APNsEnvironment)
	}
	if store.got.PushMode != PushModePrivatePush {
		t.Fatalf("push mode = %q", store.got.PushMode)
	}
}

func TestPushDeviceServiceRegisterAppleRejectsInvalidOrUnsupported(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ApplePushRegistrationInput)
		wantErr error
	}{
		{
			name:    "missing device id",
			mutate:  func(input *ApplePushRegistrationInput) { input.DeviceID = " " },
			wantErr: ErrPushDeviceInvalid,
		},
		{
			name:    "malformed token",
			mutate:  func(input *ApplePushRegistrationInput) { input.APNsToken = strings.Repeat("z", 64) },
			wantErr: ErrPushDeviceInvalid,
		},
		{
			name:    "short token",
			mutate:  func(input *ApplePushRegistrationInput) { input.APNsToken = "abcd" },
			wantErr: ErrPushDeviceInvalid,
		},
		{
			name:    "invalid environment",
			mutate:  func(input *ApplePushRegistrationInput) { input.APNsEnvironment = "development" },
			wantErr: ErrPushDeviceInvalid,
		},
		{
			name:    "unsupported topic",
			mutate:  func(input *ApplePushRegistrationInput) { input.APNsTopic = "com.example.app" },
			wantErr: ErrPushDeviceUnsupported,
		},
		{
			name:    "unsupported push mode",
			mutate:  func(input *ApplePushRegistrationInput) { input.PushMode = "custom_apns" },
			wantErr: ErrPushDeviceUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validApplePushInput()
			tt.mutate(&input)
			store := &fakePushDeviceStore{}
			service := NewPushDeviceService(store, testPushCipher(t))

			_, err := service.RegisterApple(context.Background(), 42, "profile-1", input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if store.calls != 0 {
				t.Fatalf("store was called for rejected input")
			}
		})
	}
}

func TestPushDeviceServiceRegisterFCMNormalizesAndStores(t *testing.T) {
	store := &fakePushDeviceStore{}
	service := NewPushDeviceService(store, testPushCipher(t))

	token := strings.Repeat("F", 100) + ":APA91b-" + strings.Repeat("x", 40)
	device, err := service.RegisterFCM(context.Background(), 42, " profile-1 ", FCMPushRegistrationInput{
		DeviceID: " local-device ",
		FCMToken: " " + token + " ",
		PushMode: "",
	})
	if err != nil {
		t.Fatalf("register fcm: %v", err)
	}
	if device.ServerDeviceID != "server-device" || !device.Enabled || device.PushMode != PushModePrivatePush {
		t.Fatalf("unexpected device response: %+v", device)
	}
	if store.gotFCM.UserID != 42 || store.gotFCM.ProfileID != "profile-1" || store.gotFCM.DeviceID != "local-device" {
		t.Fatalf("unexpected owner scope: %+v", store.gotFCM)
	}
	if store.gotFCM.FCMToken != token {
		t.Fatalf("fcm token was not trimmed exactly: %q", store.gotFCM.FCMToken)
	}
	if store.gotFCM.PushMode != PushModePrivatePush {
		t.Fatalf("push mode = %q", store.gotFCM.PushMode)
	}
}

func TestPushDeviceServiceRegisterFCMRejectsInvalidOrUnsupported(t *testing.T) {
	validToken := strings.Repeat("F", 140)
	tests := []struct {
		name    string
		input   FCMPushRegistrationInput
		wantErr error
	}{
		{
			name:    "missing device id",
			input:   FCMPushRegistrationInput{DeviceID: " ", FCMToken: validToken},
			wantErr: ErrPushDeviceInvalid,
		},
		{
			name:    "short token",
			input:   FCMPushRegistrationInput{DeviceID: "local-device", FCMToken: "abc"},
			wantErr: ErrPushDeviceInvalid,
		},
		{
			name:    "token with invalid characters",
			input:   FCMPushRegistrationInput{DeviceID: "local-device", FCMToken: strings.Repeat("!", 140)},
			wantErr: ErrPushDeviceInvalid,
		},
		{
			name:    "unsupported push mode",
			input:   FCMPushRegistrationInput{DeviceID: "local-device", FCMToken: validToken, PushMode: "custom_fcm"},
			wantErr: ErrPushDeviceUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakePushDeviceStore{}
			service := NewPushDeviceService(store, testPushCipher(t))
			_, err := service.RegisterFCM(context.Background(), 42, "profile-1", tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if store.calls != 0 {
				t.Fatalf("store was called for rejected input")
			}
		})
	}
}

func TestPushDeviceFCMTokenHashIsCaseSensitive(t *testing.T) {
	token := strings.Repeat("f", 140)
	if fcmTokenHash(token) == fcmTokenHash(strings.ToUpper(token)) {
		t.Fatal("fcm token hash must be case-sensitive")
	}
	if fcmTokenHash(" "+token+" ") != fcmTokenHash(token) {
		t.Fatal("fcm token hash must ignore surrounding whitespace")
	}
}

func TestPushDeviceAPNsTokenEncryptionUsesRowAAD(t *testing.T) {
	cipher := testPushCipher(t)
	token := strings.Repeat("a", 64)
	ciphertext, err := cipher.Encrypt(token, pushDeviceAPNsTokenAAD("row-1"))
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	if ciphertext == token || strings.Contains(ciphertext, token) {
		t.Fatalf("ciphertext exposes token: %q", ciphertext)
	}
	plaintext, err := cipher.Decrypt(ciphertext, pushDeviceAPNsTokenAAD("row-1"))
	if err != nil {
		t.Fatalf("decrypt token: %v", err)
	}
	if plaintext != token {
		t.Fatalf("plaintext = %q, want token", plaintext)
	}
	if _, err := cipher.Decrypt(ciphertext, pushDeviceAPNsTokenAAD("row-2")); err == nil {
		t.Fatalf("decrypt with wrong row AAD succeeded")
	}
	if got, want := apnsTokenHash(strings.ToUpper(token)), apnsTokenHash(token); got != want {
		t.Fatalf("hash should be token-case agnostic: %q != %q", got, want)
	}
}

// newPushDeviceTestRepo connects to SILO_TEST_DATABASE_URL (skipping when
// unset) and shadows push_devices with a session-local temp table, pinning the
// pool to one connection so every query sees it.
func newPushDeviceTestRepo(t *testing.T) (*PushDeviceRepository, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run DB-backed push device repository test")
	}

	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse db config: %v", err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `
		CREATE TEMP TABLE push_devices (
			id text PRIMARY KEY,
			user_id integer NOT NULL,
			profile_id text NOT NULL,
			device_id varchar(128) NOT NULL,
			platform text NOT NULL,
			provider text NOT NULL,
			apns_environment text,
			apns_topic text,
			apns_token_ciphertext text,
			apns_token_hash text,
			server_device_id text NOT NULL,
			push_mode text NOT NULL DEFAULT 'private_push',
			enabled boolean NOT NULL DEFAULT true,
			last_seen_at timestamptz,
			last_success_at timestamptz,
			last_failure_at timestamptz,
			last_failure_code text,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			CONSTRAINT push_devices_profile_device_platform_key UNIQUE (profile_id, device_id, platform),
			CONSTRAINT push_devices_server_device_id_key UNIQUE (server_device_id)
		) ON COMMIT PRESERVE ROWS`); err != nil {
		t.Fatalf("create temp push_devices table: %v", err)
	}
	return NewPushDeviceRepository(pool), pool
}

func TestPushDeviceRepositoryUpsertApplePreservesStableIDs(t *testing.T) {
	ctx := context.Background()
	repo, pool := newPushDeviceTestRepo(t)
	cipher := testPushCipher(t)
	registration := ApplePushDeviceRegistration{
		UserID:          42,
		ProfileID:       "profile-1",
		DeviceID:        "local-device",
		APNsToken:       strings.Repeat("a", 64),
		APNsEnvironment: APNsEnvironmentProd,
		APNsTopic:       ApplePushTopicSilo,
		PushMode:        PushModePrivatePush,
	}

	first, err := repo.UpsertApple(ctx, registration, cipher)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	registration.APNsToken = strings.Repeat("b", 64)
	registration.PushMode = PushModeInAppOnly
	second, err := repo.UpsertApple(ctx, registration, cipher)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	if second.ID != first.ID {
		t.Fatalf("row id changed on token rotation: %q != %q", second.ID, first.ID)
	}
	if second.ServerDeviceID != first.ServerDeviceID {
		t.Fatalf("server device id changed on token rotation: %q != %q", second.ServerDeviceID, first.ServerDeviceID)
	}
	if second.APNsTokenHash != apnsTokenHash(registration.APNsToken) {
		t.Fatalf("token hash = %q, want rotated hash", second.APNsTokenHash)
	}
	if first.APNsTokenHash == second.APNsTokenHash {
		t.Fatalf("token hash did not change after rotation")
	}
	plaintext, err := cipher.Decrypt(second.APNsTokenCiphertext, pushDeviceAPNsTokenAAD(second.ID))
	if err != nil {
		t.Fatalf("decrypt rotated token: %v", err)
	}
	if plaintext != registration.APNsToken {
		t.Fatalf("rotated plaintext = %q", plaintext)
	}
	if !second.Enabled || second.PushMode != PushModeInAppOnly {
		t.Fatalf("upsert did not re-enable/update mode: %+v", second)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_devices`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
}

func TestPushDeviceRepositoryUpsertApplePurgesOtherProfiles(t *testing.T) {
	ctx := context.Background()
	repo, pool := newPushDeviceTestRepo(t)
	cipher := testPushCipher(t)

	registration := ApplePushDeviceRegistration{
		UserID:          42,
		ProfileID:       "profile-parent",
		DeviceID:        "shared-phone",
		APNsToken:       strings.Repeat("a", 64),
		APNsEnvironment: APNsEnvironmentProd,
		APNsTopic:       ApplePushTopicSilo,
		PushMode:        PushModePrivatePush,
	}
	if _, err := repo.UpsertApple(ctx, registration, cipher); err != nil {
		t.Fatalf("register under first profile: %v", err)
	}

	// A different install on another profile must be untouched by the purge.
	other := registration
	other.ProfileID = "profile-parent"
	other.DeviceID = "other-phone"
	if _, err := repo.UpsertApple(ctx, other, cipher); err != nil {
		t.Fatalf("register unrelated device: %v", err)
	}

	// The shared phone switches profiles and re-registers: the old profile's
	// row for that install must be gone, not left enabled.
	registration.ProfileID = "profile-kid"
	device, err := repo.UpsertApple(ctx, registration, cipher)
	if err != nil {
		t.Fatalf("register under second profile: %v", err)
	}
	if device.ProfileID != "profile-kid" {
		t.Fatalf("profile = %q, want profile-kid", device.ProfileID)
	}

	rows, err := pool.Query(ctx, `SELECT profile_id, device_id FROM push_devices ORDER BY profile_id`)
	if err != nil {
		t.Fatalf("list rows: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var profileID, deviceID string
		if err := rows.Scan(&profileID, &deviceID); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		got[profileID] = deviceID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := map[string]string{"profile-kid": "shared-phone", "profile-parent": "other-phone"}
	if len(got) != len(want) || got["profile-kid"] != want["profile-kid"] || got["profile-parent"] != want["profile-parent"] {
		t.Fatalf("rows after reassignment = %v, want %v", got, want)
	}
}
