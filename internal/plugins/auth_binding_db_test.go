package plugins

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestUpsertAuthBindingAllowsOneEnabledProvider: a server has at most one
// enabled external sign-in binding; turning one off and another on works,
// and concurrent enables cannot both win.
func TestUpsertAuthBindingAllowsOneEnabledProvider(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	var others int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plugin_auth_bindings WHERE enabled`).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others > 0 {
		t.Skipf("%d enabled auth bindings already in the database", others)
	}
	configs := NewRuntimeConfigStore(pool, instanceStateTestCipher(t))
	first := seedResidentQueryInstallation(t, pool, true)
	time.Sleep(time.Millisecond) // distinct plugin ids
	second := seedResidentQueryInstallation(t, pool, true)

	if err := configs.UpsertAuthBinding(ctx, AuthBinding{InstallationID: first, CapabilityID: "oidc", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Rewriting the enabled binding itself is fine.
	if err := configs.UpsertAuthBinding(ctx, AuthBinding{InstallationID: first, CapabilityID: "oidc", Enabled: true, DisplayOrder: 2}); err != nil {
		t.Fatal(err)
	}
	if err := configs.UpsertAuthBinding(ctx, AuthBinding{InstallationID: second, CapabilityID: "ldap", Enabled: true}); !errors.Is(err, ErrAuthProviderAlreadyEnabled) {
		t.Fatalf("second enable = %v, want ErrAuthProviderAlreadyEnabled", err)
	}
	// A disabled binding may be stored next to the enabled one.
	if err := configs.UpsertAuthBinding(ctx, AuthBinding{InstallationID: second, CapabilityID: "ldap"}); err != nil {
		t.Fatal(err)
	}
	if err := configs.UpsertAuthBinding(ctx, AuthBinding{InstallationID: first, CapabilityID: "oidc"}); err != nil {
		t.Fatal(err)
	}

	// Both now off: two concurrent enables, exactly one wins.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, id := range []int{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			capability := "oidc"
			if id == second {
				capability = "ldap"
			}
			results[i] = configs.UpsertAuthBinding(ctx, AuthBinding{InstallationID: id, CapabilityID: capability, Enabled: true})
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrAuthProviderAlreadyEnabled):
			t.Fatalf("enable err = %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d concurrent enables won, want 1", won)
	}
	// New bindings default to account creation on.
	var autoProvision bool
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled)
		VALUES ($1, 'default-check', false) RETURNING auto_provision`, first).Scan(&autoProvision); err != nil || !autoProvision {
		t.Fatalf("auto_provision default = %v, %v", autoProvision, err)
	}
}
