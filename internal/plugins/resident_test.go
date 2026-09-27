package plugins

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// buildResidentFixture compiles testdata/residentplugin into a temp install
// dir, writes the manifest the binary reports (checksum included) next to
// it as manifest.json, and returns the binary path.
func buildResidentFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "residentplugin")
	build := exec.Command("go", "build", "-o", bin, "./testdata/residentplugin")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build residentplugin: %v\n%s", err, out)
	}
	raw, err := exec.Command(bin, "manifest").Output()
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	manifest := &pluginv1.PluginManifest{}
	if err := protojson.Unmarshal(raw, manifest); err != nil {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	if err := os.WriteFile(InstalledManifestPath(bin), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return bin
}

type residentFixture struct {
	service *Service
	store   *fakeServiceInstallationStore
	host    *pluginhost.Host
	exit    string
	broker  *netaccess.Broker
}

func newResidentFixture(t *testing.T, opts ResidentOptions) *residentFixture {
	t.Helper()
	bin := buildResidentFixture(t)
	exitFile := filepath.Join(t.TempDir(), "exit-now")
	t.Setenv("SILO_TEST_PLUGIN_EXIT_FILE", exitFile)

	store := newFakeServiceInstallationStore(&Installation{
		ID:          5,
		PluginID:    "silo.test.resident",
		Version:     "0.1.0",
		InstallPath: bin,
		Enabled:     true,
		Kind:        KindPlugin,
	})
	store.listCapabilities = []*Capability{{InstallationID: 5, Type: capability.NetworkAccessProvider, ID: "stub"}}

	broker := netaccess.NewBroker()
	host := pluginhost.NewHost(pluginhost.Config{
		NetworkAccess:     broker,
		Logger:            hclog.NewNullLogger(),
		ExitCheckInterval: 20 * time.Millisecond,
		// Bind the RuntimeHost broker so fixtures can call back into the host.
		HostInfo: func(context.Context) (pluginhost.HostInfo, error) {
			return pluginhost.HostInfo{Role: pluginhost.HostRoleAPI, Name: "fixture"}, nil
		},
	})
	service := &Service{installations: store, host: NewHostAdapter(host)}
	if opts.MinBackoff == 0 {
		opts.MinBackoff = 10 * time.Millisecond
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 50 * time.Millisecond
	}
	service.resident = newResidentSupervisor(service, opts)
	// Same hook order as NewService and NewNodeService: the row cache is
	// dropped before the supervisor reads the rows.
	service.AddLifecycleHook(func(context.Context) { service.invalidateInstallationCache() })
	service.AddLifecycleHook(func(ctx context.Context) { service.resident.Reconcile(ctx) })
	host.SetExitHandler(service.HandleResidentExit)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = service.StopResidents(ctx)
		_ = host.Shutdown(ctx)
	})
	return &residentFixture{service: service, store: store, host: host, exit: exitFile, broker: broker}
}

