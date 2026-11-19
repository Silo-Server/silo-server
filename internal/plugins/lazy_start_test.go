package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
)

func newLazyStartService(t *testing.T) (*Service, *countingHerdHost, *Installation) {
	t.Helper()
	manifest := testPluginManifest(t, "silo.metadb", "0.0.36")
	installation := &Installation{
		ID: 7, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
		InstallPath: writeInstalledPluginManifest(t, manifest), Enabled: true,
	}
	store := newFakeServiceInstallationStore(installation)
	host := &countingHerdHost{startResult: &fakePluginClient{manifest: manifest}}
	return &Service{installations: store, host: host}, host, installation
}

func TestSupervisedStartIsNotRecordedAsLazyFailure(t *testing.T) {
	manifest := testPluginManifest(t, "silo.vpn", "1.0.0")
	manifest.Capabilities = append(manifest.Capabilities, &pluginv1.CapabilityDescriptor{
		Type: capability.NetworkAccessProvider, Id: "vpn", DisplayName: "VPN",
		NetworkAccessProvider: &pluginv1.NetworkAccessProviderDescriptor{Provider: "vpn"},
	})
	installation := &Installation{
		ID: 7, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
		InstallPath: writeInstalledPluginManifest(t, manifest), Enabled: true,
	}
	host := &countingHerdHost{startErr: errors.New("boom")}
	service := &Service{installations: newFakeServiceInstallationStore(installation), host: host}

	if _, err := service.Start(t.Context(), 7); !errors.Is(err, host.startErr) {
		t.Fatalf("Start returned %v, want the host start error", err)
	}
	if got := service.RuntimeStateOf(installation); got.State != ResidentStopped || got.LastError != "" {
		t.Fatalf("RuntimeStateOf after supervised start failure = %+v, want stopped", got)
	}
}

// The exported Start (used to restore a plugin after a failed uninstall) may
// launch residents, but a non-resident it fails to launch is still recorded.
func TestExportedStartRecordsNonResidentFailure(t *testing.T) {
	service, host, installation := newLazyStartService(t)

	host.startErr = errors.New("boom")
	if _, err := service.Start(context.Background(), 7); err == nil {
		t.Fatal("Start succeeded, want the start error")
	}
	if got := service.RuntimeStateOf(installation); got.State != ResidentFailed || got.LastError != "boom" {
		t.Fatalf("RuntimeStateOf after failed Start = %+v, want failed", got)
	}
}

func TestFailedLazyStartMasksCredentialsInLastError(t *testing.T) {
	service, host, installation := newLazyStartService(t)

	host.startErr = errors.New("rpc error: code = Unauthenticated desc = api_key=sk-live-123")
	if _, err := service.ensureClient(context.Background(), 7); err == nil {
		t.Fatal("ensureClient succeeded, want the start error")
	}
	got := service.RuntimeStateOf(installation)
	if got.State != ResidentFailed || strings.Contains(got.LastError, "sk-live-123") ||
		!strings.Contains(got.LastError, "Unauthenticated") {
		t.Fatalf("RuntimeStateOf = %+v, want failed with the api key masked", got)
	}
}

func TestFailedLazyStartIsRecordedAgainstTheTriedSnapshot(t *testing.T) {
	service, _, tried := newLazyStartService(t)
	host := &ctxCaptureHost{
		entered: make(chan struct{}), proceed: make(chan struct{}),
		startErr: errors.New("plugin handshake failed"),
	}
	service.host = host
	release := sync.OnceFunc(func() { close(host.proceed) })
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := service.ensureClient(t.Context(), 7)
		done <- err
	}()
	waitForLifecycleTestSignal(t, host.entered)
	if err := service.installations.Update(t.Context(), 7, UpdateInstallationInput{Version: new("0.0.37")}); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; !errors.Is(err, host.startErr) {
		t.Fatalf("ensureClient returned %v, want the host start error", err)
	}
	updated, err := service.installations.GetByID(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if got := service.RuntimeStateOf(tried); got.State != ResidentFailed {
		t.Fatalf("RuntimeStateOf for the tried release = %+v, want failed", got)
	}
	if got := service.RuntimeStateOf(updated); got.State != ResidentStopped || got.LastError != "" {
		t.Fatalf("RuntimeStateOf for the updated release = %+v, want stopped", got)
	}
}
