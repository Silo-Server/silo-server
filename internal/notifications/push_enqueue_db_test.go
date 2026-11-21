package notifications

import (
	"context"
	"testing"
	"time"
)

// A browser that unsubscribes, or a device removed at sign-out, after the
// delivery listed it gets no attempt. Its foreign key must not abort the
// delivery transaction, which also holds the inbox row and the other
// channels' attempts.
func TestPushEnqueueSkipsTargetsRemovedAfterListingDB(t *testing.T) {
	pool := schemaDB(t, "push_enqueue_")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE web_push_subscriptions (LIKE public.web_push_subscriptions INCLUDING ALL);
		CREATE TABLE web_push_delivery_attempts (LIKE public.web_push_delivery_attempts INCLUDING ALL);
		ALTER TABLE web_push_delivery_attempts ADD FOREIGN KEY (subscription_id) REFERENCES web_push_subscriptions(id) ON DELETE CASCADE;
		CREATE TABLE push_devices (LIKE public.push_devices INCLUDING ALL);
		CREATE TABLE push_delivery_attempts (LIKE public.push_delivery_attempts INCLUDING ALL);
		ALTER TABLE push_delivery_attempts ADD FOREIGN KEY (push_device_id) REFERENCES push_devices(id) ON DELETE CASCADE;
		INSERT INTO web_push_subscriptions (id, user_id, profile_id, endpoint, p256dh, auth) VALUES
			('sub-kept', 7, 'default', 'https://push.example.test/kept', 'key', 'auth'),
			('sub-gone', 7, 'default', 'https://push.example.test/gone', 'key', 'auth');
		INSERT INTO push_devices (id, user_id, profile_id, device_id, platform, provider, server_device_id, fcm_token_ciphertext, fcm_token_hash) VALUES
			('device-kept', 7, 'default', 'phone', 'android', 'silo_relay', 'server-phone', 'sealed', 'hash-phone'),
			('device-gone', 7, 'default', 'tablet', 'android', 'silo_relay', 'server-tablet', 'sealed', 'hash-tablet');
		-- Removed after the delivery listed them.
		DELETE FROM web_push_subscriptions WHERE id = 'sub-gone';
		DELETE FROM push_devices WHERE id = 'device-gone';`); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := NewWebPushRepository(pool).EnqueueAttempts(ctx, tx, []DeliveryAttempt{
		{ID: "web-kept", NotificationDeliveryID: "delivery", TargetID: "sub-kept"},
		{ID: "web-gone", NotificationDeliveryID: "delivery", TargetID: "sub-gone"},
	}); err != nil {
		t.Fatalf("enqueue web push: %v", err)
	}
	if err := NewPushDeviceRepository(pool).EnqueuePushAttempts(ctx, tx,
		newPushDeliveryAttempts("delivery", []PushDevice{
			{ID: "device-kept", Provider: PushProviderSiloRelay, Platform: PushPlatformAndroid},
			{ID: "device-gone", Provider: PushProviderSiloRelay, Platform: PushPlatformAndroid},
		})); err != nil {
		t.Fatalf("enqueue push: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var web, push []string
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT array_agg(subscription_id) FROM web_push_delivery_attempts),
		(SELECT array_agg(push_device_id || ':' || platform || ':' || provider || ':' || trigger_type || ':' || outcome) FROM push_delivery_attempts)`).
		Scan(&web, &push); err != nil {
		t.Fatal(err)
	}
	if len(web) != 1 || web[0] != "sub-kept" {
		t.Fatalf("web push attempts for %v, want sub-kept only", web)
	}
	want := "device-kept:" + PushPlatformAndroid + ":" + PushProviderSiloRelay + ":" + PushTriggerDelivery + ":" + PushOutcomePending
	if len(push) != 1 || push[0] != want {
		t.Fatalf("push attempts %v, want [%s]", push, want)
	}
}

// A retention sweep skips a target that a delivery still holds from its
// enqueue and removes it on a later sweep. Waiting on it instead could
// deadlock with a delivery locking the same targets in id order, and abort
// that delivery with its inbox row.
func TestPushRetentionSkipsTargetsADeliveryHoldsDB(t *testing.T) {
	pool := schemaDB(t, "push_retention_")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE auth_sessions (LIKE public.auth_sessions INCLUDING ALL);
		CREATE TABLE web_push_subscriptions (LIKE public.web_push_subscriptions INCLUDING ALL);
		CREATE TABLE web_push_delivery_attempts (LIKE public.web_push_delivery_attempts INCLUDING ALL);
		ALTER TABLE web_push_delivery_attempts ADD FOREIGN KEY (subscription_id) REFERENCES web_push_subscriptions(id) ON DELETE CASCADE;
		CREATE TABLE push_devices (LIKE public.push_devices INCLUDING ALL);
		CREATE TABLE push_delivery_attempts (LIKE public.push_delivery_attempts INCLUDING ALL);
		ALTER TABLE push_delivery_attempts ADD FOREIGN KEY (push_device_id) REFERENCES push_devices(id) ON DELETE CASCADE;
		INSERT INTO auth_sessions (id, user_id, expires_at, revoked_at) VALUES ('ended', 7, now() + interval '1 hour', now());
		INSERT INTO web_push_subscriptions (id, user_id, profile_id, endpoint, p256dh, auth, login_session_id)
			VALUES ('sub', 7, 'default', 'https://push.example.test/browser', 'key', 'auth', 'ended');
		INSERT INTO push_devices (id, user_id, profile_id, device_id, platform, provider, server_device_id, fcm_token_ciphertext, fcm_token_hash, login_session_id)
			VALUES ('device', 7, 'default', 'phone', 'android', 'silo_relay', 'server-phone', 'sealed', 'hash-phone', 'ended');`); err != nil {
		t.Fatal(err)
	}
	webPush, devices := NewWebPushRepository(pool), NewPushDeviceRepository(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := webPush.EnqueueAttempts(ctx, tx, []DeliveryAttempt{{ID: "web", NotificationDeliveryID: "delivery", TargetID: "sub"}}); err != nil {
		t.Fatal(err)
	}
	if err := devices.EnqueuePushAttempts(ctx, tx, newPushDeliveryAttempts("delivery", []PushDevice{
		{ID: "device", Provider: PushProviderSiloRelay, Platform: PushPlatformAndroid},
	})); err != nil {
		t.Fatal(err)
	}

	sweep := func() (int64, int64) {
		t.Helper()
		// A sweep that waits on the delivery's locks never returns before
		// the deadline.
		sweepCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		subscriptions, err := webPush.DeleteEndedLoginSubscriptions(sweepCtx)
		if err != nil {
			t.Fatalf("web push sweep: %v", err)
		}
		registrations, err := devices.DeleteEndedLoginRegistrations(sweepCtx)
		if err != nil {
			t.Fatalf("push sweep: %v", err)
		}
		return subscriptions, registrations
	}
	if subscriptions, registrations := sweep(); subscriptions != 0 || registrations != 0 {
		t.Fatalf("sweep beside a delivery removed %d subscriptions and %d registrations, want none", subscriptions, registrations)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if subscriptions, registrations := sweep(); subscriptions != 1 || registrations != 1 {
		t.Fatalf("later sweep removed %d subscriptions and %d registrations, want 1 each", subscriptions, registrations)
	}
}