func (f *residentFixture) crash(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.exit, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *residentFixture) heal(t *testing.T) {
	t.Helper()
	if err := os.Remove(f.exit); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

// waitState polls the supervisor until pred accepts the installation's
// state or the deadline passes.
func waitState(t *testing.T, service *Service, id int, what string, pred func(RuntimeState, bool) bool) RuntimeState {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		state, ok := service.Residents().State(id)
		if pred(state, ok) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last state %+v (tracked=%v)", what, state, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func running(state RuntimeState, ok bool) bool { return ok && state.State == ResidentRunning }

func TestResidentSupervisorStartsAtReconcileAndStopsOnDisable(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()

	// Lifecycle hooks before the API listener is bound must not launch.
	f.service.OnLifecycleChange(ctx)
	if _, ok := f.service.Residents().State(5); ok {
		t.Fatal("supervisor tracked the installation before StartResidents")
	}
	if got := f.service.RuntimeState(5); got.State != ResidentStopped {
		t.Fatalf("RuntimeState before start = %+v, want stopped", got)
	}

	f.service.StartResidents(ctx)
	state := waitState(t, f.service, 5, "running after StartResidents", running)
	if !state.Resident || state.RestartCount != 0 || state.LastStartedAt == nil || state.LastError != "" {
		t.Fatalf("running state = %+v", state)
	}
	if _, err := f.host.Client(5); err != nil {
		t.Fatalf("host.Client after start: %v", err)
	}
	if got := f.service.RuntimeState(5); got.State != ResidentRunning || !got.Resident {
		t.Fatalf("RuntimeState = %+v", got)
	}

	// Disable: the next reconcile stops the process and forgets the entry.
	disabled := false
	if err := f.store.Update(ctx, 5, UpdateInstallationInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	f.service.OnLifecycleChange(ctx)
	if _, ok := f.service.Residents().State(5); ok {
		t.Fatal("supervisor still tracks a disabled installation")
	}
	if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("host.Client after disable = %v, want ErrClientNotFound", err)
	}
	if got := f.service.RuntimeState(5); got.State != ResidentStopped {
		t.Fatalf("RuntimeState after disable = %+v", got)
	}

	// Re-enable: reconcile starts it again from a clean entry.
	enabled := true
	if err := f.store.Update(ctx, 5, UpdateInstallationInput{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	f.service.OnLifecycleChange(ctx)
	waitState(t, f.service, 5, "running after re-enable", running)
}

func TestResidentSupervisorRestartsAfterCrashWithBackoff(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{MaxFailures: 10})
	ctx := context.Background()
	f.service.StartResidents(ctx)
	first := waitState(t, f.service, 5, "initial running", running)

	f.crash(t)
	backoff := waitState(t, f.service, 5, "backoff after crash", func(s RuntimeState, ok bool) bool {
		return ok && s.State != ResidentRunning
	})
	f.heal(t)
	if backoff.RestartCount < 1 || backoff.LastError == "" {
		t.Fatalf("state after crash = %+v", backoff)
	}
	if backoff.State == ResidentBackoff && backoff.NextRestartAt == nil {
		t.Fatalf("backoff without NextRestartAt: %+v", backoff)
	}

	again := waitState(t, f.service, 5, "running after restart", func(s RuntimeState, ok bool) bool {
		return running(s, ok) && s.LastStartedAt != nil && s.LastStartedAt.After(*first.LastStartedAt)
	})
	if again.RestartCount < 1 || again.LastError != "" || again.NextRestartAt != nil {
		t.Fatalf("state after restart = %+v", again)
	}
	if _, err := f.host.Client(5); err != nil {
		t.Fatalf("host.Client after restart: %v", err)
	}
}

func TestResidentSupervisorFailsAfterConsecutiveFailuresAndAdminRestartRecovers(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{MaxFailures: 3})
	ctx := context.Background()
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "initial running", running)

	// The exit trigger stays in place, so every relaunch dies again.
	f.crash(t)
	failed := waitState(t, f.service, 5, "failed state", func(s RuntimeState, ok bool) bool {
		return ok && s.State == ResidentFailed
	})
	if failed.LastError == "" || failed.NextRestartAt != nil {
		t.Fatalf("failed state = %+v", failed)
	}
	if failed.RestartCount != 2 {
		t.Fatalf("restart_count in failed state = %d, want 2 (MaxFailures-1)", failed.RestartCount)
	}
	// Reconcile does not resurrect a failed resident.
	f.service.OnLifecycleChange(ctx)
	if s, _ := f.service.Residents().State(5); s.State != ResidentFailed {
		t.Fatalf("state after reconcile = %+v, want failed", s)
	}

	f.heal(t)
	if err := f.service.RestartInstallation(ctx, 5); err != nil {
		t.Fatalf("RestartInstallation: %v", err)
	}
	state, ok := f.service.Residents().State(5)
	if !ok || state.State != ResidentRunning || state.RestartCount != 0 || state.LastError != "" {
		t.Fatalf("state after admin restart = %+v (tracked=%v)", state, ok)
	}
	if _, err := f.host.Client(5); err != nil {
		t.Fatalf("host.Client after admin restart: %v", err)
	}
}

func TestResidentLazyRPCDoesNotBypassFailureBudget(t *testing.T) {
	for _, state := range []ResidentState{ResidentFailed, ResidentBackoff} {
		t.Run(string(state), func(t *testing.T) {
			maxFailures := 1
			if state == ResidentBackoff {
				maxFailures = 2
			}
			f := newResidentFixture(t, ResidentOptions{
				MaxFailures: maxFailures, MinBackoff: time.Hour, MaxBackoff: time.Hour,
			})
			f.service.StartResidents(t.Context())
			waitState(t, f.service, 5, "initial running", running)
			f.crash(t)
			waitState(t, f.service, 5, "parked after crash", func(s RuntimeState, ok bool) bool {
				return ok && s.State == state
			})
			f.heal(t)
			// Every lazy capability RPC obtains its process through this path,
			// before checking whether the manifest declares that capability.
			if _, err := f.service.MetadataProviderClient(t.Context(), 5, "metadata"); !errors.Is(err, pluginhost.ErrPluginUnhealthy) {
				t.Errorf("lazy RPC error = %v, want ErrPluginUnhealthy", err)
			}
			if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
				t.Fatalf("lazy RPC revived the parked resident: %v", err)
			}
			if err := f.service.RestartInstallation(t.Context(), 5); err != nil {
				t.Fatal(err)
			}
			waitState(t, f.service, 5, "running after admin restart", running)
		})
	}
}

