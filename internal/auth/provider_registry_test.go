package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakeRegistryStores struct {
	bindings      []*plugins.AuthBinding
	installations map[int]*plugins.Installation
	capabilities  map[int][]*plugins.Capability
	configs       map[int][]*plugins.RuntimeConfig
	listErr       error
	getErr        error
	capErr        error
}

func (f *fakeRegistryStores) ListAuthBindings(context.Context) ([]*plugins.AuthBinding, error) {
	return f.bindings, f.listErr
}

func (f *fakeRegistryStores) GetByID(_ context.Context, id int) (*plugins.Installation, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	installation, ok := f.installations[id]
	if !ok {
		return nil, plugins.ErrInstallationNotFound
	}
	return installation, nil
}

func (f *fakeRegistryStores) ListCapabilities(_ context.Context, id int) ([]*plugins.Capability, error) {
	if f.capErr != nil {
		return nil, f.capErr
	}
	return f.capabilities[id], nil
}

func (f *fakeRegistryStores) ListGlobalConfigs(_ context.Context, id int) ([]*plugins.RuntimeConfig, error) {
	return f.configs[id], nil
}

func TestPluginProviderRegistryRebuild(t *testing.T) {
	stores := &fakeRegistryStores{
		bindings: []*plugins.AuthBinding{
			{InstallationID: 3, CapabilityID: "oidc", Enabled: true, AutoProvision: true},
			{InstallationID: 4, CapabilityID: "ldap", Enabled: false},
			{InstallationID: 5, CapabilityID: "gone", Enabled: true},
		},
		installations: map[int]*plugins.Installation{3: {ID: 3, Enabled: true}, 4: {ID: 4, Enabled: true}},
		capabilities: map[int][]*plugins.Capability{3: {{Type: "auth_provider.v1", ID: "oidc", Metadata: map[string]any{
			"display_name": "Manifest name", "auth_modes": []any{"oauth2"},
		}}}, 4: {{Type: "auth_provider.v1", ID: "ldap"}}},
		configs: map[int][]*plugins.RuntimeConfig{3: {
			{Key: "display_name", Value: map[string]any{"value": "Company SSO"}},
			{Key: "icon_url_path", Value: map[string]any{"value": "/logo.svg"}},
		}},
	}
	registry := NewPluginProviderRegistry(PluginProviderRegistryConfig{Bindings: stores, Installations: stores, GlobalConfigs: stores})
	if got := registry.Providers(); len(got) != 0 {
		t.Fatalf("before rebuild = %+v", got)
	}
	if err := registry.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := registry.Providers()
	if len(got) != 1 {
		t.Fatalf("providers = %+v", got)
	}
	info := got[0].Info
	if info.ID != "plugin:3:oidc" || info.DisplayName != "Company SSO" || info.Mode != "oauth" ||
		info.IconURL != plugins.ContentPrefix+"/plugins/3/assets/logo.svg" || info.InstallationID != 3 {
		t.Fatalf("info = %+v", info)
	}
	if pp, ok := got[0].Provider.(*PluginProvider); !ok || !pp.config.AutoProvision {
		t.Fatalf("provider = %#v", got[0].Provider)
	}

	// Switching providers without a restart: the OIDC binding goes off, the
	// LDAP one comes on.
	stores.bindings[0].Enabled = false
	stores.bindings[1].Enabled = true
	if err := registry.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	got = registry.Providers()
	if len(got) != 1 || got[0].Info.ID != "plugin:4:ldap" || got[0].Info.Mode != "credentials" || got[0].Info.DisplayName != "ldap" {
		t.Fatalf("after switch = %+v", got)
	}

	// A disabled installation offers no provider.
	stores.installations[4].Enabled = false
	_ = registry.Rebuild(context.Background())
	if got := registry.Providers(); len(got) != 0 {
		t.Fatalf("disabled installation = %+v", got)
	}

	// A failed read keeps the previous list.
	stores.installations[4].Enabled = true
	_ = registry.Rebuild(context.Background())
	stores.listErr = errors.New("database down")
	if err := registry.Rebuild(context.Background()); err == nil {
		t.Fatal("rebuild error swallowed")
	}
	if got := registry.Providers(); len(got) != 1 {
		t.Fatalf("after failed rebuild = %+v", got)
	}
}

