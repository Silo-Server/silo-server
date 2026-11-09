package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// An administrator test push carries the device profile's newest
// notification, so the device fetches and shows that notification's real
// text. Profile IDs repeat across accounts, so neither another account's
// same-named profile nor a sibling profile lends its notification, and a
// profile with no notification keeps a bare test. Dispatching that
// notification must leave a pending test to the request that enqueued it.
// Repeating the test beside the delivery's own attempt must not collide on the
// attempt key, and none of it touches the inbox.
func TestSendPushTestCarriesTheProfilesNewestNotification(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("testpush-%d", time.Now().UnixNano())
	var accounts []int
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM push_devices WHERE user_id = ANY($1)`, accounts)
		_, _ = pool.Exec(cleanup, `DELETE FROM notification_deliveries WHERE user_id = ANY($1)`, accounts)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = ANY($1)`, accounts)
	})
	for _, name := range []string{"account", "other-account"} {
		var id int
		username := prefix + "-" + name
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
			username, username+"@example.test").Scan(&id); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		accounts = append(accounts, id)
	}
	account, otherAccount := accounts[0], accounts[1]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	const profile = "default"
	quietProfile := prefix + "-quiet"
	newest := prefix + "-newest"
	base := time.Now().Add(-time.Hour).UTC()
	for i, delivery := range []struct {
		id      string
		account int
		profile string
	}{
		{prefix + "-older", account, profile},
		{newest, account, profile},
		{prefix + "-other-account", otherAccount, profile},
		{prefix + "-sibling", account, prefix + "-sibling"},
	} {
		exec(`INSERT INTO notification_deliveries (id, user_id, profile_id, type, reason_flags, created_at)
			VALUES ($1, $2, $3, 'webhook.auto_disabled', '{}', $4)`,
			delivery.id, delivery.account, delivery.profile, base.Add(time.Duration(i)*time.Minute))
	}

	cipher := testPushCipher(t)
	repo := NewPushDeviceRepository(pool)
	register := func(profile, device, token string) *PushDevice {
		t.Helper()
		registered, err := repo.UpsertApple(ctx, ApplePushDeviceRegistration{
			UserID: account, ProfileID: profile, DeviceID: prefix + "-" + device, APNsToken: token,
			APNsEnvironment: APNsEnvironmentSandbox, APNsTopic: ApplePushTopicSilo, PushMode: PushModePrivatePush,
		}, cipher)
		if err != nil {
			t.Fatalf("register %s: %v", device, err)
		}
		return registered
	}
	phone := register(profile, "phone", strings.Repeat("a", 64))
	quietPhone := register(quietProfile, "quiet-phone", strings.Repeat("b", 64))
	// The delivery's own first attempt holds attempt number 1 for the phone.
	exec(`INSERT INTO push_delivery_attempts (id, notification_delivery_id, push_device_id, trigger_type, provider, platform, attempt_number, outcome)
		VALUES ($1, $2, $3, 'delivery', 'silo_relay', 'apple', 1, 'delivered')`, prefix+"-own", newest, phone.ID)

	var relayed []map[string]any
	settings := NewSettings(mapSettingReader{SettingPushRelayURL: DefaultPushRelayURL, SettingPushRelayAPIKey: "relay-key"})
	sender := newPushSender(repo, NewDeliveryRepository(pool), cipher, settings)
	sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode relay body: %v", err)
		}
		relayed = append(relayed, body)
		return relayResponse(http.StatusOK, `{"request_id":"relay-request","status":"accepted"}`), nil
	})}
	system := &System{Settings: settings, pushDeviceRepo: repo, pushSender: sender}

	pending, _, err := repo.EnqueueTestAttempt(ctx, PushPlatformApple, account, profile, phone.ServerDeviceID)
	if err != nil {
		t.Fatalf("enqueue test: %v", err)
	}
	if dispatched, err := repo.ClaimPendingPushForDelivery(ctx, newest); err != nil || len(dispatched) != 0 {
		t.Fatalf("delivery dispatch claimed %+v (err %v); the pending test belongs to its request", dispatched, err)
	}
	if claimed, err := repo.ClaimPushAttemptByID(ctx, pending.ID); err != nil || len(claimed) != 1 {
		t.Fatalf("test claim = %+v (err %v), want the pending test", claimed, err)
	}

	for range 2 {
		result, err := system.SendApplePushTest(ctx, account, profile, phone.ServerDeviceID)
		if err != nil {
			t.Fatalf("send test: %v", err)
		}
		body := relayed[len(relayed)-1]
		if result.Outcome != PushOutcomeDelivered || result.DeliveryID != newest || body["delivery_id"] != newest || body["collapse_id"] != newest {
			t.Fatalf("result = %+v, relay body = %v; want delivered carrying %s", result, body, newest)
		}
	}

	result, err := system.SendApplePushTest(ctx, account, quietProfile, quietPhone.ServerDeviceID)
	if err != nil {
		t.Fatalf("send bare test: %v", err)
	}
	if body := relayed[len(relayed)-1]; result.Outcome != PushOutcomeDelivered || result.DeliveryID != "" || body["delivery_id"] != result.AttemptID {
		t.Fatalf("bare test result = %+v, relay body = %v", result, body)
	}

	var rows, read int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(read_at) FROM notification_deliveries WHERE user_id = ANY($1)`, accounts).Scan(&rows, &read); err != nil {
		t.Fatal(err)
	}
	if rows != 4 || read != 0 {
		t.Fatalf("inbox rows = %d with %d read; test pushes must not touch the inbox", rows, read)
	}
}

// A test push reaches only the caller's own account. Profile IDs such as the
// legacy `default` repeat across accounts, so a profile match alone would pick
// whichever account's phone was seen last and push that person a real
// notification. Another account's device must look exactly like no device.
func TestSendPushTestStaysOnTheCallersAccount(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("testpush-scope-%d", time.Now().UnixNano())
	admin, stranger := seedPushScopeAccounts(t, pool, prefix)

	const sharedProfile = "default"
	sibling := prefix + "-sibling"
	strangersOnlyProfile := prefix + "-strangers"

	cipher := testPushCipher(t)
	repo := NewPushDeviceRepository(pool)
	var relayed []string
	system := newTokenRecordingPushSystem(t, pool, repo, cipher, &relayed)

	for _, platform := range []struct {
		name     string
		register func(account int, profile, device, token string) (*PushDevice, error)
		send     func(ctx context.Context, userID int, profileID, serverDeviceID string) (*ApplePushTestResult, error)
	}{
		{
			name: "apple",
			register: func(account int, profile, device, token string) (*PushDevice, error) {
				return repo.UpsertApple(ctx, ApplePushDeviceRegistration{
					UserID: account, ProfileID: profile, DeviceID: device, APNsToken: token,
					APNsEnvironment: APNsEnvironmentSandbox, APNsTopic: ApplePushTopicSilo, PushMode: PushModePrivatePush,
				}, cipher)
			},
			send: system.SendApplePushTest,
		},
		{
			name: "android",
			register: func(account int, profile, device, token string) (*PushDevice, error) {
				return repo.UpsertFCM(ctx, FCMPushDeviceRegistration{
					UserID: account, ProfileID: profile, DeviceID: device, FCMToken: token, PushMode: PushModePrivatePush,
				}, cipher)
			},
			send: system.SendAndroidPushTest,
		},
	} {
		t.Run(platform.name, func(t *testing.T) {
			adminToken := strings.Repeat("a", 64)
			siblingToken := strings.Repeat("c", 64)
			register := func(account int, profile, device, token string) *PushDevice {
				t.Helper()
				registered, err := platform.register(account, profile, prefix+"-"+platform.name+"-"+device, token)
				if err != nil {
					t.Fatalf("register %s: %v", device, err)
				}
				return registered
			}
			register(admin, sharedProfile, "admin-phone", adminToken)
			register(admin, sibling, "sibling-phone", siblingToken)
			// Registered last, so it is the newest device for the shared profile.
			strangerPhone := register(stranger, sharedProfile, "stranger-phone", strings.Repeat("b", 64))
			strangerOther := register(stranger, strangersOnlyProfile, "stranger-other", strings.Repeat("d", 64))

			relayed = nil
			if _, err := platform.send(ctx, admin, sharedProfile, ""); err != nil {
				t.Fatalf("send to own shared profile: %v", err)
			}
			if _, err := platform.send(ctx, admin, sibling, ""); err != nil {
				t.Fatalf("send to own sibling profile: %v", err)
			}
			if len(relayed) != 2 || relayed[0] != adminToken || relayed[1] != siblingToken {
				t.Fatalf("relayed tokens = %q; want the admin's own phone, then the sibling profile's", relayed)
			}

			relayed = nil
			_, missing := platform.send(ctx, admin, prefix+"-no-such-profile", "")
			if !errors.Is(missing, ErrPushDeliveryNotFound) {
				t.Fatalf("missing profile err = %v, want not found", missing)
			}
			for name, target := range map[string]struct{ profile, device string }{
				"device selector": {sharedProfile, strangerPhone.ServerDeviceID},
				"no selector":     {strangersOnlyProfile, ""},
			} {
				if _, err := platform.send(ctx, admin, target.profile, target.device); err == nil || err.Error() != missing.Error() {
					t.Errorf("%s: err = %v, want the same error as no device (%v)", name, err, missing)
				}
			}
			var attempts int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_delivery_attempts WHERE push_device_id = ANY($1)`,
				[]string{strangerPhone.ID, strangerOther.ID}).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if attempts != 0 || len(relayed) != 0 {
				t.Fatalf("stranger attempts = %d, relayed = %q; want neither", attempts, relayed)
			}
		})
	}
}

