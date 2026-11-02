package notifications

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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

func (f *fakePushDeviceStore) DeleteByProfileDevice(ctx context.Context, userID int, profileID, deviceID string) error {
	f.deleted = append(f.deleted, fmt.Sprintf("%d/%s/%s", userID, profileID, deviceID))
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
			fcm_token_ciphertext text,
			fcm_token_hash text,
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
	for _, name := range []string{"20260906054410_ordered_android_push_registrations.sql", "20260906184134_ordered_apple_push_registrations.sql"} {
		migration, err := os.ReadFile("../../migrations/sql/" + name)
		if err != nil {
			t.Fatal(err)
		}
		ddl := strings.Split(strings.Split(string(migration), "-- +goose StatementBegin")[1], "-- +goose StatementEnd")[0]
		ddl = strings.ReplaceAll(ddl, "CREATE TABLE public.", "CREATE TEMP TABLE ")
		if _, err = pool.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
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
	if strings.Contains(second.APNsTokenCiphertext, registration.APNsToken) {
		t.Fatal("stored ciphertext exposes the token")
	}
	if _, err := cipher.Decrypt(second.APNsTokenCiphertext, pushDeviceAPNsTokenAAD(second.ID+"-other")); err == nil {
		t.Fatal("stored token decrypted with another row's AAD")
	}
	if apnsTokenHash(strings.ToUpper(registration.APNsToken)) != second.APNsTokenHash {
		t.Fatal("APNs token hash must ignore token case")
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

func TestRetireApplePushStopsDeliveryAndOrdersAgainstRegistrationDB(t *testing.T) {
	repo, pool := newPushDeviceTestRepo(t)
	ctx, cipher, cmd := t.Context(), testPushCipher(t), ApplePushCommand{UserID: 7, ProfileID: "profile", InstallationKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), Generation: 1, ApplePushRegistrationInput: validApplePushInput()}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	unknown := ApplePushRetirement{DeviceID: "never-registered", InstallationKey: cmd.InstallationKey, Generation: 4}
	if got, err := repo.RetireApplePush(ctx, unknown); err != nil || !got.Removed {
		t.Fatalf("unknown=%+v err=%v", got, err)
	}
	if n := count(`SELECT count(*) FROM apple_push_installations WHERE device_id=$1`, unknown.DeviceID); n != 0 {
		t.Fatalf("unknown installation wrote %d rows", n)
	}

	cmd.Generation = 5
	first, err := repo.ApplyApplePush(ctx, cmd, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE TEMP TABLE push_delivery_attempts(id text PRIMARY KEY,push_device_id text REFERENCES push_devices(id) ON DELETE CASCADE)`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO push_delivery_attempts VALUES('pending',$1)`, first.RegistrationID); err != nil {
		t.Fatal(err)
	}
	retire := ApplePushRetirement{DeviceID: cmd.DeviceID, InstallationKey: cmd.InstallationKey, Generation: 5}
	if _, err = repo.RetireApplePush(ctx, retire); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("retirement at the registration's generation=%v", err)
	}
	wrong := retire
	wrong.Generation = 6
	wrong.InstallationKey = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	if _, err = repo.RetireApplePush(ctx, wrong); !errors.Is(err, ErrPushInstallationProof) {
		t.Fatalf("wrong key=%v", err)
	}
	retire.Generation = 4
	if _, err = repo.RetireApplePush(ctx, retire); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("stale=%v", err)
	}
	if n := count(`SELECT count(*) FROM push_devices WHERE enabled`); n != 1 {
		t.Fatalf("refused retirements changed delivery: %d enabled rows", n)
	}

	retire.Generation = 6
	for range 2 { // the second call is an exact replay after a lost response
		if got, e := repo.RetireApplePush(ctx, retire); e != nil || !got.Removed || got.Generation != 6 {
			t.Fatalf("retire=%+v err=%v", got, e)
		}
	}
	if n := count(`SELECT count(*) FROM push_devices`); n != 0 {
		t.Fatalf("retired installation keeps %d rows", n)
	}
	if n := count(`SELECT count(*) FROM push_delivery_attempts`); n != 0 {
		t.Fatalf("retired installation keeps %d pending attempts", n)
	}

	cmd.Generation = 6
	if _, err = repo.ApplyApplePush(ctx, cmd, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("registration at the retirement's generation=%v", err)
	}
	cmd.Generation = 7
	again, err := repo.ApplyApplePush(ctx, cmd, cipher)
	if err != nil || !again.Enabled || again.RegistrationID == first.RegistrationID {
		t.Fatalf("re-register=%+v err=%v", again, err)
	}
	if _, err = repo.RetireApplePush(ctx, retire); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("late retirement=%v", err)
	}
	if current, e := repo.getPushDeviceByID(ctx, again.RegistrationID); e != nil || !current.Enabled {
		t.Fatalf("late retirement touched the new registration: %+v err=%v", current, e)
	}
}

