package plugins

import (
	"context"
	"strings"
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
	ctx := t.Context()
	writer, installation := newConfigProvisionWriter(t, ProvisionPlugin{
		Version:     "1.2.3",
		Config:      map[string]map[string]any{"connection": {"label": "saved"}},
		AuthBinding: &ProvisionAuthBinding{CapabilityID: "login", Enabled: new(false)},
	})
	// The stand-in reports no provider while the declared binding is off.
	checks := 0
	RegisterSignInProviderGuard(func(ctx context.Context, tx pgx.Tx, _ int) error {
		checks++
		var on bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE(bool_or(enabled), false) FROM plugin_auth_bindings WHERE plugin_installation_id = $1`, installation.ID).Scan(&on); err != nil {
			return err
		}
		if !on {
			return ErrSignInProviderRequired
		}
		return nil
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
	// The warning names every way back, starting with the declaration.
	for _, way := range []string{"SILO_CONFIG or SILO_PLUGINS", "/login?local=1", "silo auth local-login enable"} {
		if !strings.Contains(SignInProviderWarning, way) {
			t.Errorf("warning does not name %q", way)
		}
	}

	// Putting the provider back in the declaration clears the warning.
	writer.Close()
	desired := writer.document.Plugins[tmdbID]
	desired.AuthBinding = &ProvisionAuthBinding{CapabilityID: "login", Enabled: new(true)}
	writer.document.Plugins[tmdbID] = desired
	if err := writer.StartOffline(ctx); err != nil {
		t.Fatalf("restored declaration: %v", err)
	}
	assertConfigProvisionStatus(t, writer, "applied", "")
}
