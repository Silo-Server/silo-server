package plugins

import (
	"context"
	"strings"
	"testing"
)

// ListGlobalConfigKeys answers "has an operator configured this plugin" without
// handing over the configuration, which is the whole reason it stops at the key and
// never decodes a value. The seeded value is credential-shaped so that a leak fails
// an assertion here rather than needing a reviewer to notice it.
func TestListGlobalConfigKeysReturnsNamesOnly(t *testing.T) {
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

	keys, err := store.ListGlobalConfigKeys(ctx, installationID)
	if err != nil {
		t.Fatalf("ListGlobalConfigKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "account" {
		t.Fatalf("keys = %v, want [account]", keys)
	}
	for _, key := range keys {
		if strings.Contains(key, secret) {
			t.Fatalf("key %q carries the stored value", key)
		}
	}

	// The answer is per installation: another installation's configuration must not
	// make this one look configured.
	other := seedResidentQueryInstallation(t, pool, true)
	otherKeys, err := store.ListGlobalConfigKeys(ctx, other)
	if err != nil {
		t.Fatalf("ListGlobalConfigKeys(other): %v", err)
	}
	if len(otherKeys) != 0 {
		t.Fatalf("other installation keys = %v, want none", otherKeys)
	}
}