func TestResidentSupervisorHaltStopsResidents(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "initial running", running)

	if err := f.service.StopResidents(ctx); err != nil {
		t.Fatalf("StopResidents: %v", err)
	}
	if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("host.Client after halt = %v, want ErrClientNotFound", err)
	}
	// Halted: neither a reconcile nor an admin restart may relaunch.
	f.service.OnLifecycleChange(ctx)
	if err := f.service.Residents().Restart(ctx, 5); !errors.Is(err, ErrNotResident) {
		t.Fatalf("Restart after halt = %v, want ErrNotResident", err)
	}
	if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("host.Client after halted reconcile = %v, want ErrClientNotFound", err)
	}
}

func TestResidentBackoffSchedule(t *testing.T) {
	minBackoff, maxBackoff := time.Second, time.Minute
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{{0, time.Second}, {1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {6, 32 * time.Second}, {7, time.Minute}, {10, time.Minute}, {64, time.Minute}} {
		if got := residentBackoff(tc.failures, minBackoff, maxBackoff); got != tc.want {
			t.Errorf("residentBackoff(%d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
	for i := 0; i < 100; i++ {
		if j := defaultBackoffJitter(time.Minute); j < 0 || j > 15*time.Second {
			t.Fatalf("jitter %v outside [0, 15s]", j)
		}
	}
}

func TestResidentPredicateNeedsCapability(t *testing.T) {
	if !IsResidentCapabilityType(capability.NetworkAccessProvider) {
		t.Fatal("network_access_provider.v1 must be resident")
	}
	for _, typ := range []string{capability.MetadataProvider, capability.ScheduledTask, capability.WatchSyncProvider, ""} {
		if IsResidentCapabilityType(typ) {
			t.Fatalf("%q must not be resident", typ)
		}
	}
}

func TestResidentRestartOfNonResidentOnlyStops(t *testing.T) {
	host := &fakeServiceHost{}
	store := newFakeServiceInstallationStore(&Installation{ID: 2, PluginID: "silo.metadb", Version: "1", InstallPath: "/x/plugin", Enabled: true, Kind: KindPlugin})
	service := &Service{installations: store, host: host}
	service.resident = newResidentSupervisor(service, ResidentOptions{})
	if err := service.RestartInstallation(context.Background(), 2); err != nil {
		t.Fatalf("RestartInstallation: %v", err)
	}
	if len(host.stopped) != 1 || host.stopped[0] != 2 || len(host.started) != 0 {
		t.Fatalf("stopped=%v started=%d, want one stop and no start", host.stopped, len(host.started))
	}
	disabled := false
	_ = store.Update(context.Background(), 2, UpdateInstallationInput{Enabled: &disabled})
	service.invalidateInstallationCache()
	if err := service.RestartInstallation(context.Background(), 2); !errors.Is(err, ErrInstallationDisabled) {
		t.Fatalf("RestartInstallation(disabled) = %v, want ErrInstallationDisabled", err)
	}
}

// buildResidentFixtureVersion is buildResidentFixture for a second release
// of the same plugin: the binary reports the given version in its manifest.
func buildResidentFixtureVersion(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "residentplugin")
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", bin, "./testdata/residentplugin")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build residentplugin %s: %v\n%s", version, err, out)
	}
	raw, err := exec.Command(bin, "manifest").Output()
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	if err := os.WriteFile(InstalledManifestPath(bin), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return bin
}

// A replaced or auto-updated binary changes the row's version and install
// path. On the host that made the change the process is already gone; on
// any other host running the installation (a proxy node) it is still alive
// and must be replaced by the next reconcile, not kept because it answers
// health checks.
func TestResidentSupervisorReplacesProcessWhenBinaryChanges(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "initial running", running)
	first, err := f.host.Client(5)
	if err != nil {
		t.Fatal(err)
	}
	if got := first.Manifest().GetVersion(); got != "0.1.0" {
		t.Fatalf("initial version = %q", got)
	}

	next := buildResidentFixtureVersion(t, "0.2.0")
	version := "0.2.0"
	if err := f.store.Update(ctx, 5, UpdateInstallationInput{Version: &version, InstallPath: &next}); err != nil {
		t.Fatal(err)
	}
	f.service.OnLifecycleChange(ctx)

	deadline := time.Now().Add(30 * time.Second)
	for {
		current, err := f.host.Client(5)
		state, _ := f.service.Residents().State(5)
		if err == nil && current != first && current.Manifest().GetVersion() == "0.2.0" && state.State == ResidentRunning {
			if state.RestartCount != 0 || state.LastError != "" {
				t.Fatalf("replacement counted as a failure: %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resident kept the old process after the binary changed (err=%v, state=%+v)", err, state)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The same reconcile again is a no-op: nothing changed.
	replaced, _ := f.host.Client(5)
	f.service.OnLifecycleChange(ctx)
	if again, err := f.host.Client(5); err != nil || again != replaced {
		t.Fatalf("an unchanged row replaced the process (err=%v)", err)
	}
}

// fixtureArchive packs a built fixture binary the way the installer stores
// it in plugin_archives, so a service with an archive cache can rehydrate it.
func fixtureArchive(t *testing.T, bin string, installationID int) *InstallationArchive {
	t.Helper()
	binaryData, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(InstalledManifestPath(bin))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifestBytes(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := buildBinaryPluginArchive(manifestBytes, binaryData)
	if err != nil {
		t.Fatal(err)
	}
	return &InstallationArchive{InstallationID: installationID, ManifestJSON: manifestBytes, Checksum: manifest.GetChecksum(), Bytes: archiveBytes}
}

// A proxy node never has the API server's install path: it rehydrates each
// release from plugin_archives into its own cache root. When the row moves
// to a new release the old process, still healthy, is replaced by one built
// from the new archive, and the old release leaves the cache.
func TestResidentSupervisorReplacesRehydratedProcessOnNewRelease(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "proxy-cache")
	apiMachine := filepath.Join(string(filepath.Separator), "api-machine-does-not-exist", "plugins", "silo.test.resident")

	first := f.store.byID[5].InstallPath
	f.store.archives = map[int]*InstallationArchive{5: fixtureArchive(t, first, 5)}
	f.store.byID[5].InstallPath = filepath.Join(apiMachine, "0.1.0", "install-aaaa", "plugin")
	f.service.archiveCache = NewArchiveCacheAt(f.store, root)

	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running from the rehydrated 0.1.0", running)
	initial, err := f.host.Client(5)
	if err != nil {
		t.Fatal(err)
	}
	firstLocal := filepath.Join(root, "silo.test.resident", "0.1.0", "install-aaaa", "plugin")
	if _, err := os.Stat(firstLocal); err != nil {
		t.Fatalf("0.1.0 was not rehydrated under the cache root: %v", err)
	}

	next := buildResidentFixtureVersion(t, "0.2.0")
	f.store.archives[5] = fixtureArchive(t, next, 5)
	version := "0.2.0"
	nextRecorded := filepath.Join(apiMachine, "0.2.0", "install-bbbb", "plugin")
	if err := f.store.Update(ctx, 5, UpdateInstallationInput{Version: &version, InstallPath: &nextRecorded}); err != nil {
		t.Fatal(err)
	}
	f.service.OnLifecycleChange(ctx)

	deadline := time.Now().Add(30 * time.Second)
	for {
		current, err := f.host.Client(5)
		state, _ := f.service.Residents().State(5)
		if err == nil && current != initial && current.Manifest().GetVersion() == "0.2.0" && state.State == ResidentRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy kept the 0.1.0 process after the release changed (err=%v, state=%+v)", err, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(root, "silo.test.resident", "0.2.0", "install-bbbb", "plugin")); err != nil {
		t.Fatalf("0.2.0 was not rehydrated under the cache root: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(firstLocal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("0.1.0 release still in the cache after the replacement: %v", err)
	}
	if _, err := os.Stat(apiMachine); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proxy touched the API server's install path: %v", err)
	}
}

// Two enabled installations declaring one provider slug: only the lowest id
// is resident. The duplicate is never commanded or reported, so it must not
// run either.
func TestResidentSupervisorDoesNotStartADuplicateProviderSlug(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	second := buildResidentFixture(t)
	dup := &Installation{ID: 6, PluginID: "silo.test.resident-copy", Version: "0.1.0", InstallPath: second, Enabled: true, Kind: KindPlugin}
	f.store.byID[dup.ID] = dup
	f.store.byPluginID[dup.PluginID] = append(f.store.byPluginID[dup.PluginID], dup)
	f.store.listCapabilities = append(f.store.listCapabilities, &Capability{InstallationID: 6, Type: capability.NetworkAccessProvider, ID: "stub"})

	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "owner running", running)
	if _, tracked := f.service.Residents().State(6); tracked {
		t.Fatal("duplicate slug installation became resident")
	}
	if _, err := f.host.Client(6); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("duplicate slug installation was launched: %v", err)
	}
	if _, err := f.service.MetadataProviderClient(ctx, 6, "metadata"); !errors.Is(err, pluginhost.ErrPluginUnhealthy) {
		t.Errorf("duplicate lazy RPC error = %v, want ErrPluginUnhealthy", err)
	}
	if _, err := f.host.Client(6); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatal("lazy RPC started the excluded duplicate provider")
	}
}

func TestResidentLazyRPCDoesNotLaunchBeforeTheGateOpens(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	for _, armed := range []bool{false, true} {
		if armed {
			f.service.SetResidentGate(func(context.Context) error { return errors.New("node disabled") })
			f.service.StartResidents(t.Context())
		}
		if _, err := f.service.MetadataProviderClient(t.Context(), 5, "metadata"); !errors.Is(err, pluginhost.ErrPluginUnhealthy) {
			t.Errorf("lazy RPC with armed=%v error = %v, want ErrPluginUnhealthy", armed, err)
		}
		if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
			t.Fatalf("lazy RPC started a provider before the gate opened (armed=%v)", armed)
		}
	}
}

