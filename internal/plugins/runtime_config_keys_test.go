package plugins

import (
	"context"
	"strings"
	"testing"
)

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

	keys, err := store.ListConfiguredGlobalConfigKeys(ctx, installationID)
	if err != nil {
		t.Fatalf("ListConfiguredGlobalConfigKeys: %v", err)
	}
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

	cleared, err := store.ListConfiguredGlobalConfigKeys(ctx, installationID)
	if err != nil {
		t.Fatalf("ListConfiguredGlobalConfigKeys after clearing: %v", err)
	}
	if len(cleared) != 0 {
		t.Fatalf("keys after clearing = %v, want none", cleared)
	}

	// The answer is per installation: another installation's configuration must not make
	// this one look configured.
	other := seedResidentQueryInstallation(t, pool, true)
	otherKeys, err := store.ListConfiguredGlobalConfigKeys(ctx, other)
	if err != nil {
		t.Fatalf("ListConfiguredGlobalConfigKeys(other): %v", err)
	}
	if len(otherKeys) != 0 {
		t.Fatalf("other installation keys = %v, want none", otherKeys)
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

	keys, err := store.ListConfiguredGlobalConfigKeys(ctx, installationID)
	if err != nil {
		t.Fatalf("ListConfiguredGlobalConfigKeys: %v", err)
	}
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

	keys, err := store.ListConfiguredGlobalConfigKeys(ctx, installationID)
	if err != nil {
		t.Fatalf("ListConfiguredGlobalConfigKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("keys = %v, want none for a blank entry", keys)
	}
}
