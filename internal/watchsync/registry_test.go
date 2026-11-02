package watchsync

import "testing"

type stubProvider struct {
	key          string
	displayName  string
	capabilities Capabilities
}

func (s stubProvider) Key() string                { return s.key }
func (s stubProvider) DisplayName() string        { return s.displayName }
func (s stubProvider) Capabilities() Capabilities { return s.capabilities }

func TestRegistryRejectsDuplicateProvider(t *testing.T) {
	registry := NewRegistry()
	provider := stubProvider{key: "trakt", displayName: "Trakt"}

	if err := registry.Register(provider); err != nil {
		t.Fatalf("register first provider: %v", err)
	}
	if err := registry.Register(provider); err == nil {
		t.Fatal("expected duplicate provider to be rejected")
	}
}

type stubPluginProvider struct{ stubProvider }

func (stubPluginProvider) ProviderSource() string { return providerSourcePlugin }

func TestRegistryReplacePluginProviders(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(stubProvider{key: "trakt"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplacePluginProviders([]Provider{stubPluginProvider{stubProvider{key: testPluginCapabilityID}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("anilist"); !ok {
		t.Fatal("plugin provider was not registered")
	}
	if err := registry.ReplacePluginProviders([]Provider{stubPluginProvider{stubProvider{key: "simkl-plugin"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("anilist"); ok {
		t.Fatal("stale plugin provider was not removed")
	}
	if _, ok := registry.Get("trakt"); !ok {
		t.Fatal("built-in provider was removed")
	}
}

func TestRegistryReplacePluginProvidersRejectsBuiltinCollision(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(stubProvider{key: "trakt"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplacePluginProviders([]Provider{stubPluginProvider{stubProvider{key: "trakt"}}}); err == nil {
		t.Fatal("expected built-in collision to fail")
	}
}
