package plugins

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// TestStopAfterDisableKeepsANewerEnable stages a disable whose stop runs late:
// the disable commits, an enable commits and launches the plugin, then the
// disable's stop runs. The plugin stays running, since its row is enabled. A
// row still disabled, gone after an uninstall, or that cannot be read is
// stopped.
func TestStopAfterDisableKeepsANewerEnable(t *testing.T) {
	ctx := t.Context()
	newService := func(t *testing.T) (*Service, *fakeServiceInstallationStore, *fakeServiceHost) {
		store := installedPluginStore(t, 3, testPluginManifest(t, "silo.metadb", "0.0.36"))
		host := &fakeServiceHost{clientErr: pluginhost.ErrClientNotFound}
		return &Service{installations: store, host: host}, store, host
	}
	setEnabled := func(t *testing.T, store *fakeServiceInstallationStore, enabled bool) {
		t.Helper()
		if err := store.Update(ctx, 3, UpdateInstallationInput{Enabled: &enabled}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("enabled again in between", func(t *testing.T) {
		service, store, host := newService(t)
		setEnabled(t, store, false) // the disable commits
		setEnabled(t, store, true)  // an enable commits
		service.InvalidateInstallationCache()
		if _, err := service.ensureClient(ctx, 3); err != nil { // and launches
			t.Fatal(err)
		}
		if len(host.started) != 1 {
			t.Fatalf("launches = %d, want 1", len(host.started))
		}
		if err := service.StopUnlessEnabled(ctx, 3); err != nil { // the disable's stop
			t.Fatal(err)
		}
		if len(host.stopped) != 0 {
			t.Fatalf("stopped %v; the newer enable's process should keep running", host.stopped)
		}
	})

	t.Run("still disabled", func(t *testing.T) {
		service, store, host := newService(t)
		setEnabled(t, store, false)
		if err := service.StopUnlessEnabled(ctx, 3); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(host.stopped, []int{3}) {
			t.Fatalf("stopped %v, want [3]", host.stopped)
		}
	})

	t.Run("row cannot be read", func(t *testing.T) {
		service, store, host := newService(t)
		service.installations = failingReadStore{store}
		err := service.StopUnlessEnabled(ctx, 3)
		if !errors.Is(err, errRowRead) {
			t.Fatalf("StopUnlessEnabled = %v, want the read error", err)
		}
		if !slices.Equal(host.stopped, []int{3}) {
			t.Fatalf("stopped %v, want [3]: an unread row must not keep the plugin running", host.stopped)
		}
	})

	t.Run("uninstalled", func(t *testing.T) {
		service, store, host := newService(t)
		store.mu.Lock()
		delete(store.byID, 3)
		store.mu.Unlock()
		if err := service.StopUnlessEnabled(ctx, 3); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(host.stopped, []int{3}) {
			t.Fatalf("stopped %v, want [3]", host.stopped)
		}
	})
}

var errRowRead = errors.New("database unreachable")

// failingReadStore is the installation store with every row read failing.
type failingReadStore struct{ *fakeServiceInstallationStore }

func (failingReadStore) GetByID(context.Context, int) (*Installation, error) {
	return nil, errRowRead
}
