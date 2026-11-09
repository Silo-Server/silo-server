package notifications

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5"
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
			login_session_id text,
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
	if current, _, e := repo.getPushDeviceByID(ctx, again.RegistrationID); e != nil || !current.Enabled {
		t.Fatalf("late retirement touched the new registration: %+v err=%v", current, e)
	}
}

// A reinstalled iPhone keeps its device ID but loses its installation key. The
// owning account may reset the installation and register from scratch; any
// other account is refused and learns nothing beyond the generic refusal.
func TestApplePushInstallationResetDB(t *testing.T) {
	repo, pool := newPushDeviceTestRepo(t)
	ctx, cipher := t.Context(), testPushCipher(t)
	key := func(c string) string { return base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(c, 32))) }
	owner := ApplePushCommand{UserID: 7, ProfileID: "profile", InstallationKey: key("1"), Generation: 5, ApplePushRegistrationInput: validApplePushInput()}
	first, err := repo.ApplyApplePushAndFinalize(ctx, owner, cipher, nil)
	if err != nil {
		t.Fatal(err)
	}
	reinstalled := owner
	reinstalled.InstallationKey, reinstalled.Generation, reinstalled.APNsToken = key("2"), 1, strings.Repeat("b", 64)
	foreign := reinstalled
	foreign.UserID, foreign.ProfileID, foreign.InstallationKey = 8, "other", key("3")
	if _, err = repo.ApplyApplePushAndFinalize(ctx, reinstalled, cipher, nil); !errors.Is(err, ErrPushInstallationKeyMismatch) {
		t.Fatalf("owner with a new key=%v", err)
	}
	if _, err = repo.ApplyApplePushAndFinalize(ctx, foreign, cipher, nil); !errors.Is(err, ErrPushInstallationProof) || errors.Is(err, ErrPushInstallationKeyMismatch) {
		t.Fatalf("another account with a new key=%v", err)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	reset := func(user int, k string) (bool, error) {
		return repo.ResetApplePushInstallation(ctx, ApplePushReset{UserID: user, DeviceID: owner.DeviceID, InstallationKey: k})
	}

	if _, err = reset(foreign.UserID, foreign.InstallationKey); !errors.Is(err, ErrPushInstallationProof) {
		t.Fatalf("another account's reset=%v", err)
	}
	if current, _, e := repo.getPushDeviceByID(ctx, first.RegistrationID); e != nil || !current.Enabled {
		t.Fatalf("refused reset touched the registration: %+v err=%v", current, e)
	}
	if done, e := reset(owner.UserID, reinstalled.InstallationKey); e != nil || !done {
		t.Fatalf("owner reset=%v err=%v", done, e)
	}
	if n := count(`SELECT count(*) FROM push_devices`); n != 0 {
		t.Fatalf("reset installation keeps %d rows", n)
	}
	// Until the owner registers again, the cleared installation stays reserved
	// for its account against the ordered and the legacy writer alike.
	if _, err = reset(foreign.UserID, foreign.InstallationKey); !errors.Is(err, ErrPushInstallationProof) {
		t.Fatalf("another account resetting a reserved installation=%v", err)
	}
	if done, e := reset(owner.UserID, reinstalled.InstallationKey); e != nil || done {
		t.Fatalf("owner retrying a cleared installation reset=%v err=%v", done, e)
	}
	if _, err = repo.ApplyApplePushAndFinalize(ctx, foreign, cipher, nil); !errors.Is(err, ErrPushInstallationProof) || errors.Is(err, ErrPushInstallationKeyMismatch) {
		t.Fatalf("another account binding a reset installation=%v", err)
	}
	legacy := ApplePushDeviceRegistration{UserID: foreign.UserID, ProfileID: foreign.ProfileID, DeviceID: foreign.DeviceID, APNsToken: foreign.APNsToken, APNsEnvironment: foreign.APNsEnvironment, APNsTopic: foreign.APNsTopic, PushMode: foreign.PushMode}
	if _, err = repo.UpsertApple(ctx, legacy, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("legacy writer of another account on a reset installation=%v", err)
	}
	again, err := repo.ApplyApplePushAndFinalize(ctx, reinstalled, cipher, nil)
	if err != nil || !again.Enabled || again.Generation != 1 {
		t.Fatalf("register after reset=%+v err=%v", again, err)
	}
	// A reset retried after its response was lost finds the caller's own key and
	// leaves the new registration alone.
	if done, e := reset(owner.UserID, reinstalled.InstallationKey); e != nil || done {
		t.Fatalf("replayed reset=%v err=%v", done, e)
	}
	if current, _, e := repo.getPushDeviceByID(ctx, again.RegistrationID); e != nil || !current.Enabled {
		t.Fatalf("replayed reset touched the new registration: %+v err=%v", current, e)
	}
	if _, err = repo.ApplyApplePushAndFinalize(ctx, foreign, cipher, nil); !errors.Is(err, ErrPushInstallationProof) || errors.Is(err, ErrPushInstallationKeyMismatch) {
		t.Fatalf("another account after reset=%v", err)
	}
	if done, e := repo.ResetApplePushInstallation(ctx, ApplePushReset{UserID: owner.UserID, DeviceID: "never-registered", InstallationKey: key("2")}); e != nil || done {
		t.Fatalf("unknown installation reset=%v err=%v", done, e)
	}
	if n := count(`SELECT count(*) FROM apple_push_installations WHERE device_id='never-registered'`); n != 0 {
		t.Fatalf("unknown installation reset wrote %d rows", n)
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
	if current, _, e := repo.getPushDeviceByID(ctx, again.RegistrationID); e != nil || !current.Enabled {
		t.Fatalf("late retirement touched the new registration: %+v err=%v", current, e)
	}
}

// loginSessionPushDB returns a repository on a schema with its own copies of
// the push and login session tables, holding one session in each state a
// registration can find its login in.
func loginSessionPushDB(t *testing.T) (*PushDeviceRepository, *pgxpool.Pool) {
	t.Helper()
	repo, pool := noLoginsPushDB(t)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO auth_sessions (id, user_id, expires_at, revoked_at) VALUES
			('live', 7, now() + interval '1 hour', NULL),
			('revoked', 7, now() + interval '1 hour', now()),
			('expired', 7, now() - interval '1 hour', NULL)`); err != nil {
		t.Fatal(err)
	}
	return repo, pool
}

// noLoginsPushDB returns a repository on a schema with its own empty copies of
// the push, login session and API key tables.
func noLoginsPushDB(t *testing.T) (*PushDeviceRepository, *pgxpool.Pool) {
	t.Helper()
	pool := schemaDB(t, "push_login_session_")
	if _, err := pool.Exec(t.Context(), `
		CREATE TABLE auth_sessions (LIKE public.auth_sessions INCLUDING ALL);
		CREATE TABLE api_keys (LIKE public.api_keys INCLUDING ALL);
		CREATE TABLE push_devices (LIKE public.push_devices INCLUDING ALL);
		CREATE TABLE push_delivery_attempts (LIKE public.push_delivery_attempts INCLUDING ALL);
		ALTER TABLE push_delivery_attempts ADD FOREIGN KEY (push_device_id) REFERENCES push_devices(id) ON DELETE CASCADE;
		CREATE TABLE apple_push_installations (LIKE public.apple_push_installations INCLUDING ALL);
		CREATE TABLE android_push_installations (LIKE public.android_push_installations INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	return NewPushDeviceRepository(pool), pool
}

// sessionDevice is one ordered registration made by registerForSession.
type sessionDevice struct{ id, platform, serverDeviceID string }

// registerForSession registers one ordered device per platform, named after
// the login session that registers it: Apple first, then Android.
func registerForSession(t *testing.T, repo *PushDeviceRepository, session string) (apple, android sessionDevice) {
	t.Helper()
	ctx, cipher := t.Context(), testPushCipher(t)
	key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	input := validApplePushInput()
	input.DeviceID, input.LoginSessionID = "apple-"+session, session
	appleReceipt, err := repo.ApplyApplePush(ctx, ApplePushCommand{UserID: 7, ProfileID: "default", InstallationKey: key, Generation: 1, ApplePushRegistrationInput: input}, cipher)
	if err != nil {
		t.Fatal(err)
	}
	androidReceipt, err := repo.ApplyAndroidPush(ctx, AndroidPushCommand{UserID: 7, ProfileID: "default", DeviceID: "android-" + session, InstallationKey: key, Generation: 1,
		Token: strings.Repeat("a", 64), PushMode: PushModePrivatePush, LoginSessionID: session}, cipher)
	if err != nil {
		t.Fatal(err)
	}
	return sessionDevice{input.DeviceID, PushPlatformApple, appleReceipt.ServerDeviceID},
		sessionDevice{"android-" + session, PushPlatformAndroid, androidReceipt.ServerDeviceID}
}

// Push follows the login that registered the device, whichever way that login
// ended: fan-out and test sends refuse the device and retention removes its
// token. A registration with no known session keeps delivering, and a session a
// restored backup no longer holds counts as ended.
func TestPushStopsWhenTheRegisteringLoginEndsDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	ctx := t.Context()
	wantDelivered := map[string]bool{"live": true, "": true, "revoked": false, "expired": false, "deleted": false}
	devices := map[sessionDevice]string{}
	for session := range wantDelivered {
		apple, android := registerForSession(t, repo, session)
		devices[apple], devices[android] = session, session
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := repo.ListEnabledPushByProfiles(ctx, tx, []profileKey{{7, "default"}}, []string{PushPlatformApple, PushPlatformAndroid})
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	picked := map[string]bool{}
	for _, device := range listed[profileKey{7, "default"}] {
		picked[device.DeviceID] = true
	}
	for device, session := range devices {
		want := wantDelivered[session]
		if picked[device.id] != want {
			t.Errorf("fan-out picked %s (session %q) = %v, want %v", device.id, session, picked[device.id], want)
		}
		_, _, err := repo.EnqueueTestAttempt(ctx, device.platform, "default", device.serverDeviceID)
		if delivered := !errors.Is(err, ErrPushDeliveryNotFound); delivered != want {
			t.Errorf("test send to %s (session %q): err=%v, want delivered=%v", device.id, session, err, want)
		}
	}

	if _, err := repo.DeleteEndedLoginRegistrations(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT device_id FROM push_devices`)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for device, session := range devices {
		if got := slices.Contains(kept, device.id); got != wantDelivered[session] {
			t.Errorf("retention kept %s (session %q) = %v, want %v", device.id, session, got, wantDelivered[session])
		}
	}
}

// A push queued while the login was live must not leave once it has ended: the
// retry schedule can hold an attempt for half an hour after the revoke.
func TestQueuedPushStopsWhenTheRegisteringLoginEndsDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	ctx := t.Context()
	var relayed atomic.Int32
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		relayed.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"r","status":"accepted"}`))
	}))
	t.Cleanup(relay.Close)
	sender := newRelayTestSender(relay)
	sender.devices, sender.cipher = repo, testPushCipher(t)
	sender.settings = NewSettings(mapSettingReader{
		SettingPushRelayURL: relay.URL, SettingPushRelayAPIKey: "relay-key", SettingAndroidPushDeliveryEnabled: "true",
	})

	_, device := registerForSession(t, repo, "live")
	queue := func() PushDeliveryAttempt {
		t.Helper()
		attempt, _, err := repo.EnqueueTestAttempt(ctx, device.platform, "default", device.serverDeviceID)
		if err != nil {
			t.Fatal(err)
		}
		return *attempt
	}
	queued := queue()

	if got := sender.processAttempt(ctx, queue()); got == nil || got.Outcome != PushOutcomeDelivered || relayed.Load() != 1 {
		t.Fatalf("attempt with a live login = %+v after %d relay calls, want delivered after 1", got, relayed.Load())
	}

	if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = now() WHERE id = 'live'`); err != nil {
		t.Fatal(err)
	}
	got := sender.processAttempt(ctx, queued)
	if got == nil || got.Outcome != PushOutcomeFailed || got.NextRetryAt != nil || got.UpstreamReason == nil || *got.UpstreamReason != "login_session_ended" {
		t.Fatalf("attempt after the revoke = %+v, want failed for good", got)
	}
	if relayed.Load() != 1 {
		t.Fatalf("relay calls after the revoke = %d, want 1", relayed.Load())
	}
}

// An Apple registration from before the column learns its login when the app
// renews its display token, which replays the accepted generation.
func TestAppleRenewalRecordsTheLoginOfAnOlderRegistrationDB(t *testing.T) {
	repo, pool := loginSessionPushDB(t)
	ctx, cipher := t.Context(), testPushCipher(t)
	key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	cmd := ApplePushCommand{UserID: 7, ProfileID: "default", InstallationKey: key, Generation: 1, ApplePushRegistrationInput: validApplePushInput()}
	if _, err := repo.ApplyApplePush(ctx, cmd, cipher); err != nil {
		t.Fatal(err)
	}
	cmd.LoginSessionID = "revoked"
	if _, err := repo.ApplyApplePush(ctx, cmd, cipher); err != nil {
		t.Fatal(err)
	}
	var stored *string
	if err := pool.QueryRow(ctx, `SELECT login_session_id FROM push_devices WHERE device_id = $1`, cmd.DeviceID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == nil || *stored != "revoked" {
		t.Fatalf("login_session_id = %v, want revoked", stored)
	}
}

// unboundLoginSession is a login session in a TestSweepOfUnboundRegistrationsDB
// case. The registration under test was last written 10 hours ago.
type unboundLoginSession struct {
	user       int
	device     string // auth_sessions.device_id; empty is NULL.
	startedAgo int    // hours
	state      string // "live", "revoked" or "expired" (1 hour ago), or "revoked long ago" (12 hours ago).
}

// A registration from before login_session_id has no recorded login, and a phone
// that is already signed out can never supply one. The sweep removes such a
// registration only when the evidence says the device has no live login: a wrong
// match silently stops push on a working phone, so ambiguous device evidence keeps
// delivering. An account with no live session counts as signed out.
func TestSweepOfUnboundRegistrationsDB(t *testing.T) {
	const account, otherAccount, device = 7, 8, "phone"
	for _, tc := range []struct {
		name     string
		platform string
		sessions []unboundLoginSession
		apiKey   bool
		boundTo  string
		want     string // "stops" or "delivers"
	}{
		{name: "android, the account's only login was revoked", platform: PushPlatformAndroid,
			sessions: []unboundLoginSession{{account, "login-uuid", 20, "revoked"}}, want: "stops"},
		{name: "android, the account's only login expired", platform: PushPlatformAndroid,
			sessions: []unboundLoginSession{{account, "", 20, "expired"}}, want: "stops"},
		{name: "android, the account still has a login elsewhere", platform: PushPlatformAndroid,
			sessions: []unboundLoginSession{{account, "login-uuid", 20, "revoked"}, {account, "browser-uuid", 2, "live"}}, want: "delivers"},
		{name: "apple, the account has no live login and its session predates device ids", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{account, "", 20, "revoked"}}, want: "stops"},
		// No session row means no login: a full restore leaves auth_sessions empty,
		// and every client signs in again afterwards.
		{name: "android, no sessions at all, as after a restore", platform: PushPlatformAndroid, want: "stops"},
		{name: "apple, no sessions at all, as after a restore", platform: PushPlatformApple, want: "stops"},
		{name: "android, only another account is signed in", platform: PushPlatformAndroid,
			sessions: []unboundLoginSession{{otherAccount, "other-uuid", 2, "live"}}, want: "stops"},
		{name: "android, the account's only login began after the registration was written", platform: PushPlatformAndroid,
			sessions: []unboundLoginSession{{account, "browser-uuid", 3, "revoked"}}, want: "stops"},
		// An API key registers with no session and outlives every login.
		{name: "android, the account holds an API key", platform: PushPlatformAndroid, apiKey: true,
			sessions: []unboundLoginSession{{account, "login-uuid", 20, "revoked"}}, want: "delivers"},
		{name: "apple, the account holds an API key", platform: PushPlatformApple, apiKey: true,
			sessions: []unboundLoginSession{{account, device, 20, "revoked"}}, want: "delivers"},
		// The device is signed in to another account on this server: its row here
		// is the other account's to replace, not this sweep's to remove.
		{name: "apple, the device has a live login on another account", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{account, device, 20, "revoked"}, {otherAccount, device, 2, "live"}}, want: "delivers"},
		// An Apple login names the push device, so the device's own login can have
		// ended while the account stays signed in elsewhere.
		{name: "apple, this device's login was revoked, the account lives on elsewhere", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{account, device, 20, "revoked"}, {account, "ipad", 2, "live"}}, want: "stops"},
		{name: "apple, this device's login expired, the account lives on elsewhere", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{account, device, 20, "expired"}, {account, "ipad", 2, "live"}}, want: "stops"},
		{name: "apple, this device has a live login too", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{account, device, 20, "revoked"}, {account, device, 2, "live"}}, want: "delivers"},
		{name: "apple, a live login names no device, so it may be this one", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{account, device, 20, "revoked"}, {account, "", 30, "live"}}, want: "delivers"},
		{name: "apple, the registration was written after this device's login ended", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{account, device, 20, "revoked long ago"}, {account, "ipad", 2, "live"}}, want: "delivers"},
		{name: "apple, this device's login began after the registration was written", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{account, device, 3, "revoked"}, {account, "ipad", 2, "live"}}, want: "delivers"},
		{name: "apple, only another device's login ended", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{account, "ipad", 20, "revoked"}, {account, "mac", 2, "live"}}, want: "delivers"},
		{name: "apple, the ended login on this device was another account's", platform: PushPlatformApple,
			sessions: []unboundLoginSession{{otherAccount, device, 20, "revoked"}, {account, "ipad", 2, "live"}}, want: "delivers"},
		// An Android login never names the push device, so the same evidence on an
		// Android registration is a coincidence.
		{name: "android, a login with a matching id ended, the account lives on elsewhere", platform: PushPlatformAndroid,
			sessions: []unboundLoginSession{{account, device, 20, "revoked"}, {account, "browser-uuid", 2, "live"}}, want: "delivers"},
		// A recorded login is the send-time check's to judge.
		{name: "a registration bound to a live login", platform: PushPlatformApple, boundTo: "s1",
			sessions: []unboundLoginSession{{account, device, 20, "revoked"}, {account, "ipad", 2, "live"}}, want: "delivers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, pool := unboundRegistrationDB(t, tc.platform, account, device)
			for i, s := range tc.sessions {
				insertUnboundLoginSession(t, pool, fmt.Sprintf("s%d", i), s)
			}
			if tc.apiKey {
				if _, err := pool.Exec(t.Context(), `INSERT INTO api_keys (user_id, label, key_hash, revision)
					VALUES ($1, 'integration', encode(sha256('k'), 'hex'), 1)`, account); err != nil {
					t.Fatal(err)
				}
			}
			if tc.boundTo != "" {
				if _, err := pool.Exec(t.Context(), `UPDATE push_devices SET login_session_id = $1`, tc.boundTo); err != nil {
					t.Fatal(err)
				}
			}
			swept, err := repo.DeleteRegistrationsWithNoLogin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := map[int64]string{1: "stops", 0: "delivers"}[swept]; got != tc.want {
				t.Fatalf("registration %s, want it to %s", got, tc.want)
			}
		})
	}
}

// unboundRegistrationDB returns a schema holding one registration with no
// recorded login, last written 10 hours ago.
func unboundRegistrationDB(t *testing.T, platform string, account int, device string) (*PushDeviceRepository, *pgxpool.Pool) {
	t.Helper()
	repo, pool := noLoginsPushDB(t)
	if _, err := registerLegacyMobile(t, repo, platform, account, "default", device, PushModePrivatePush); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE push_devices SET last_seen_at = now() - interval '10 hours'`); err != nil {
		t.Fatal(err)
	}
	return repo, pool
}

