package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakePluginInventory struct {
	catalogCalls, installCalls int
	catalog                    []handlers.PluginCatalogEntryView
	installations              []handlers.PluginInstallationView
	err                        error
}

func (f *fakePluginInventory) ListAdminPluginCatalog(context.Context) ([]handlers.PluginCatalogEntryView, error) {
	f.catalogCalls++
	return f.catalog, f.err
}
func (f *fakePluginInventory) ListAdminPluginInstallations(context.Context) ([]handlers.PluginInstallationView, error) {
	f.installCalls++
	return f.installations, f.err
}

func syntheticCatalogEntry(pluginID, version string, repo int) handlers.PluginCatalogEntryView {
	return handlers.PluginCatalogEntryView{RepositoryID: repo, PluginID: pluginID, Version: version, ArchiveURL: "https://example.invalid/" + pluginID + ".zip", SourceKind: "external", RepositoryName: "Synthetic", Presentation: &handlers.PluginPresentationView{DisplayName: "Synthetic " + pluginID, SourceURL: "https://example.invalid/src"}, Capabilities: []handlers.PluginCapabilityView{{Type: "metadata_provider.v1", ID: "meta", DisplayName: "Meta", Metadata: map[string]any{"item_types": []any{"movie"}}}}, GlobalConfigSchema: []plugins.ConfigSchemaView{{Key: "account", Title: "Account", JSONSchema: `{"type":"object"}`, AdminForm: &plugins.AdminFormView{Fields: []plugins.AdminFormFieldView{{Key: "api_key", Label: "API key", Control: "password", Secret: true, DefaultValue: map[string]any{"nested": true}}}}}}, Metadata: map[string]any{"category": "Tools"}}
}

func TestAdminPluginCatalogRead(t *testing.T) {
	f := &fakePluginInventory{catalog: []handlers.PluginCatalogEntryView{syntheticCatalogEntry("org.example.zeta", "1.0.0", 9), syntheticCatalogEntry("org.example.alpha", "2.0.0", 2)}}
	deps := pilotDeps(nil, nil)
	deps.AdminPluginInventory = f
	h := NewHandler(deps)
	path := Prefix + "/admin/plugins/catalog"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.catalogCalls != 0 {
		t.Fatal("refusal fetched the catalog")
	}
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var body Collection[AdminPluginCatalogEntry]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Items) != 1 || body.Items[0].PluginID != "org.example.alpha" || body.Items[0].RepositoryID != "2" || body.Page == nil || !body.Page.HasMore {
		t.Fatal(rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, want := range []string{`"default_value":{"nested":true}`, `"metadata":{"category":"Tools"}`, `"item_types":["movie"]`, `"routes":[]`, `"assets":[]`, `"user_config_schema":[]`, `"secret":true`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	cursor := url.QueryEscape(body.Page.NextCursor)
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"plugin_id":"org.example.zeta"`) || strings.Contains(rec.Body.String(), `"has_more":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, "GET", path+"?limit=2&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	f.catalog = nil
	if rec = do(t, h, "GET", path, "", bearer(adminToken)); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.catalog = []handlers.PluginCatalogEntryView{syntheticCatalogEntry("a", "1", 1), syntheticCatalogEntry("a", "1", 2)}
	if rec = do(t, h, "GET", path, "", bearer(adminToken)); rec.Code != 500 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.err = errors.New("private index failure detail")
	if rec = do(t, h, "GET", path, "", bearer(adminToken)); rec.Code != 500 || strings.Contains(rec.Body.String(), "private index") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.err = &handlers.APIError{Status: 503, Code: "unavailable", Message: "Plugin service not configured"}
	requireProblem(t, do(t, h, "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
	deps.AdminPluginInventory = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

// TestAdminPluginAuthBindingURLs: an OAuth binding shows the callback and
// post-logout redirect URLs to register at the provider, built from the
// public URL; a password binding and a server without a public URL show
// none.
func TestAdminPluginAuthBindingURLs(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	installation := func(id int, capabilityID string, modes ...string) handlers.PluginInstallationView {
		return handlers.PluginInstallationView{ID: id, PluginID: "org.example." + capabilityID, Version: "1.0.0", Enabled: true, Kind: plugins.KindPlugin, UpdatePolicy: "manual", SourceKind: "silo",
			Capabilities: []handlers.PluginCapabilityView{{Type: "auth_provider.v1", ID: capabilityID, AuthModes: modes}},
			AuthBindings: []handlers.PluginAuthBindingView{{CapabilityID: capabilityID, Enabled: true, CreatedAt: at, UpdatedAt: at}}, CreatedAt: at, UpdatedAt: at}
	}
	f := &fakePluginInventory{installations: []handlers.PluginInstallationView{installation(3, "oidc", "oauth2"), installation(4, "ldap", "password")}}
	deps := pilotDeps(nil, nil)
	deps.AdminPluginInventory = f
	read := func(deps Dependencies) []AdminPluginInstallation {
		t.Helper()
		rec := do(t, NewHandler(deps), "GET", Prefix+"/admin/plugins/installations", "", bearer(adminToken))
		var body Collection[AdminPluginInstallation]
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Items) != 2 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		return body.Items
	}
	items := read(deps)
	oidc, ldap := items[0].AuthBindings[0], items[1].AuthBindings[0]
	if oidc.CallbackURL != "https://silo.example.test/api/v2/auth/oauth/3/callback" || oidc.PostLogoutRedirectURL != "https://silo.example.test/login" {
		t.Fatalf("oidc binding = %+v", oidc)
	}
	if ldap.CallbackURL != "" || ldap.PostLogoutRedirectURL != "" {
		t.Fatalf("ldap binding = %+v", ldap)
	}
	deps.OAuth = noPublicURLOAuth{fakeOAuth{}}
	if got := read(deps)[0].AuthBindings[0]; got.CallbackURL != "" || got.PostLogoutRedirectURL != "" {
		t.Fatalf("without a public URL = %+v", got)
	}
}

// noPublicURLOAuth is fakeOAuth on a server without a public URL.
type noPublicURLOAuth struct{ fakeOAuth }

func (noPublicURLOAuth) PostLogoutRedirectURL() string { return "" }

func (noPublicURLOAuth) NativeSignInAvailable() bool { return false }
