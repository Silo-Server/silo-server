package apiv2

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"strings"
	"testing"
)

type fakeAdminMarkerProviders struct {
	calls    int
	provider string
	update   handlers.MarkerProviderUpdate
	invalid  bool
}

func (f *fakeAdminMarkerProviders) ListMarkerProviders(context.Context) ([]handlers.MarkerProviderConfigView, error) {
	return []handlers.MarkerProviderConfigView{{Provider: "plugin:7:markers", PluginInstallationID: 7, FetchEnabled: true, ContributeMinConfidence: 0.8}}, nil
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
func TestAdminMarkerProvidersTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminMarkerProviders{}
	deps.AdminMarkerProviders = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/markers/providers"
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"plugin_installation_id":"7"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "PUT", path+"/plugin%3A7%3Amarkers", `{"fetch_enabled":false,"fetch_priority":0,"contribute_min_confidence":null}`, bearer(adminToken))
	if rec.Code != 200 || f.provider != "plugin:7:markers" || f.update.FetchEnabled == nil || *f.update.FetchEnabled || f.update.FetchPriority == nil || *f.update.FetchPriority != 0 || f.update.ContributeMinConfidence != nil {
		t.Fatalf("update: %d %s %+v", rec.Code, rec.Body, f)
	}
	before := f.calls
	rec = do(t, h, "PUT", path+"/p", `{"contribute_min_confidence":1.1}`, bearer(adminToken))
	if rec.Code != 422 || f.calls != before {
		t.Fatalf("invalid: %d %s", rec.Code, rec.Body)
	}
	for _, method := range []string{"PUT", "POST"} {
		suffix := "/p"
		body := `{}`
		if method == "POST" {
			suffix += "/validate"
			body = ""
		}
		rec = do(t, h, method, path+suffix, body, bearer(memberToken))
		if rec.Code != 403 || f.calls != before {
			t.Fatalf("member: %d", rec.Code)
		}
	}
	rec = do(t, h, "POST", path+"/plugin%3A7%3Amarkers/validate", "", bearer(adminToken))
	if rec.Code != 200 || f.provider != "plugin:7:markers" || !strings.Contains(rec.Body.String(), `"acceptance_rate":0.8`) {
		t.Fatalf("validate: %d %s", rec.Code, rec.Body)
	}
	f.invalid = true
	rec = do(t, h, "POST", path+"/p/validate", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"valid":false`) || strings.Contains(rec.Body.String(), "private provider") {
		t.Fatalf("failure: %d %s", rec.Code, rec.Body)
	}
}
func adminCatalogMarkerProvidersFixtureCases() []fixtureCase {
	out := []fixtureCase{}
	for _, c := range []struct{ name, id, method, path, body, schema string }{{"list", "listAdminMarkerProviders", "GET", "", "", "AdminMarkerProviders"}, {"update", "updateAdminMarkerProvider", "PUT", "/plugin%3A7%3Amarkers", `{"fetch_enabled":false}`, "AdminMarkerProvider"}, {"validate", "validateAdminMarkerProvider", "POST", "/plugin%3A7%3Amarkers/validate", "", "AdminMarkerProviderValidation"}} {
		out = append(out, fixtureCase{name: "admin_marker_providers_" + c.name, operationID: c.id, method: c.method, path: Prefix + "/admin/markers/providers" + c.path, body: c.body, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/" + c.schema, assertHeaders: []string{"Content-Type"}, scenario: "Provider administration preserves synchronous result and partial configuration semantics."})
	}
	return out
}
