package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

func TestAdminPluginProvisioningStatusRequiresActingAdmin(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	status := plugins.ProvisionStatus{PluginID: "silo.auth.oidc", Version: "0.1.2", State: "held", Detail: "A restore withheld plugin authority."}
	calls := 0
	deps.AdminPluginProvisionStatus = func(context.Context) ([]plugins.ProvisionStatus, error) {
		calls++
		return []plugins.ProvisionStatus{status}, nil
	}
	h := NewHandler(deps)
	path := Prefix + "/admin/plugins/provisioning-status"
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
	if calls != 0 {
		t.Fatalf("status store called %d times before authorization", calls)
	}

	// No installation service is required to report a declaration that has
	// not created an installation.
	rec := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	var body Collection[AdminPluginProvisionStatus]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String(), err)
	}
	if len(body.Items) != 1 || body.Items[0] != AdminPluginProvisionStatus(status) || body.Page != nil {
		t.Fatalf("unexpected provisioning status: %+v", body)
	}
}

func TestAdminPluginProvisioningOwnershipError(t *testing.T) {
	err := fmt.Errorf("save plugin config: %w", &plugins.ManagedByEnvironmentError{Field: "config.connection.client_secret"})
	p, ok := errors.AsType[*Problem](adminPluginMutationProblem(err))
	if !ok || p.Type != TypeValidationFailed.URI() || p.Status != http.StatusUnprocessableEntity {
		t.Fatalf("problem = %+v, want 422 validation_failed", p)
	}
	if len(p.Errors) != 1 || p.Errors[0].Code != codeManagedByEnvironment || p.Errors[0].Location != "body.config.connection.client_secret" {
		t.Fatalf("errors = %+v, want managed_by_environment for the owned config field", p.Errors)
	}
}

func TestAdminPluginProvisioningOwnershipProjection(t *testing.T) {
	view := handlers.PluginInstallationView{
		ID:           4,
		PluginID:     "silo.auth.oidc",
		Capabilities: []handlers.PluginCapabilityView{{Type: "auth_provider.v1", ID: "oidc"}},
		ProvisionOwnership: plugins.ProvisionOwnership{
			Managed:       true,
			VersionPin:    "0.1.2",
			ConfigFields:  map[string][]string{"connection": {"client_secret"}},
			BindingFields: map[string][]string{"oidc": {"enabled", "callback_mode"}},
		},
	}
	var reg Registry
	row, err := reg.adminPluginInstallationOf(view)
	if err != nil || row.EnvManaged == nil {
		t.Fatalf("installation ownership = %+v, error = %v", row.EnvManaged, err)
	}
	if row.EnvManaged.VersionPin != "0.1.2" ||
		!slices.Equal(row.EnvManaged.ConfigFields["connection"], []string{"client_secret"}) ||
		!slices.Equal(row.Capabilities[0].EnvManaged, []string{"enabled", "callback_mode"}) ||
		len(row.AuthBindings) != 0 {
		t.Fatalf("ownership projection without a saved binding = %+v, capabilities = %+v", row.EnvManaged, row.Capabilities)
	}
}
