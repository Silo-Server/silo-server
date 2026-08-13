package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDispatchOperationalEnqueuesApplePushAttempts(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run DB-backed operational push dispatch test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(pool.Close)

	// Every identifier carries the same nanosecond suffix so binaries sharing
	// one database stay out of each other's rows.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	id := func(name string) string { return name + "-" + suffix }
	primaryProfile, otherProfile := id("profile-1"), id("profile-2")
	privateDevice := id("device-private")
	deliveryID := id("delivery-request")

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		// push_delivery_attempts cascades from both parents.
		if _, err := pool.Exec(cleanupCtx,
			`DELETE FROM notification_deliveries WHERE profile_id LIKE '%-' || $1`, suffix); err != nil {
			t.Errorf("clean up notification deliveries: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx,
			`DELETE FROM push_devices WHERE profile_id LIKE '%-' || $1`, suffix); err != nil {
			t.Errorf("clean up push devices: %v", err)
		}
	})

	seedDevices := []struct {
		deviceRowID string
		profileID   string
		installID   string
		pushMode    string
		enabled     bool
	}{
		{privateDevice, primaryProfile, id("local-private"), PushModePrivatePush, true},
		{id("device-in-app"), primaryProfile, id("local-in-app"), PushModeInAppOnly, true},
		{id("device-disabled"), primaryProfile, id("local-disabled"), PushModePrivatePush, false},
		{id("device-other-profile"), otherProfile, id("local-other"), PushModePrivatePush, true},
	}
	for _, device := range seedDevices {
		if _, err := pool.Exec(ctx, `
			INSERT INTO push_devices
				(id, user_id, profile_id, device_id, platform, provider, apns_environment,
				 apns_topic, apns_token_ciphertext, apns_token_hash, server_device_id,
				 push_mode, enabled)
			VALUES ($1, 42, $2, $3, 'apple', 'silo_relay', 'sandbox',
				'org.siloserver.silo', 'ciphertext', $4, $5, $6, $7)`,
			device.deviceRowID, device.profileID, device.installID,
			"hash-"+device.deviceRowID, "server-"+device.deviceRowID,
			device.pushMode, device.enabled,
		); err != nil {
			t.Fatalf("seed push device %s: %v", device.deviceRowID, err)
		}
	}

	system := &System{
		pool:           pool,
		Settings:       NewSettings(mapSettingReader{SettingApplePushDeliveryEnabled: "true"}),
		Deliveries:     NewDeliveryRepository(pool),
		pushDeviceRepo: NewPushDeviceRepository(pool),
		dispatcher:     NewMultiDispatcher(),
		logger:         slog.New(slog.DiscardHandler),
	}

	inserted, err := system.DispatchOperational(ctx, Delivery{
		ID:          deliveryID,
		UserID:      42,
		ProfileID:   primaryProfile,
		Type:        DeliveryTypeRequestFulfilled,
		ReasonFlags: []byte(`{}`),
	}, OperationalDispatch{})
	if err != nil {
		t.Fatalf("dispatch operational: %v", err)
	}
	if inserted == nil || inserted.ID != deliveryID {
		t.Fatalf("inserted = %+v", inserted)
	}

	var gotDeliveryID, pushDeviceID, triggerType, provider, platform, outcome string
	if err := pool.QueryRow(ctx, `
		SELECT notification_delivery_id, push_device_id, trigger_type, provider, platform, outcome
		FROM push_delivery_attempts
		WHERE notification_delivery_id = $1
	`, inserted.ID).Scan(&gotDeliveryID, &pushDeviceID, &triggerType, &provider, &platform, &outcome); err != nil {
		t.Fatalf("query push attempt: %v", err)
	}
	if gotDeliveryID != inserted.ID ||
		pushDeviceID != privateDevice ||
		triggerType != PushTriggerDelivery ||
		provider != PushProviderSiloRelay ||
		platform != PushPlatformApple ||
		outcome != PushOutcomePending {
		t.Fatalf("unexpected push attempt: delivery=%q device=%q trigger=%q provider=%q platform=%q outcome=%q",
			gotDeliveryID, pushDeviceID, triggerType, provider, platform, outcome)
	}

	// Only the private-push device on the target profile is attempted: the
	// in-app-only and disabled installs and the other profile's device are not.
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM push_delivery_attempts
		WHERE push_device_id IN (SELECT id FROM push_devices WHERE profile_id LIKE '%-' || $1)
	`, suffix).Scan(&count); err != nil {
		t.Fatalf("count push attempts: %v", err)
	}
	if count != 1 {
		t.Fatalf("push attempt count = %d, want 1", count)
	}
}
