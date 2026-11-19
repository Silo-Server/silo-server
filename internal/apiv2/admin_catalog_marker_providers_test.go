package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminMarkerProviders struct {
	calls    int
	provider string
	update   handlers.MarkerProviderUpdate
	invalid  bool
}

func (f *fakeAdminMarkerProviders) ListMarkerProviders(context.Context) ([]handlers.MarkerProviderConfigView, error) {
	return []handlers.MarkerProviderConfigView{{Provider: "plugin:7:markers", PluginInstallationID: 7, FetchEnabled: true, FetchMinConfidence: 0.4, ContributeMinConfidence: 0.8}}, nil
}
func (f *fakeAdminMarkerProviders) UpdateMarkerProvider(_ context.Context, p string, b handlers.MarkerProviderUpdate) (handlers.MarkerProviderConfigView, error) {
	f.calls++
	f.provider = p
	f.update = b
	return handlers.MarkerProviderConfigView{Provider: p}, nil
}
func (f *fakeAdminMarkerProviders) ValidateMarkerProvider(_ context.Context, p string) (handlers.MarkerProviderValidationView, error) {
	f.calls++
	f.provider = p
	if f.invalid {
		return handlers.MarkerProviderValidationView{Error: "private provider response"}, nil
	}
	return handlers.MarkerProviderValidationView{Valid: true, Stats: &handlers.MarkerUserStatsView{Total: 5, Accepted: 4, AcceptanceRate: 0.8}}, nil
}
func adminCatalogMarkerProvidersFixtureCases() []fixtureCase {
	out := []fixtureCase{}
	for _, c := range []struct{ name, id, method, path, body, schema string }{{"list", "listAdminMarkerProviders", "GET", "", "", "AdminMarkerProviders"}, {"update", "updateAdminMarkerProvider", "PUT", "/plugin%3A7%3Amarkers", `{"fetch_enabled":false}`, "AdminMarkerProvider"}, {"validate", "validateAdminMarkerProvider", "POST", "/plugin%3A7%3Amarkers/validate", "", "AdminMarkerProviderValidation"}} {
		out = append(out, fixtureCase{name: "admin_marker_providers_" + c.name, operationID: c.id, method: c.method, path: Prefix + "/admin/markers/providers" + c.path, body: c.body, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/" + c.schema, assertHeaders: []string{"Content-Type"}, scenario: "Provider administration preserves synchronous result and partial configuration semantics."})
	}
	return out
}
