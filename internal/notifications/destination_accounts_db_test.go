package notifications

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestChannelTargetsStayOnTheDeliveryAccount covers profile IDs that repeat
// across accounts: accounts 1 and 2 each have a "default" profile with a
// webhook, a web push subscription, and an Apple push device, and only
// account 2's follows the series. The episode delivery for account 2's
// "default", and an operational notice for it, queue attempts for account 2's
// targets only.
func TestChannelTargetsStayOnTheDeliveryAccount(t *testing.T) {
	pool := inboxPageDB(t)
	ctx := t.Context()
	c := seedAccessCatalog(t, pool)
	seedFanoutEvent(t, pool, c, nil)
	seedTwoAccountTargets(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO profile_series_interest (user_id, profile_id, library_id, series_id, favorite)
		VALUES (2, 'default', $1, $2, true)`, c.library, c.series); err != nil {
		t.Fatalf("seed interest: %v", err)
	}

	settings := NewSettings(mapSettingReader{SettingFanoutSettleSeconds: "0", SettingWebhooksEnabled: "true"})
	worker := newAccessFanoutWorker(pool, scopeByProfile{"default": {}})
	worker.settings = settings
	worker.SetWebhookOutbox(NewWebhookRepository(pool), newProfileRateLimiter())
	worker.SetWebPushOutbox(NewWebPushRepository(pool))
	worker.SetPushOutbox(NewPushDeviceRepository(pool))
	if _, err := worker.processBatch(ctx); err != nil {
		t.Fatalf("processBatch: %v", err)
	}
	if got := accountRecipients(t, pool, "event-1"); !slices.Equal(got, []string{"2/default"}) {
		t.Fatalf("event-1 recipients = %v, want account 2's default", got)
	}
	want := []string{"browser-2", "hook-2", "phone-2"}
	if got := attemptTargets(t, pool, `d.release_event_id = 'event-1'`); !slices.Equal(got, want) {
		t.Fatalf("episode attempts = %v, want %v", got, want)
	}

	system := newAccessSystem(pool, scopeByProfile{"default": {}})
	system.Settings = settings
	system.webhookRepo = NewWebhookRepository(pool)
	system.webPushRepo = NewWebPushRepository(pool)
	system.pushDeviceRepo = NewPushDeviceRepository(pool)
	notice, err := system.DispatchOperational(ctx, Delivery{
		ID: "notice-1", UserID: 2, ProfileID: "default",
		Type: DeliveryTypeWebhookAutoDisabled, ReasonFlags: []byte(`{}`),
	}, OperationalDispatch{WebhookFilter: func(Webhook) bool { return true }})
	if err != nil || notice == nil {
		t.Fatalf("DispatchOperational = %+v, %v; want a delivery", notice, err)
	}
	if got := attemptTargets(t, pool, `d.id = 'notice-1'`); !slices.Equal(got, want) {
		t.Fatalf("operational attempts = %v, want %v", got, want)
	}
}

// TestSendersSkipAnotherAccountsTargets covers attempts already queued for a
// target on another account: a build that matched targets on profile ID
// alone queued them, or the browser or device was registered to another
// account's profile with the same ID before the send. Each sender fails such
// an attempt without contacting the target.
func TestSendersSkipAnotherAccountsTargets(t *testing.T) {
	pool := inboxPageDB(t)
	ctx := t.Context()
	seedTwoAccountTargets(t, pool)
	for _, stmt := range []string{
		`INSERT INTO notification_deliveries (id, user_id, profile_id, type, reason_flags)
			VALUES ('delivery-2', 2, 'default', 'webhook.auto_disabled', '{}')`,
		`INSERT INTO webhook_delivery_attempts (id, notification_delivery_id, webhook_id, attempt_number, outcome)
			VALUES ('hook-attempt', 'delivery-2', 'hook-1', 0, 'pending')`,
		`INSERT INTO web_push_delivery_attempts (id, notification_delivery_id, subscription_id, attempt_number, outcome)
			VALUES ('browser-attempt', 'delivery-2', 'browser-1', 0, 'pending')`,
		`INSERT INTO push_delivery_attempts (id, notification_delivery_id, push_device_id, trigger_type, provider, platform, attempt_number, outcome)
			VALUES ('phone-attempt', 'delivery-2', 'phone-1', 'delivery', 'silo_relay', 'apple', 0, 'pending')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed attempts: %v", err)
		}
	}

	refuse := &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Errorf("sent %s %s for another account's target", req.Method, req.URL)
		return nil, errors.New("refused")
	})}
	settings := NewSettings(mapSettingReader{SettingWebhooksEnabled: "true"})
	deliveries := NewDeliveryRepository(pool)

	webhooks := newWebhookSender(NewWebhookRepository(pool), deliveries, nil, settings)
	webhooks.client = refuse
	webhooks.processAttempt(ctx, DeliveryAttempt{ID: "hook-attempt", NotificationDeliveryID: "delivery-2", TargetID: "hook-1"})

	webPush := newWebPushSender(NewWebPushRepository(pool), deliveries, nil, settings)
	webPush.client = refuse
	webPush.processAttempt(ctx, DeliveryAttempt{ID: "browser-attempt", NotificationDeliveryID: "delivery-2", TargetID: "browser-1"})

	push := newPushSender(NewPushDeviceRepository(pool), deliveries, nil, settings)
	push.client = refuse
	deliveryID := "delivery-2"
	push.processAttempt(ctx, PushDeliveryAttempt{ID: "phone-attempt", NotificationDeliveryID: &deliveryID, PushDeviceID: "phone-1"})

	rows, err := pool.Query(ctx, `
		SELECT id || ' ' || outcome FROM webhook_delivery_attempts
		UNION ALL SELECT id || ' ' || outcome FROM web_push_delivery_attempts
		UNION ALL SELECT id || ' ' || outcome FROM push_delivery_attempts
		ORDER BY 1`)
	if err != nil {
		t.Fatalf("query attempts: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if want := []string{"browser-attempt failed", "hook-attempt failed", "phone-attempt failed"}; !slices.Equal(got, want) {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
}

// seedTwoAccountTargets creates the channel target and attempt tables in the
// test schema and gives accounts 1 and 2 each a "default" profile with one
// webhook, web push subscription, and Apple push device, suffixed -1 and -2.
func seedTwoAccountTargets(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed targets: %v", err)
		}
	}
	exec(`
		CREATE TABLE notification_webhooks (LIKE public.notification_webhooks INCLUDING ALL);
		CREATE TABLE webhook_delivery_attempts (LIKE public.webhook_delivery_attempts INCLUDING ALL);
		CREATE TABLE web_push_subscriptions (LIKE public.web_push_subscriptions INCLUDING ALL);
		CREATE TABLE web_push_delivery_attempts (LIKE public.web_push_delivery_attempts INCLUDING ALL);
		CREATE TABLE push_devices (LIKE public.push_devices INCLUDING ALL);
		CREATE TABLE push_delivery_attempts (LIKE public.push_delivery_attempts INCLUDING ALL)`)
	// The webhook and push device keys are unique per profile ID, so the two
	// accounts' targets carry different names and device IDs.
	for _, account := range []string{"1", "2"} {
		exec(`INSERT INTO notification_webhooks (id, user_id, profile_id, name, type, url_ciphertext, url_host)
			VALUES ('hook-' || $1, $1::int, 'default', 'hook-' || $1, 'discord', 'ciphertext', 'example.test')`, account)
		exec(`INSERT INTO web_push_subscriptions (id, user_id, profile_id, endpoint, p256dh, auth)
			VALUES ('browser-' || $1, $1::int, 'default', 'https://push.example.test/' || $1, 'key', 'auth')`, account)
		exec(`INSERT INTO push_devices (id, user_id, profile_id, device_id, platform, provider, apns_environment,
				apns_topic, apns_token_ciphertext, apns_token_hash, server_device_id)
			VALUES ('phone-' || $1, $1::int, 'default', 'phone-' || $1, 'apple', 'silo_relay', 'sandbox',
				'topic', 'ciphertext', 'hash-' || $1, 'server-phone-' || $1)`, account)
	}
}

// attemptTargets lists the webhook, web push, and Apple push targets queued
// for the deliveries matching where (over notification_deliveries d).
func attemptTargets(t *testing.T, pool *pgxpool.Pool, where string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT a.webhook_id FROM webhook_delivery_attempts a
			JOIN notification_deliveries d ON d.id = a.notification_delivery_id WHERE `+where+`
		UNION ALL
		SELECT a.subscription_id FROM web_push_delivery_attempts a
			JOIN notification_deliveries d ON d.id = a.notification_delivery_id WHERE `+where+`
		UNION ALL
		SELECT a.push_device_id FROM push_delivery_attempts a
			JOIN notification_deliveries d ON d.id = a.notification_delivery_id WHERE `+where+`
		ORDER BY 1`)
	if err != nil {
		t.Fatalf("query attempts: %v", err)
	}
	targets, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	return targets
}
