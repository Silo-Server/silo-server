package migrations

import (
	"strings"
	"testing"
)

// TestUserSettingValuesMigrationContract pins the parts of the canonical
// settings storage that the store code and the design both depend on: the five
// original scope CHECK/index identities, and the covering indexes the one-query
// read path needs. TestProfileClientSettingsMigrationContract pins the sixth
// identity added by the later extension migration.
// A silent edit to any of them would not fail a store test until a duplicate row
// or a sequential-scan regression reached production.
func TestUserSettingValuesMigrationContract(t *testing.T) {
	migration := readMigration(t, "sql/20260727010621_user_setting_values.sql")

	for _, want := range []string{
		"CREATE TABLE public.user_setting_values",
		"value       jsonb NOT NULL",
		"revision    bigint NOT NULL DEFAULT 1",
		"CONSTRAINT user_setting_values_scope_check\n        CHECK (scope IN ('account', 'profile', 'profile_device', 'profile_library', 'profile_series'))",
		"(scope = 'account' AND profile_id IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL)",
		"(scope = 'profile' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL)",
		"(scope = 'profile_device' AND profile_id IS NOT NULL AND device_id IS NOT NULL AND library_id IS NULL AND series_id IS NULL)",
		"(scope = 'profile_library' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NOT NULL AND series_id IS NULL)",
		"(scope = 'profile_series' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NOT NULL)",

		// The cascades that exist today, and only those.
		"CONSTRAINT user_setting_values_user_id_fkey\n        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE",
		"CONSTRAINT user_setting_values_profile_fkey\n        FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE",

		// One explicit value per identity.
		"CREATE UNIQUE INDEX user_setting_values_account_uq\n  ON public.user_setting_values (user_id, key) WHERE scope = 'account'",
		"CREATE UNIQUE INDEX user_setting_values_profile_uq\n  ON public.user_setting_values (user_id, profile_id, key) WHERE scope = 'profile'",
		"CREATE UNIQUE INDEX user_setting_values_profile_device_uq\n  ON public.user_setting_values (user_id, profile_id, device_id, key) WHERE scope = 'profile_device'",
		"CREATE UNIQUE INDEX user_setting_values_profile_library_uq\n  ON public.user_setting_values (user_id, profile_id, library_id, key) WHERE scope = 'profile_library'",
		"CREATE UNIQUE INDEX user_setting_values_profile_series_uq\n  ON public.user_setting_values (user_id, profile_id, series_id, key) WHERE scope = 'profile_series'",

		// The hot read path.
		"ON public.user_setting_values (user_id, profile_id, key, scope)",
		"ON public.user_setting_values (user_id, profile_id, series_id)",
		"ON public.user_setting_values (user_id, profile_id, library_id)",

		// Idempotency and the inert migration audit table.
		"CREATE TABLE public.user_setting_mutations",
		"CONSTRAINT user_setting_mutations_pkey PRIMARY KEY (user_id, mutation_id)",
		"request_hash text NOT NULL",
		"expires_at   timestamptz NOT NULL",
		"ON public.user_setting_mutations (expires_at)",
		"CREATE TABLE public.user_setting_migration_rejects",
	} {
		if !strings.Contains(migration, want) {
			t.Fatalf("migration missing %q", want)
		}
	}

	// Library, series and device identity columns must stay reference-free: the
	// per-user SQLite store has no foreign keys at all, so inheriting cleanup
	// from constraints here would let the two backends drift.
	for _, forbidden := range []string{
		"REFERENCES public.library_folders",
		"REFERENCES public.media_items",
		"REFERENCES public.user_devices",
	} {
		if strings.Contains(migration, forbidden) {
			t.Fatalf("migration must not add %q; delete behavior is application-enforced", forbidden)
		}
	}
}

func TestProfileClientSettingsMigrationContract(t *testing.T) {
	migration := readMigration(t, "sql/20260803191207_add_profile_client_settings_scope.sql")

	for _, want := range []string{
		"ADD COLUMN client_family text",
		"CHECK (client_family IS NULL OR client_family IN ('tv', 'mobile', 'tablet', 'desktop', 'web'))",
		"CHECK (scope IN ('account', 'profile', 'profile_client', 'profile_device', 'profile_library', 'profile_series'))",
		"(scope = 'profile_client' AND profile_id IS NOT NULL AND client_family IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL)",
		"CREATE UNIQUE INDEX user_setting_values_profile_client_uq\n  ON public.user_setting_values (user_id, profile_id, client_family, key)",

		// Legacy pins are copied only when the new profile row is absent. Blank
		// labels never become values that the new contract would reject, and the
		// old row is not deleted.
		"current.key = 'nav.shortcuts'",
		"CREATE FUNCTION pg_temp.decode_legacy_sidebar_pins(candidate jsonb)",
		"jsonb_typeof(candidate) = 'string'",
		"decoded := (candidate #>> '{}')::jsonb",
		"EXCEPTION WHEN invalid_text_representation",
		"pg_temp.decode_legacy_sidebar_pins(value_row.value)",
		"WHEN groups.group_key ~ '^[1-9][0-9]{0,9}$' THEN",
		"WHEN groups.group_key::bigint <= 2147483647 THEN groups.group_key::integer",
		"group_row.library_id IS NOT NULL",
		"CASE WHEN jsonb_typeof(group_row.pins) = 'array' THEN group_row.pins ELSE '[]'::jsonb END",
		"jsonb_typeof(pin.pin_value) = 'object'",
		"jsonb_typeof(pin.pin_value->'id') = 'string'",
		"jsonb_typeof(pin.pin_value->'label') = 'string'",
		"pin.pin_value->>'id' ~ '[^[:space:]]'",
		"pin.pin_value->>'label' ~ '[^[:space:]]'",
		"ON CONFLICT (user_id, profile_id, key) WHERE scope = 'profile' DO NOTHING",
	} {
		if !strings.Contains(migration, want) {
			t.Fatalf("profile_client migration missing %q", want)
		}
	}

	if strings.Contains(migration, "DELETE FROM public.user_setting_values WHERE key = 'ui.sidebar_pins'") {
		t.Fatal("profile_client migration deletes the legacy sidebar pins it must preserve")
	}
	if strings.Contains(migration, "999999999") || strings.Contains(migration, "{0,8}") {
		t.Fatal("profile_client migration retains the obsolete nine-digit library id cap")
	}
}

func readMigration(t *testing.T, path string) string {
	t.Helper()
	contents, err := FS.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", path, err)
	}
	return string(contents)
}
