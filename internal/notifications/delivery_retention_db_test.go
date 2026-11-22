package notifications

import (
	"testing"
	"time"
)

// Inbox retention keeps a delivery while a mobile push or web push attempt
// can still run, since deleting it cascades to the attempt. A long
// Retry-After schedules the retry past the retention cutoff, and the row
// must outlive it. A retry that just fell due keeps the row until the sweep
// can claim it. A stranded attempt holds the row only for a bounded time.
func TestPushRetryOutlivesInboxRetentionDB(t *testing.T) {
	pool := schemaDB(t, "push_retention_")
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE notification_deliveries (LIKE public.notification_deliveries INCLUDING ALL);
		CREATE TABLE webhook_delivery_attempts (LIKE public.webhook_delivery_attempts INCLUDING ALL);
		CREATE TABLE plugin_destination_delivery_attempts (LIKE public.plugin_destination_delivery_attempts INCLUDING ALL);
		CREATE TABLE push_delivery_attempts (LIKE public.push_delivery_attempts INCLUDING ALL);
		ALTER TABLE push_delivery_attempts ADD FOREIGN KEY (notification_delivery_id) REFERENCES notification_deliveries(id) ON DELETE CASCADE;
		CREATE TABLE web_push_delivery_attempts (LIKE public.web_push_delivery_attempts INCLUDING ALL);
		ALTER TABLE web_push_delivery_attempts ADD FOREIGN KEY (notification_delivery_id) REFERENCES notification_deliveries(id) ON DELETE CASCADE`); err != nil {
		t.Fatal(err)
	}
	deliveries := NewDeliveryRepository(pool)

	attempts := map[string]string{
		"push": `INSERT INTO push_delivery_attempts (id, notification_delivery_id, push_device_id, provider, platform, outcome, next_retry_at)
			VALUES ($1, $1, 'device', 'silo_relay', 'apple', $2, now() + $3::interval)`,
		"web push": `INSERT INTO web_push_delivery_attempts (id, notification_delivery_id, subscription_id, attempt_number, outcome, next_retry_at)
			VALUES ($1, $1, 'subscription', 1, $2, now() + $3::interval)`,
	}
	for channel, insertAttempt := range attempts {
		t.Run(channel, func(t *testing.T) {
			queue := func(id string, age time.Duration, outcome string, retryIn time.Duration) {
				t.Helper()
				if _, err := pool.Exec(ctx, `
					INSERT INTO notification_deliveries (id, user_id, profile_id, type, reason_flags, created_at)
					VALUES ($1, 1, 'default', $2, '{}', now() - $3::interval)`, id, DeliveryTypeRequestDeclined, age); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, insertAttempt, id, outcome, retryIn); err != nil {
					t.Fatal(err)
				}
			}
			prefix := channel + "-"
			// A 429 with Retry-After just under a day, under one day of unread retention.
			queue(prefix+"scheduled", pluginAttemptMaxAge+time.Hour, "retrying", 23*time.Hour)
			queue(prefix+"pending", time.Hour, "pending", 0)
			// Past the age bound and just due: the sweep hasn't claimed it yet.
			queue(prefix+"due", pluginAttemptMaxAge+time.Hour, "retrying", -time.Minute)
			// Past the age bound and due long ago: stranded, so retention takes it.
			queue(prefix+"stranded", pluginAttemptMaxAge+time.Hour, "retrying", -2*retentionClaimGrace)

			cutoff := time.Now().Add(time.Minute)
			if _, err := deliveries.DeleteOld(ctx, cutoff, cutoff, cutoff); err != nil {
				t.Fatal(err)
			}
			for id, want := range map[string]bool{prefix + "scheduled": true, prefix + "pending": true, prefix + "due": true, prefix + "stranded": false} {
				row, err := deliveries.GetRowByID(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if got := row != nil; got != want {
					t.Errorf("%s kept = %v, want %v", id, got, want)
				}
			}
		})
	}
}
