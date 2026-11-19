package plugins

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
	})
	installation := &Installation{
		ID: 7, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
		InstallPath: writeInstalledPluginManifest(t, manifest), Enabled: true,
	}
	host := &countingHerdHost{startErr: errors.New("boom")}
	service := &Service{installations: newFakeServiceInstallationStore(installation), host: host}

	if _, err := service.start(context.Background(), 7, true); err == nil {
		t.Fatal("start succeeded, want the start error")
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
	service, _, installation := newLazyStartService(t)
	tried := *installation
	tried.InstallPath = filepath.Join(t.TempDir(), "missing", "plugin")
	service.installationCache = map[int]*Installation{7: &tried}

	updated := *installation
	updated.Version = "0.0.37"
	service.installations = newFakeServiceInstallationStore(&updated)

	if _, err := service.ensureClient(context.Background(), 7); err == nil {
		t.Fatal("ensureClient succeeded, want the manifest load error")
	}
	if got := service.RuntimeStateOf(&tried); got.State != ResidentFailed {
		t.Fatalf("RuntimeStateOf for the tried release = %+v, want failed", got)
	}
	if got := service.RuntimeStateOf(&updated); got.State != ResidentStopped || got.LastError != "" {
		t.Fatalf("RuntimeStateOf for the updated release = %+v, want stopped", got)
	}
}