func TestRetireAndroidPushStopsDeliveryAndOrdersAgainstRegistrationDB(t *testing.T) {
	repo, pool := newPushDeviceTestRepo(t)
	ctx, cipher := t.Context(), testPushCipher(t)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	register := AndroidPushCommand{UserID: 7, ProfileID: "default", DeviceID: "phone", InstallationKey: key, Generation: 5, Token: strings.Repeat("a", 64), PushMode: PushModePrivatePush}
	first, err := repo.ApplyAndroidPush(ctx, register, cipher)
	if err != nil {
		t.Fatal(err)
	}
	// A bridge-only installation sits at generation 0 and has no key to check.
	if _, err = registerLegacyMobile(t, repo, PushPlatformAndroid, 8, "default", "bridge-phone", PushModePrivatePush); err != nil {
		t.Fatal(err)
	}
	legacyMobileAttempts(t, pool)
	before := legacyMobileSnapshot(t, pool)
	for _, device := range []string{"never-registered", "bridge-phone"} {
		if got, e := repo.RetireAndroidPush(ctx, AndroidPushRetirement{DeviceID: device, InstallationKey: key, Generation: 4}); e != nil || !got.Removed || got.Generation != 4 {
			t.Fatalf("%s retire=%+v err=%v", device, got, e)
		}
	}
	retire := AndroidPushRetirement{DeviceID: register.DeviceID, InstallationKey: key, Generation: 5}
	if _, err = repo.RetireAndroidPush(ctx, retire); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("retirement at the registration's generation=%v", err)
	}
	wrong := AndroidPushRetirement{DeviceID: register.DeviceID, InstallationKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32))), Generation: 6}
	if _, err = repo.RetireAndroidPush(ctx, wrong); !errors.Is(err, ErrPushInstallationProof) {
		t.Fatalf("wrong key=%v", err)
	}
	retire.Generation = 4
	if _, err = repo.RetireAndroidPush(ctx, retire); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("stale=%v", err)
	}
	if legacyMobileSnapshot(t, pool) != before {
		t.Fatal("unknown, bridge-only, or refused retirements changed rows")
	}

	retire.Generation = 6
	for range 2 { // the second call is an exact replay after a lost response
		if got, e := repo.RetireAndroidPush(ctx, retire); e != nil || !got.Removed || got.Generation != 6 {
			t.Fatalf("retire=%+v err=%v", got, e)
		}
	}
	if n := count(`SELECT count(*) FROM push_devices WHERE device_id=$1`, register.DeviceID); n != 0 {
		t.Fatalf("retired installation keeps %d rows", n)
	}
	if n := count(`SELECT count(*) FROM push_delivery_attempts WHERE push_device_id=$1`, first.RegistrationID); n != 0 {
		t.Fatalf("retired installation keeps %d attempts", n)
	}
	if n := count(`SELECT count(*) FROM push_devices WHERE device_id='bridge-phone' AND enabled`); n != 1 {
		t.Fatalf("retirement touched another installation: %d rows", n)
	}

	// The retirement is the owner's removal at that generation: the owner's own
	// removal replays and a registration there conflicts.
	removal := AndroidPushCommand{UserID: 7, ProfileID: "default", DeviceID: register.DeviceID, InstallationKey: key, Generation: 6, Remove: true}
	if got, e := repo.ApplyAndroidPush(ctx, removal, cipher); e != nil || !got.Removed {
		t.Fatalf("owner removal replay=%+v err=%v", got, e)
	}
	register.Generation = 6
	if _, err = repo.ApplyAndroidPush(ctx, register, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("registration at the retirement's generation=%v", err)
	}
	register.Generation = 7
	again, err := repo.ApplyAndroidPush(ctx, register, cipher)
	if err != nil || again.Removed || again.RegistrationID == first.RegistrationID {
		t.Fatalf("re-register=%+v err=%v", again, err)
	}
	if _, err = repo.RetireAndroidPush(ctx, retire); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("late retirement=%v", err)
	}
	if current, e := repo.getPushDeviceByID(ctx, again.RegistrationID); e != nil || !current.Enabled {
		t.Fatalf("late retirement touched the new registration: %+v err=%v", current, e)
	}
}