func newTokenRecordingPushSystem(t *testing.T, pool *pgxpool.Pool, repo *PushDeviceRepository, cipher *secret.Cipher, relayed *[]string) *System {
	t.Helper()
	settings := NewSettings(mapSettingReader{SettingPushRelayURL: DefaultPushRelayURL, SettingPushRelayAPIKey: "relay-key"})
	sender := newPushSender(repo, NewDeliveryRepository(pool), cipher, settings)
	sender.client = &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode relay body: %v", err)
		}
		*relayed = append(*relayed, body.Token)
		return relayResponse(http.StatusOK, `{"request_id":"relay-request","status":"accepted"}`), nil
	})}
	return &System{Settings: settings, pushDeviceRepo: repo, pushSender: sender}
}

func seedPushScopeAccounts(t *testing.T, pool *pgxpool.Pool, prefix string) (admin, stranger int) {
	t.Helper()
	ctx := context.Background()
	var accounts []int
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM push_devices WHERE user_id = ANY($1)`, accounts)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = ANY($1)`, accounts)
	})
	for _, name := range []string{"admin", "stranger"} {
		var id int
		username := prefix + "-" + name
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
			username, username+"@example.test").Scan(&id); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		accounts = append(accounts, id)
	}
	return accounts[0], accounts[1]
}
