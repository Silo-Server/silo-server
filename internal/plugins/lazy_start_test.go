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

func TestRuntimeStateOfReportsFailedLazyStartUntilALaterStartSucceeds(t *testing.T) {
	service, host, installation := newLazyStartService(t)
	ctx := context.Background()

	if got := service.RuntimeStateOf(installation); got.State != ResidentStopped || got.LastError != "" {
		t.Fatalf("RuntimeStateOf before any start = %+v, want stopped", got)
	}

	host.startErr = errors.New("start plugin process: exit status 2")
	if _, err := service.ensureClient(ctx, 7); err == nil {
		t.Fatal("ensureClient succeeded, want the start error")
	}
	got := service.RuntimeStateOf(installation)
	if got.Resident || got.State != ResidentFailed || got.LastError != "start plugin process: exit status 2" {
		t.Fatalf("RuntimeStateOf after failed start = %+v, want failed with the start error", got)
	}
	// The ID-only view other callers use is unchanged.
	if got := service.RuntimeState(7); got.State != ResidentStopped {
		t.Fatalf("RuntimeState after failed start = %+v, want stopped", got)
	}

	host.startErr = nil
	if _, err := service.ensureClient(ctx, 7); err != nil {
		t.Fatalf("ensureClient after recovery: %v", err)
	}
	if got := service.RuntimeStateOf(installation); got.State != ResidentRunning || got.LastError != "" {
		t.Fatalf("RuntimeStateOf after successful start = %+v, want running without error", got)
	}

	// A stopped process after a successful start reads stopped, not failed.
	if err := service.Stop(7); err != nil {
		t.Fatal(err)
	}
	if got := service.RuntimeStateOf(installation); got.State != ResidentStopped || got.LastError != "" {
		t.Fatalf("RuntimeStateOf after stop = %+v, want stopped", got)
	}
}

// An update, reinstall, config save or restart changes the installation the
// failure was recorded for; the failure no longer describes it.
func TestFailedLazyStartGoesStaleWhenTheInstallationChanges(t *testing.T) {
	service, host, installation := newLazyStartService(t)

	host.startErr = errors.New("boom")
	if _, err := service.ensureClient(context.Background(), 7); err == nil {
		t.Fatal("ensureClient succeeded, want the start error")
	}
	if got := service.RuntimeStateOf(installation); got.State != ResidentFailed {
		t.Fatalf("RuntimeStateOf after failed start = %+v, want failed", got)
	}

	updated := *installation
	updated.Version = "0.0.37"
	reconfigured := *installation
	reconfigured.RuntimeGeneration++
	for name, changed := range map[string]*Installation{"new release": &updated, "new runtime generation": &reconfigured} {
		if got := service.RuntimeStateOf(changed); got.State != ResidentStopped || got.LastError != "" {
			t.Fatalf("RuntimeStateOf for %s = %+v, want stopped", name, got)
		}
	}
}

func TestRestartInstallationClearsFailedLazyStart(t *testing.T) {
	service, host, installation := newLazyStartService(t)
	ctx := context.Background()

	host.startErr = errors.New("boom")
	if _, err := service.ensureClient(ctx, 7); err == nil {
		t.Fatal("ensureClient succeeded, want the start error")
	}
	if err := service.RestartInstallation(ctx, 7); err != nil {
		t.Fatalf("RestartInstallation: %v", err)
	}
	if got := service.RuntimeStateOf(installation); got.State != ResidentStopped || got.LastError != "" {
		t.Fatalf("RuntimeStateOf after restart = %+v, want stopped without error", got)
	}
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

// A launch that fails on a cached snapshot while an update commits records
// the failure against the release it tried, so the updated row, which has not
// been tried, does not read failed.
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
