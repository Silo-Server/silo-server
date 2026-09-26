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

func TestAdminPluginInstallationsRead(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 123456789, time.UTC)
	repo := 4
	available := "1.1.0"
	installation := func(id int, pluginID string) handlers.PluginInstallationView {
		return handlers.PluginInstallationView{ID: id, RepositoryID: &repo, PluginID: pluginID, Version: "1.0.0", InstallPath: "/plugins/" + pluginID, Enabled: true, Kind: plugins.KindPlugin, UpdatePolicy: "manual", AvailableVersion: &available, SourceKind: "silo", RepositoryName: "Official", UpdatesPaused: false, GlobalConfigs: []handlers.PluginConfigValueView{{Key: "account", Value: map[string]any{"region": "us-east"}, ConfiguredSecrets: []string{"api_key"}}}, AuthBindings: []handlers.PluginAuthBindingView{{CapabilityID: "oidc", Enabled: true, DisplayOrder: 2, CreatedAt: at, UpdatedAt: at}}, TaskBindings: []handlers.PluginTaskBindingView{{CapabilityID: "sync", Enabled: true, Trigger: map[string]any{"cron": "0 * * * *"}, CreatedAt: at, UpdatedAt: at}}, CreatedAt: at, UpdatedAt: at}
	}
	f := &fakePluginInventory{installations: []handlers.PluginInstallationView{installation(9, "org.example.b"), installation(3, "org.example.a")}}
	deps := pilotDeps(nil, nil)
	deps.AdminPluginInventory = f
	h := NewHandler(deps)
	path := Prefix + "/admin/plugins/installations"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.installCalls != 0 {
		t.Fatal("refusal read installations")
	}
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var body Collection[AdminPluginInstallation]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	item := body.Items[0]
	if rec.Code != 200 || len(body.Items) != 1 || item.ID != "3" || item.RepositoryID == nil || *item.RepositoryID != "4" || item.AvailableVersion != "1.1.0" || body.Page == nil || !body.Page.HasMore {
		t.Fatal(rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, want := range []string{`"value":{"region":"us-east"}`, `"configured_secrets":["api_key"]`, `"trigger":{"cron":"0 * * * *"}`, `"display_order":2`, `2026-09-01T00:00:00.123Z`, `"capabilities":[]`, `"metadata":{}`, `"runtime":{"resident":false,"state":"stopped","restart_count":0}`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	if strings.Contains(raw, "api_key\":") {
		t.Fatal("secret value leaked", raw)
	}
	cursor := url.QueryEscape(body.Page.NextCursor)
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"9"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)
	f.installations = nil
	if rec = do(t, h, "GET", path, "", bearer(adminToken)); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	builtin := installation(1, "silo.builtin")
	builtin.Kind = plugins.KindBuiltin
	f.installations = []handlers.PluginInstallationView{builtin}
	if rec = do(t, h, "GET", path, "", bearer(adminToken)); rec.Code != 500 {
		t.Fatal("builtin row must never be projected", rec.Code, rec.Body.String())
	}
	f.installations = nil
	f.err = errors.New("private database detail")
	if rec = do(t, h, "GET", path, "", bearer(adminToken)); rec.Code != 500 || strings.Contains(rec.Body.String(), "private database") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminPluginInventory = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}