// A change of host identity (a proxy whose stream_nodes row was deleted and
// re-registered under a new id) replaces every running resident: the process
// keeps the identity it started with in memory, so it must not go on serving
// under the new one.
func TestResidentSupervisorReplacesProcessWhenHostIdentityChanges(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	var identity atomic.Value
	identity.Store("node:1")
	f.service.SetResidentHostIdentity(func() string { return identity.Load().(string) })
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running under node:1", running)
	first, err := f.host.Client(5)
	if err != nil {
		t.Fatal(err)
	}

	f.service.OnLifecycleChange(ctx)
	if same, err := f.host.Client(5); err != nil || same != first {
		t.Fatalf("an unchanged identity replaced the process (err=%v)", err)
	}

	identity.Store("node:2")
	f.service.OnLifecycleChange(ctx)
	deadline := time.Now().Add(30 * time.Second)
	for {
		current, err := f.host.Client(5)
		state, _ := f.service.Residents().State(5)
		if err == nil && current != first && state.State == ResidentRunning {
			if state.RestartCount != 0 || state.LastError != "" {
				t.Fatalf("identity change counted as a failure: %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resident kept the old process after the host identity changed (err=%v, state=%+v)", err, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Overlapping reconciles must not let an older one, whose desired set was
// computed before a disable, resurrect the resident after a newer one
// removed it. Reconcile is serialized end to end, so the second call sees
// the disabled row.
func TestResidentSupervisorConcurrentReconcilesDoNotResurrectADisabledResident(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running", running)

	// Race many reconciles against a disable. Whatever the interleaving, the
	// final state is "not resident" and no process is left behind.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.service.resident.Reconcile(ctx)
		}()
	}
	disabled := false
	if err := f.store.Update(ctx, 5, UpdateInstallationInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	f.service.OnLifecycleChange(ctx)
	wg.Wait()
	// One more reconcile after everything settled must be a no-op.
	f.service.resident.Reconcile(ctx)
	if _, tracked := f.service.Residents().State(5); tracked {
		t.Fatal("disabled resident is still tracked after overlapping reconciles")
	}
	if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("disabled resident's process survived: %v", err)
	}
}

func TestResidentSupervisorKeepsRunningProviderWhenManifestUnavailable(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := t.Context()
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running", running)
	first, err := f.host.Client(5)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(InstalledManifestPath(f.store.byID[5].InstallPath)); err != nil {
		t.Fatal(err)
	}
	f.service.OnLifecycleChange(ctx)
	if current, err := f.host.Client(5); err != nil || current != first {
		t.Fatalf("healthy process replaced after manifest read failure: %v", err)
	}
	if _, err := f.service.HostNetworkAccessDisconnect(ctx, "stub"); err != nil {
		t.Fatalf("provider cannot be disconnected without its disk manifest: %v", err)
	}
	disabled := false
	if err := f.store.Update(ctx, 5, UpdateInstallationInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	f.service.OnLifecycleChange(ctx)
	if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("disabled provider still running: %v", err)
	}
}

func TestResidentRestartReturnsWhenCallerCancels(t *testing.T) {
	manifest := testPluginManifest(t, "silo.metadb", "0.0.36")
	path := writeInstalledPluginManifest(t, manifest)
	host := &ctxCaptureHost{entered: make(chan struct{}), proceed: make(chan struct{}), startResult: &fakePluginClient{manifest: manifest}}
	store := newFakeServiceInstallationStore(&Installation{ID: 5, PluginID: manifest.PluginId, Version: manifest.Version, InstallPath: path, Enabled: true, Kind: KindPlugin})
	service := &Service{host: host, installations: store}
	service.resident = newResidentSupervisor(service, ResidentOptions{})
	service.resident.entries[5] = &residentEntry{id: 5, state: ResidentRunning}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.resident.Restart(ctx, 5) }()
	t.Cleanup(func() { close(host.proceed); _ = service.StopResidents(context.Background()) })
	select {
	case <-host.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not reach the host")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("restart cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("restart ignored caller cancellation")
	}
}