// TestPluginProviderRegistryRebuildKeepsSetOnReadErrors: a transient failure
// reading an installation or its capabilities fails the rebuild, so the
// previous set stays, instead of dropping the provider or turning an OAuth
// provider into a credentials one. A missing installation is still skipped.
func TestPluginProviderRegistryRebuildKeepsSetOnReadErrors(t *testing.T) {
	stores := &fakeRegistryStores{
		bindings:      []*plugins.AuthBinding{{InstallationID: 3, CapabilityID: "oidc", Enabled: true}},
		installations: map[int]*plugins.Installation{3: {ID: 3, Enabled: true}},
		capabilities: map[int][]*plugins.Capability{3: {{Type: "auth_provider.v1", ID: "oidc", Metadata: map[string]any{
			"display_name": "Company SSO", "auth_modes": []any{"oauth2"},
		}}}},
	}
	registry := NewPluginProviderRegistry(PluginProviderRegistryConfig{Bindings: stores, Installations: stores})
	if err := registry.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := registry.Providers()
	if len(want) != 1 || want[0].Info.Mode != ProviderModeOAuth {
		t.Fatalf("providers = %+v", want)
	}

	for name, set := range map[string]func(error){
		"installation": func(err error) { stores.getErr = err },
		"capabilities": func(err error) { stores.capErr = err },
	} {
		set(errors.New("connection reset"))
		if err := registry.Rebuild(context.Background()); err == nil {
			t.Fatalf("%s read error: rebuild succeeded", name)
		}
		got := registry.Providers()
		if len(got) != 1 || got[0].Info.Mode != ProviderModeOAuth || got[0].Info.DisplayName != "Company SSO" {
			t.Fatalf("%s read error: providers = %+v, want the previous set", name, got)
		}
		set(nil)
	}

	stores.getErr = plugins.ErrInstallationNotFound
	if err := registry.Rebuild(context.Background()); err != nil {
		t.Fatalf("missing installation: %v", err)
	}
	if got := registry.Providers(); len(got) != 0 {
		t.Fatalf("missing installation: providers = %+v", got)
	}
}

func TestPluginProviderRegistryRejectsMissingCapability(t *testing.T) {
	stores := &fakeRegistryStores{
		bindings:      []*plugins.AuthBinding{{InstallationID: 3, CapabilityID: "oidc", Enabled: true}},
		installations: map[int]*plugins.Installation{3: {ID: 3, Enabled: true}},
		capabilities: map[int][]*plugins.Capability{3: {{Type: "auth_provider.v1", ID: "oidc", Metadata: map[string]any{
			"auth_modes": []any{"oauth2"},
		}}}},
	}
	registry := NewPluginProviderRegistry(PluginProviderRegistryConfig{Bindings: stores, Installations: stores})
	if err := registry.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	stores.capabilities[3] = []*plugins.Capability{{Type: "auth_provider.v1", ID: "renamed"}}
	if err := registry.Rebuild(t.Context()); err == nil {
		t.Fatal("binding without its auth capability was registered")
	}
	if got := registry.Providers(); len(got) != 1 || got[0].Info.Mode != ProviderModeOAuth {
		t.Fatalf("failed rebuild replaced the previous OAuth provider: %+v", got)
	}
}

