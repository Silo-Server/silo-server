package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
