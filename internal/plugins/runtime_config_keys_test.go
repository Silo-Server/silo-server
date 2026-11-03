package plugins

import (
	"context"
	"strings"
	"testing"
)

// configuredKeysFor reads one installation's configured global config keys through the
// batched form, which is the one the user-facing settings list calls.
func configuredKeysFor(t *testing.T, store *RuntimeConfigStore, installationID int) []string {
	t.Helper()
	byInstallation, err := store.ListConfiguredGlobalConfigKeysForInstallations(context.Background(), []int{installationID})
	if err != nil {
		t.Fatalf("ListConfiguredGlobalConfigKeysForInstallations: %v", err)
	}
	return byInstallation[installationID]
}

// The answer decides whether a screen may offer a feature that needs an operator's
// credential, so an entry that has been cleared must not count. The seeded value is
// credential-shaped so a leak fails an assertion here rather than needing a reviewer to
// notice it, and only keys are ever returned.
func TestListConfiguredGlobalConfigKeysCountsOnlyFilledInEntries(t *testing.T) {
	const secret = "super-secret-api-key-value"

	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	store := NewRuntimeConfigStore(pool, instanceStateTestCipher(t))
	installationID := seedResidentQueryInstallation(t, pool, true)

	if err := store.PutGlobalConfig(ctx, installationID, "account", map[string]any{
		"api_key": secret,
	}); err != nil {
		t.Fatalf("seed global config: %v", err)
	}

	keys := configuredKeysFor(t, store, installationID)
	if len(keys) != 1 || keys[0] != "account" {
		t.Fatalf("keys = %v, want [account]", keys)
	}
	for _, key := range keys {
		if strings.Contains(key, secret) {
			t.Fatalf("key %q carries the stored value", key)
		}
	}

	// An operator clearing the credential leaves the row behind. A caller reading
	// presence would keep the dependent setting on offer with nothing behind it.
	if err := store.PutGlobalConfig(ctx, installationID, "account", map[string]any{
		"api_key": "",
	}); err != nil {
		t.Fatalf("clear global config: %v", err)
	}

	cleared := configuredKeysFor(t, store, installationID)
	if len(cleared) != 0 {
		t.Fatalf("keys after clearing = %v, want none", cleared)
	}

	// The answer is per installation: another installation's configuration must not make
	// this one look configured, and one call covering both must keep them apart.
	other := seedResidentQueryInstallation(t, pool, true)
	if err := store.PutGlobalConfig(ctx, other, "region", map[string]any{
		"region": "us-east",
	}); err != nil {
		t.Fatalf("seed other global config: %v", err)
	}

	both, err := store.ListConfiguredGlobalConfigKeysForInstallations(ctx, []int{installationID, other})
	if err != nil {
		t.Fatalf("ListConfiguredGlobalConfigKeysForInstallations: %v", err)
	}
	if len(both[installationID]) != 0 {
		t.Fatalf("keys = %v, want none for the installation whose entry was cleared", both[installationID])
	}
	if len(both[other]) != 1 || both[other][0] != "region" {
		t.Fatalf("other installation keys = %v, want [region]", both[other])
	}

	// An installation that holds nothing at all is absent rather than present and empty.
	empty := seedResidentQueryInstallation(t, pool, true)
	both, err = store.ListConfiguredGlobalConfigKeysForInstallations(ctx, []int{installationID, empty})
	if err != nil {
		t.Fatalf("ListConfiguredGlobalConfigKeysForInstallations: %v", err)
	}
	if _, present := both[empty]; present {
		t.Fatalf("installation with no stored entry is present in %v", both)
	}
}

// A cleared entry also arrives as an empty object, which is the shape a form that drops
// blank fields leaves behind.
func TestListConfiguredGlobalConfigKeysTreatsAnEmptyObjectAsUnset(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	store := NewRuntimeConfigStore(pool, instanceStateTestCipher(t))
	installationID := seedResidentQueryInstallation(t, pool, true)

	if err := store.PutGlobalConfig(ctx, installationID, "account", map[string]any{}); err != nil {
		t.Fatalf("seed empty global config: %v", err)
	}

	keys := configuredKeysFor(t, store, installationID)
	if len(keys) != 0 {
		t.Fatalf("keys = %v, want none for an entry holding nothing", keys)
	}
}

// Whitespace is what a form field left with a stray space holds, and it is not a
// credential either.
func TestListConfiguredGlobalConfigKeysTreatsBlankStringsAsUnset(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	store := NewRuntimeConfigStore(pool, instanceStateTestCipher(t))
	installationID := seedResidentQueryInstallation(t, pool, true)

	if err := store.PutGlobalConfig(ctx, installationID, "account", map[string]any{
		"api_key": "   ",
	}); err != nil {
		t.Fatalf("seed blank global config: %v", err)
	}

	keys := configuredKeysFor(t, store, installationID)
	if len(keys) != 0 {
		t.Fatalf("keys = %v, want none for a blank entry", keys)
	}
}

// One unreadable entry must not hide any other plugin's configured state. The settings
// list reads every eligible installation in one call, so a row the cipher cannot open
// has to stay that installation's problem: reporting the whole batch as unconfigured
// would take a working plugin's dependent setting off offer.
func TestListConfiguredGlobalConfigKeysIsolatesAnUnreadableEntry(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	store := NewRuntimeConfigStore(pool, instanceStateTestCipher(t))

	healthy := seedResidentQueryInstallation(t, pool, true)
	if err := store.PutGlobalConfig(ctx, healthy, "account", map[string]any{
		"api_key": "key-value",
	}); err != nil {
		t.Fatalf("seed global config: %v", err)
	}

	// A value the server cannot open: the encrypted-entry shaper with nothing
	// encrypted inside it, which is what a truncated write leaves behind.
	damaged := seedResidentQueryInstallation(t, pool, true)
	if _, err := pool.Exec(ctx, `
		INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
		VALUES ($1, 'account', $2::jsonb)
	`, damaged, `{"__silo_encrypted_runtime_config_v1":"not-an-envelope"}`); err != nil {
		t.Fatalf("seed unreadable global config: %v", err)
	}

	byInstallation, err := store.ListConfiguredGlobalConfigKeysForInstallations(ctx, []int{healthy, damaged})
	if err != nil {
		t.Fatalf("one unreadable entry failed the batch: %v", err)
	}
	if len(byInstallation[healthy]) != 1 || byInstallation[healthy][0] != "account" {
		t.Fatalf("readable installation keys = %v, want [account]", byInstallation[healthy])
	}
	if len(byInstallation[damaged]) != 0 {
		t.Fatalf("unreadable installation keys = %v, want none", byInstallation[damaged])
	}

	// The unreadable entry does not spoil a later read either: the installation is
	// unconfigured on its own account, not poisoned for every caller.
	alone, err := store.ListConfiguredGlobalConfigKeysForInstallations(ctx, []int{healthy})
	if err != nil {
		t.Fatalf("ListConfiguredGlobalConfigKeysForInstallations: %v", err)
	}
	if len(alone[healthy]) != 1 {
		t.Fatalf("keys after a damaged batch = %v, want [account]", alone[healthy])
	}
}
