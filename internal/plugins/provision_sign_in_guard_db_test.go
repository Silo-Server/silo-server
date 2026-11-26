package plugins

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestProvisionAppliesDeclarationThatLeavesNoSignInProviderDB: provisioning
// applies the operator's declared configuration at startup. A declaration
// that leaves password sign-in off with no provider people can sign in with
// is applied, not refused (refusing would leave the server unable to
// converge on its own configuration), and the provisioning status of the
// declared sign-in plugin says so. The auth package's rule is stood in for:
// every check here reports no provider.
func TestProvisionAppliesDeclarationThatLeavesNoSignInProviderDB(t *testing.T) {
	previous := signInProviderGuard
	t.Cleanup(func() { RegisterSignInProviderGuard(previous) })
	checks := 0
	RegisterSignInProviderGuard(func(context.Context, pgx.Tx, int) error {
		checks++
		return ErrSignInProviderRequired
	})

	ctx := t.Context()
	writer, installation := newConfigProvisionWriter(t, ProvisionPlugin{
		Version:     "1.2.3",
		Config:      map[string]map[string]any{"connection": {"label": "saved"}},
		AuthBinding: &ProvisionAuthBinding{CapabilityID: "login", Enabled: new(false)},
	})
	if err := writer.StartOffline(ctx); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	if checks == 0 {
		t.Fatal("provisioning did not check for a sign-in provider")
	}
	assertConfigProvisionStatus(t, writer, "applied", SignInProviderWarning)
	binding, err := writer.configs.GetAuthBinding(ctx, installation.ID, "login")
	if err != nil || binding.Enabled {
		t.Fatalf("declared binding not applied: %+v, %v", binding, err)
	}
}
