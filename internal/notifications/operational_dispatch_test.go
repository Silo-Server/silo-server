package notifications

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDispatchOperationalEnqueuesApplePushAttempts(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run DB-backed operational push dispatch test")
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
		CREATE TEMP TABLE notification_deliveries (
			id text PRIMARY KEY,
			release_event_id text,
			user_id integer NOT NULL,
			profile_id text NOT NULL,
			library_id integer,
			series_id text,
			episode_id text,
			type text NOT NULL,
			reason_flags jsonb NOT NULL DEFAULT '{}'::jsonb,
			status text NOT NULL DEFAULT 'delivered',
			read_at timestamptz,
			delivered_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now()
		) ON COMMIT PRESERVE ROWS;

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
			updated_at timestamptz NOT NULL DEFAULT now()
		) ON COMMIT PRESERVE ROWS;

		CREATE TEMP TABLE push_delivery_attempts (
			id text PRIMARY KEY,
			notification_delivery_id text,
			push_device_id text NOT NULL,
			trigger_type text NOT NULL,
			provider text NOT NULL,
			platform text NOT NULL,
			attempt_number integer NOT NULL DEFAULT 0,
			attempted_at timestamptz,
			next_retry_at timestamptz,
			outcome text NOT NULL DEFAULT 'pending',
			relay_request_id text,
			upstream_status integer,
			upstream_reason text,
			failure_message text,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			UNIQUE (notification_delivery_id, push_device_id, trigger_type)
		) ON COMMIT PRESERVE ROWS;
	`); err != nil {
		t.Fatalf("create temp notification push tables: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO push_devices
			(id, user_id, profile_id, device_id, platform, provider, apns_environment, apns_topic,
			 apns_token_ciphertext, apns_token_hash, server_device_id, push_mode, enabled)
		VALUES
			('device-private', 42, 'profile-1', 'local-private', 'apple', 'silo_relay', 'sandbox',
			 'org.siloserver.silo', 'ciphertext', 'hash-1', 'server-private', 'private_push', true),
			('device-in-app', 42, 'profile-1', 'local-in-app', 'apple', 'silo_relay', 'sandbox',
			 'org.siloserver.silo', 'ciphertext', 'hash-2', 'server-in-app', 'in_app_only', true),
			('device-disabled', 42, 'profile-1', 'local-disabled', 'apple', 'silo_relay', 'sandbox',
			 'org.siloserver.silo', 'ciphertext', 'hash-3', 'server-disabled', 'private_push', false),
			('device-other-profile', 42, 'profile-2', 'local-other', 'apple', 'silo_relay', 'sandbox',
			 'org.siloserver.silo', 'ciphertext', 'hash-4', 'server-other', 'private_push', true)
	`); err != nil {
		t.Fatalf("seed push devices: %v", err)
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
		ID:          "delivery-request-1",
		UserID:      42,
		ProfileID:   "profile-1",
		Type:        DeliveryTypeRequestFulfilled,
		ReasonFlags: []byte(`{}`),
	}, OperationalDispatch{})
	if err != nil {
		t.Fatalf("dispatch operational: %v", err)
	}
	if inserted == nil || inserted.ID != "delivery-request-1" {
		t.Fatalf("inserted = %+v", inserted)
	}

	var deliveryID, pushDeviceID, triggerType, provider, platform, outcome string
	if err := pool.QueryRow(ctx, `
		SELECT notification_delivery_id, push_device_id, trigger_type, provider, platform, outcome
		FROM push_delivery_attempts
	`).Scan(&deliveryID, &pushDeviceID, &triggerType, &provider, &platform, &outcome); err != nil {
		t.Fatalf("query push attempt: %v", err)
	}
	if deliveryID != inserted.ID ||
		pushDeviceID != "device-private" ||
		triggerType != PushTriggerDelivery ||
		provider != PushProviderSiloRelay ||
		platform != PushPlatformApple ||
		outcome != PushOutcomePending {
		t.Fatalf("unexpected push attempt: delivery=%q device=%q trigger=%q provider=%q platform=%q outcome=%q",
			deliveryID, pushDeviceID, triggerType, provider, platform, outcome)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_delivery_attempts`).Scan(&count); err != nil {
		t.Fatalf("count push attempts: %v", err)
	}
	if count != 1 {
		t.Fatalf("push attempt count = %d, want 1", count)
	}
}