func TestServiceSeesRegistryChanges(t *testing.T) {
	stores := &fakeRegistryStores{
		bindings:      []*plugins.AuthBinding{{InstallationID: 3, CapabilityID: "oidc", Enabled: true}},
		installations: map[int]*plugins.Installation{3: {ID: 3, Enabled: true}},
		capabilities:  map[int][]*plugins.Capability{3: {{Type: "auth_provider.v1", ID: "oidc", Metadata: map[string]any{"auth_modes": []any{"oauth2"}}}}},
	}
	registry := NewPluginProviderRegistry(PluginProviderRegistryConfig{Bindings: stores, Installations: stores, GlobalConfigs: stores})
	svc := NewService(&LocalProvider{}, nil, nil, nil, nil, nil, nil)
	svc.SetPluginProviderSource(registry)
	if svc.FindOAuthInstallation(3) != nil {
		t.Fatal("provider visible before rebuild")
	}
	if err := registry.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if svc.FindOAuthInstallation(3) == nil || len(svc.ListProviders()) != 2 {
		t.Fatalf("after rebuild: %+v", svc.ListProviders())
	}
	stores.bindings[0].Enabled = false
	_ = registry.Rebuild(context.Background())
	if svc.FindOAuthInstallation(3) != nil || len(svc.ListProviders()) != 1 {
		t.Fatalf("after disable: %+v", svc.ListProviders())
	}
}

// fakeManifests answers the plugin manifest with a global config schema.
type fakeManifests map[int]*pluginv1.PluginManifest

func (f fakeManifests) ManifestForInstallation(_ context.Context, id int) (*pluginv1.PluginManifest, error) {
	if m, ok := f[id]; ok {
		return m, nil
	}
	return nil, plugins.ErrInstallationNotFound
}

// TestPluginProviderRegistryIconDefault: until the operator saves
// icon_url_path, the button shows the schema's default icon (JSON schema
// default first, then the admin form default); a saved value wins, and a
// saved empty value (the plugin config save drops a cleared field, but rows
// saved earlier may hold one) shows the default too.
func TestPluginProviderRegistryIconDefault(t *testing.T) {
	formDefault := &pluginv1.ConfigSchema{Key: "icon_url_path", JsonSchema: `{"type":"object","properties":{"value":{"type":"string"}}}`,
		AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{Key: "value", DefaultValue: structpb.NewStringValue("sso.svg")}}}}
	schemaDefault := &pluginv1.ConfigSchema{Key: "icon_url_path", JsonSchema: `{"type":"object","properties":{"value":{"type":"string","default":"/ldap.svg"}}}`}
	stores := &fakeRegistryStores{
		bindings: []*plugins.AuthBinding{
			{InstallationID: 3, CapabilityID: "oidc", Enabled: true},
			{InstallationID: 4, CapabilityID: "ldap", Enabled: true},
		},
		installations: map[int]*plugins.Installation{3: {ID: 3, Enabled: true}, 4: {ID: 4, Enabled: true}},
		capabilities:  map[int][]*plugins.Capability{3: {{Type: "auth_provider.v1", ID: "oidc"}}, 4: {{Type: "auth_provider.v1", ID: "ldap"}}},
	}
	manifests := fakeManifests{
		3: {GlobalConfigSchema: []*pluginv1.ConfigSchema{formDefault}},
		4: {GlobalConfigSchema: []*pluginv1.ConfigSchema{schemaDefault}},
	}
	registry := NewPluginProviderRegistry(PluginProviderRegistryConfig{Bindings: stores, Installations: stores, GlobalConfigs: stores, Manifests: manifests})
	icons := func() map[int]string {
		t.Helper()
		if err := registry.Rebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
		out := map[int]string{}
		for _, p := range registry.Providers() {
			out[p.Info.InstallationID] = p.Info.IconURL
		}
		return out
	}
	got := icons()
	if got[3] != plugins.ContentPrefix+"/plugins/3/assets/sso.svg" || got[4] != plugins.ContentPrefix+"/plugins/4/assets/ldap.svg" {
		t.Fatalf("unsaved icons = %v", got)
	}

	stores.configs = map[int][]*plugins.RuntimeConfig{
		3: {{Key: "icon_url_path", Value: map[string]any{"value": "custom.png"}}},
		4: {{Key: "icon_url_path", Value: map[string]any{"value": ""}}},
	}
	got = icons()
	if got[3] != plugins.ContentPrefix+"/plugins/3/assets/custom.png" || got[4] != plugins.ContentPrefix+"/plugins/4/assets/ldap.svg" {
		t.Fatalf("saved icons = %v", got)
	}

	// Without a manifest source, or a manifest without the setting, there is
	// no default.
	stores.configs = nil
	registry = NewPluginProviderRegistry(PluginProviderRegistryConfig{Bindings: stores, Installations: stores, GlobalConfigs: stores, Manifests: fakeManifests{3: {}}})
	if got = icons(); got[3] != "" || got[4] != "" {
		t.Fatalf("no default = %v", got)
	}
}

