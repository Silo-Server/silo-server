package migrations

import (
	"strings"
	"testing"
)

func TestPushDevicesMigrationContract(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260701143000_push_devices.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(migrationBytes)
	for _, want := range []string{
		"CREATE TABLE public.push_devices",
		"CONSTRAINT push_devices_profile_device_platform_key UNIQUE (profile_id, device_id, platform)",
		"CONSTRAINT push_devices_server_device_id_key UNIQUE (server_device_id)",
		"CONSTRAINT push_devices_platform_check CHECK (platform IN ('apple'))",
		"CONSTRAINT push_devices_provider_check CHECK (provider IN ('silo_relay'))",
		"CONSTRAINT push_devices_apns_environment_check CHECK (apns_environment IN ('production', 'sandbox'))",
		"CONSTRAINT push_devices_push_mode_check CHECK (push_mode IN ('off', 'in_app_only', 'private_push'))",
		"CONSTRAINT push_devices_apple_fields_check CHECK",
		"CREATE INDEX push_devices_profile_enabled_idx ON public.push_devices (profile_id) WHERE enabled",
	} {
		if !strings.Contains(migration, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
	if strings.Contains(migration, "REFERENCES profiles") {
		t.Fatal("push_devices migration must not add a profile foreign key")
	}
}

func TestPushDeliveryAttemptsMigrationContract(t *testing.T) {
	migrationBytes, err := FS.ReadFile("sql/20260701170000_push_delivery_attempts.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migration := string(migrationBytes)
	for _, want := range []string{
		"CREATE TABLE public.push_delivery_attempts",
		"notification_delivery_id text REFERENCES public.notification_deliveries(id) ON DELETE CASCADE",
		"push_device_id text NOT NULL REFERENCES public.push_devices(id) ON DELETE CASCADE",
		"CONSTRAINT push_delivery_attempts_trigger_check CHECK (trigger_type IN ('delivery', 'test'))",
		"CONSTRAINT push_delivery_attempts_delivery_required_check CHECK",
		"CONSTRAINT push_delivery_attempts_provider_check CHECK (provider IN ('silo_relay'))",
		"CONSTRAINT push_delivery_attempts_platform_check CHECK (platform IN ('apple'))",
		"CONSTRAINT push_delivery_attempts_outcome_check CHECK (outcome IN ('pending', 'delivered', 'retrying', 'failed'))",
		"CREATE UNIQUE INDEX push_delivery_attempts_delivery_unique",
		"CREATE INDEX push_delivery_attempts_retry_idx",
		"CREATE INDEX push_delivery_attempts_device_history_idx",
	} {
		if !strings.Contains(migration, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
	if strings.Contains(migration, "REFERENCES profiles") {
		t.Fatal("push delivery attempts migration must not add a profile foreign key")
	}
}