func insertUnboundLoginSession(t *testing.T, pool *pgxpool.Pool, id string, s unboundLoginSession) {
	t.Helper()
	expires, revoked := "now() + interval '1 day'", "NULL"
	switch s.state {
	case "revoked":
		revoked = "now() - interval '1 hour'"
	case "revoked long ago":
		revoked = "now() - interval '12 hours'"
	case "expired":
		expires = "now() - interval '1 hour'"
	}
	if _, err := pool.Exec(t.Context(), fmt.Sprintf(`INSERT INTO auth_sessions (id, user_id, device_id, created_at, expires_at, revoked_at)
		VALUES ($1, $2, NULLIF($3, ''), now() - make_interval(hours => $4), %s, %s)`, expires, revoked), id, s.user, s.device, s.startedAgo); err != nil {
		t.Fatal(err)
	}
}

// A sweep removes only the registration, so a phone that signs in again comes
// back however it registers. Clients take a new generation with the same key at
// a new sign-in, and the ordering record stays through the sweep.
func TestSweptPhoneRegistersAgainAtSignInDB(t *testing.T) {
	const account, otherAccount, device = 7, 8, "phone"
	key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	for _, tc := range []struct {
		name          string
		ordered       bool // the phone uses the ordered protocol, not the older route
		loseOrdering  bool // the ordering record is gone by the sign-in
		returningUser int
	}{
		{name: "ordered", ordered: true, returningUser: account},
		{name: "ordering record gone", ordered: true, loseOrdering: true, returningUser: account},
		{name: "another account", ordered: true, returningUser: otherAccount},
		{name: "older client", returningUser: account},
		{name: "older client on another account", returningUser: otherAccount},
	} {
		for _, platform := range []string{PushPlatformAndroid, PushPlatformApple} {
			t.Run(platform+", "+tc.name, func(t *testing.T) {
				repo, pool := noLoginsPushDB(t)
				ctx, cipher := t.Context(), testPushCipher(t)
				register := func(user, generation int, session string) {
					t.Helper()
					var err error
					switch {
					case !tc.ordered:
						_, err = registerLegacyMobile(t, repo, platform, user, "default", device, PushModePrivatePush)
					case platform == PushPlatformApple:
						input := validApplePushInput()
						input.DeviceID, input.LoginSessionID = device, session
						_, err = repo.ApplyApplePush(ctx, ApplePushCommand{UserID: user, ProfileID: "default", InstallationKey: key, Generation: int64(generation), ApplePushRegistrationInput: input}, cipher)
					default:
						_, err = repo.ApplyAndroidPush(ctx, AndroidPushCommand{UserID: user, ProfileID: "default", DeviceID: device, InstallationKey: key, Generation: int64(generation),
							Token: strings.Repeat("a", 64), PushMode: PushModePrivatePush, LoginSessionID: session}, cipher)
					}
					if err != nil {
						t.Fatalf("register account %d: %v", user, err)
					}
				}

				register(account, 1, "")
				insertUnboundLoginSession(t, pool, "ended", unboundLoginSession{account, "login-id", 20, "revoked"})
				if _, err := pool.Exec(ctx, `UPDATE push_devices SET last_seen_at = now() - interval '10 hours'`); err != nil {
					t.Fatal(err)
				}
				if swept, err := repo.DeleteRegistrationsWithNoLogin(ctx); err != nil || swept != 1 {
					t.Fatalf("swept = %d, err = %v, want 1", swept, err)
				}
				if tc.loseOrdering {
					if _, err := pool.Exec(ctx, `TRUNCATE apple_push_installations, android_push_installations`); err != nil {
						t.Fatal(err)
					}
				}

				insertUnboundLoginSession(t, pool, "again", unboundLoginSession{tc.returningUser, "login-id", 0, "live"})
				register(tc.returningUser, 2, "again")

				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				listed, err := repo.ListEnabledPushByProfiles(ctx, tx, []profileKey{{tc.returningUser, "default"}}, []string{platform})
				_ = tx.Rollback(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if got := listed[profileKey{tc.returningUser, "default"}]; len(got) != 1 || got[0].DeviceID != device {
					t.Fatalf("fan-out after the sign-in = %+v, want the phone", got)
				}
				if swept, err := repo.DeleteRegistrationsWithNoLogin(ctx); err != nil || swept != 0 {
					t.Fatalf("second sweep removed %d, err = %v", swept, err)
				}
			})
		}
	}
}