// lockedRegistryStores guards fakeRegistryStores for a test that changes
// them while RunRebuilds reads them.
type lockedRegistryStores struct {
	mu    sync.Mutex
	reads int
	fakeRegistryStores
}

func (f *lockedRegistryStores) ListAuthBindings(ctx context.Context) ([]*plugins.AuthBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	out := make([]*plugins.AuthBinding, 0, len(f.bindings))
	for _, b := range f.bindings {
		copied := *b
		out = append(out, &copied)
	}
	return out, f.listErr
}

// set changes the binding and the read error and answers the reads so far.
func (f *lockedRegistryStores) set(enabled bool, listErr error) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindings[0].Enabled = enabled
	f.listErr = listErr
	return f.reads
}

// waitForReads waits until the stores were read more than n times.
func (f *lockedRegistryStores) waitForReads(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		reads := f.reads
		f.mu.Unlock()
		if reads > n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reads = %d, want more than %d", reads, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForProviders waits until the registry holds want providers.
func waitForProviders(t *testing.T, registry *PluginProviderRegistry, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(registry.Providers()) != want {
		if time.Now().After(deadline) {
			t.Fatalf("providers = %d, want %d", len(registry.Providers()), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestPluginProviderRegistryRunRebuilds: a node catches up with a binding
// change it was never told about, and retries a rebuild that failed.
func TestPluginProviderRegistryRunRebuilds(t *testing.T) {
	stores := &lockedRegistryStores{fakeRegistryStores: fakeRegistryStores{
		bindings:      []*plugins.AuthBinding{{InstallationID: 3, CapabilityID: "oidc"}},
		installations: map[int]*plugins.Installation{3: {ID: 3, Enabled: true}},
		capabilities:  map[int][]*plugins.Capability{3: {{Type: "auth_provider.v1", ID: "oidc"}}},
	}}
	registry := NewPluginProviderRegistry(PluginProviderRegistryConfig{Bindings: stores, Installations: &stores.fakeRegistryStores, GlobalConfigs: &stores.fakeRegistryStores})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		registry.RunRebuilds(ctx, 20*time.Millisecond, time.Millisecond)
	}()
	t.Cleanup(func() { cancel(); <-done })

	// Turned on elsewhere, with no announcement: the periodic resync finds it.
	stores.set(true, nil)
	waitForProviders(t, registry, 1)
	// A failing read keeps the set; the retry applies the change once the
	// read works again.
	reads := stores.set(false, errors.New("database down"))
	stores.waitForReads(t, reads+2)
	if got := len(registry.Providers()); got != 1 {
		t.Fatalf("a failed rebuild changed the set: %d", got)
	}
	stores.set(false, nil)
	waitForProviders(t, registry, 0)
}

// TestPluginProviderRegistryRequestRebuild: an announcement rebuilds
// without waiting for the resync, and requests collapse.
func TestPluginProviderRegistryRequestRebuild(t *testing.T) {
	stores := &lockedRegistryStores{fakeRegistryStores: fakeRegistryStores{
		bindings:      []*plugins.AuthBinding{{InstallationID: 3, CapabilityID: "oidc"}},
		installations: map[int]*plugins.Installation{3: {ID: 3, Enabled: true}},
		capabilities:  map[int][]*plugins.Capability{3: {{Type: "auth_provider.v1", ID: "oidc"}}},
	}}
	registry := NewPluginProviderRegistry(PluginProviderRegistryConfig{Bindings: stores, Installations: &stores.fakeRegistryStores, GlobalConfigs: &stores.fakeRegistryStores})
	for range 3 {
		registry.RequestRebuild() // never blocks
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		registry.RunRebuilds(ctx, time.Hour, time.Hour)
	}()
	t.Cleanup(func() { cancel(); <-done })
	stores.set(true, nil)
	registry.RequestRebuild()
	waitForProviders(t, registry, 1)
}
