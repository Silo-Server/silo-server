package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/proxy"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// memoryEventBus is the Redis event bus for one process: what an API-mode
// host and a proxy-mode host share when they run in one test binary.
type memoryEventBus struct {
	mu       sync.Mutex
	handlers map[string][]cache.EventHandler
}

func newMemoryEventBus() *memoryEventBus {
	return &memoryEventBus{handlers: make(map[string][]cache.EventHandler)}
}

func (b *memoryEventBus) Publish(_ context.Context, channel string, event cache.Event) error {
	b.mu.Lock()
	handlers := append([]cache.EventHandler(nil), b.handlers[channel]...)
	b.mu.Unlock()
	for _, handler := range handlers {
		handler(event)
	}
	return nil
}

func (b *memoryEventBus) Subscribe(_ context.Context, channel string, handler cache.EventHandler) error {
	b.mu.Lock()
	b.handlers[channel] = append(b.handlers[channel], handler)
	b.mu.Unlock()
	return nil
}

func (b *memoryEventBus) Close() error { return nil }

func multinodeTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// setServerSetting writes one server_settings row for the test and restores
// the previous value (or absence) afterwards.
func setServerSetting(t *testing.T, pool *pgxpool.Pool, key, value string) {
	t.Helper()
	ctx := context.Background()
	var previous *string
	if err := pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&previous); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("read %s: %v", key, err)
		}
		previous = nil
	}
	if _, err := pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
	t.Cleanup(func() {
		if previous == nil {
			_, _ = pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, key)
			return
		}
		_, _ = pool.Exec(ctx, `UPDATE server_settings SET value = $2 WHERE key = $1`, key, *previous)
	})
}

func buildResidentFixtureBinary(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "residentplugin")
	build := exec.Command("go", "build", "-o", bin, "../../internal/plugins/testdata/residentplugin")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build residentplugin: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestNetworkAccessProxyReportsOriginTheAPIStores runs the api-mode plugin
// host and a proxy-mode plugin host in one process against the shared
// database and event bus, with the resident fixture installed once:
//
//   - the proxy rehydrates the plugin from plugin_archives, runs it under the
//     node:<id> state scope and reports it as host node:<id>;
//   - the API's admin connect fans out to the proxy over its bearer route and
//     the proxy's overlay origin comes back in the report;
//   - the proxy's /health carries that origin and the node health update
//     stores it on the stream_nodes row, where ClientURLFor hands it to
//     clients on the provider's path;
//   - disabling the installation on the API side reaches the proxy through
//     the bus and stops its instance.
func TestNetworkAccessProxyReportsOriginTheAPIStores(t *testing.T) {
	pool := multinodeTestPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const jwtSecret = "multinode-network-access-secret"
	setServerSetting(t, pool, "auth.jwt_secret", jwtSecret)

	cipher, err := secret.New(bytes.Repeat([]byte("k"), secret.MinMasterKeyLen))
	if err != nil {
		t.Fatal(err)
	}
	bus := newMemoryEventBus()

	// --- API host: install and enable the fixture -------------------------
	installationStore := plugins.NewInstallationStore(pool)
	runtimeConfigStore := plugins.NewRuntimeConfigStore(pool, cipher)
	repositoryStore := plugins.NewRepositoryStore(pool)
	installer := plugins.NewInstaller(installationStore, plugins.InstallerOptions{BaseDir: t.TempDir()})
	apiBroker := netaccess.NewBroker()
	apiHost := pluginhost.NewHost(pluginhost.Config{
		Logger: hclog.NewNullLogger(),
		HostInfo: func(context.Context) (pluginhost.HostInfo, error) {
			return pluginhost.HostInfo{Role: pluginhost.HostRoleAPI, Name: "api"}, nil
		},
		InstanceState: plugins.NewInstanceStateStore(pool, cipher).ForScope(plugins.HostScopeAPI),
		NetworkAccess: apiBroker,
	})
	apiService := plugins.NewService(repositoryStore, installationStore, runtimeConfigStore,
		plugins.NewCatalogService(repositoryStore, plugins.CatalogServiceOptions{SiloAPIVersion: plugins.DefaultSiloAPIVersion}),
		installer, plugins.NewHostAdapter(apiHost))
	apiHost.SetExitHandler(apiService.HandleResidentExit)
	apiService.SetNetworkAccessHostInfo(func(context.Context) (pluginhost.HostInfo, error) {
		return pluginhost.HostInfo{Role: pluginhost.HostRoleAPI, Name: "api"}, nil
	})
	apiService.SetNetworkAccessStatusSink(apiBroker)
	apiService.PublishLifecycleChanges(bus)
	t.Cleanup(func() {
		stop, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = apiService.StopResidents(stop)
		_ = apiHost.Shutdown(stop)
	})

	result, err := apiService.InstallBinaryUpload(ctx, buildResidentFixtureBinary(t))
	if err != nil {
		t.Fatalf("install fixture: %v", err)
	}
	installationID := result.Installation.ID
	t.Cleanup(func() { _ = installationStore.Delete(context.Background(), installationID) })
	enabled := true
	if err := installationStore.Update(ctx, installationID, plugins.UpdateInstallationInput{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	apiService.OnLifecycleChange(ctx)

	// --- Proxy node row and listener ---------------------------------------
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := "http://" + listener.Addr().String()
	nodeRepo := nodepool.NewRepository(pool)
	node, err := nodeRepo.Create(ctx, nodepool.CreateNodeInput{Name: "proxy-1", Type: nodepool.NodeTypeProxy, URL: proxyURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nodeRepo.Delete(context.Background(), node.ID) })
	nodeScope := plugins.NodeHostScope(int64(node.ID))

	// The proxy shares this test's filesystem, so drop the installed files to
	// prove it rehydrates the plugin from plugin_archives on its own, into
	// its own cache dir rather than the API server's install path.
	if err := os.RemoveAll(filepath.Dir(result.Installation.InstallPath)); err != nil {
		t.Fatal(err)
	}
	proxyCacheDir := filepath.Join(t.TempDir(), "proxy-plugins")

	watcher := nodeconfig.NewWatcher(pool, cipher, bus, nodeconfig.BootstrapOverrides{
		Listen: listener.Addr().String(), Mode: "proxy", NodeURL: proxyURL, NodeName: "proxy-1",
	})
	if err := watcher.Start(ctx); err != nil {
		t.Fatalf("watcher start: %v", err)
	}
	if id, ok := watcher.NodeRowID(); !ok || id != node.ID {
		t.Fatalf("watcher resolved node row %d (ok=%v), want %d", id, ok, node.ID)
	}

	proxyPlugins := newProxyPluginHost(ctx, pool, cipher, bus, watcher, "proxy-1", listener.Addr().String(), proxyCacheDir)
	proxyServer := proxy.NewServer(watcher, nil)
	proxyServer.SetIngressTokens(proxyPlugins.broker.Registry)
	proxyServer.SetNetworkAccessStatus(proxyPlugins.broker.Status)
	proxyServer.SetNetworkAccessProviderHost(proxyPlugins.service)
	httpServer := &http.Server{Handler: proxyServer.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	hooks := proxyPlugins.hooks()
	hooks.afterListen()
	t.Cleanup(func() {
		stop, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		hooks.beforeDrain(stop)
		_ = httpServer.Shutdown(stop)
	})

	// --- API fan-out --------------------------------------------------------
	nodeHandler := handlers.NewNodeHandler(nodeRepo, nil, nil, nodeRepo, bus, nil, jwtSecret)
	apiService.SetNetworkAccessNodes(nodeHandler)

	waitFor(t, "proxy resident running", 60*time.Second, func() bool {
		state, ok := proxyPlugins.service.Residents().State(installationID)
		return ok && state.State == plugins.ResidentRunning
	})
	if matches, _ := filepath.Glob(filepath.Join(proxyCacheDir, "silo.test.resident", "0.1.0", "*", "plugin")); len(matches) != 1 {
		t.Fatalf("proxy cache dir holds %v, want one rehydrated binary", matches)
	}
	if _, err := os.Stat(result.Installation.InstallPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proxy wrote to the API server's install path: %v", err)
	}

	report, err := apiService.ConnectNetworkAccess(ctx, "stub", []string{nodeScope})
	if err != nil {
		t.Fatalf("connect on the proxy: %v", err)
	}
	var proxyRow *plugins.NetworkAccessHostStatus
	for i := range report.Hosts {
		if report.Hosts[i].Host.ID == nodeScope {
			proxyRow = &report.Hosts[i]
		}
	}
	if proxyRow == nil {
		t.Fatalf("proxy host %s missing from report %+v", nodeScope, report.Hosts)
	}
	if proxyRow.Host.Role != pluginhost.HostRoleProxy || proxyRow.Host.Name != "proxy-1" {
		t.Fatalf("proxy host = %+v", proxyRow.Host)
	}
	if proxyRow.Status.State != netaccess.StateConnected || proxyRow.Status.Origin != "https://silo.stub.test" || proxyRow.Status.InstallationID != installationID {
		t.Fatalf("proxy status after connect = %+v", proxyRow.Status)
	}

	// --- Health pull stores the origin on the node row ----------------------
	healthy, activeJobs, egress, _, lastStats, networkAccess := nodepool.CheckNode(ctx, node)
	if !healthy {
		t.Fatal("proxy reported unhealthy")
	}
	if origin, ok := networkAccess.ConnectedOrigin("stub"); !ok || origin != "https://silo.stub.test" {
		t.Fatalf("health network_access = %+v", networkAccess)
	}
	if err := nodeRepo.UpdateHealth(ctx, node.ID, node.URL, healthy, activeJobs, egress, lastStats, networkAccess); err != nil {
		t.Fatal(err)
	}
	stored, err := nodeRepo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.ClientURLFor(netaccess.Path{Provider: "stub"}); got != "https://silo.stub.test" {
		t.Fatalf("ClientURLFor(stub) = %q from row %+v", got, stored.NetworkAccess)
	}
	if got := stored.ClientURLFor(netaccess.Path{}); got != proxyURL {
		t.Fatalf("ClientURLFor(default) = %q, want %q", got, proxyURL)
	}

	// The proxy validates the ingress token it issued to its own instance,
	// and refuses a forged one.
	token, ok := proxyPlugins.broker.IngressToken(installationID)
	if !ok {
		t.Fatal("proxy issued no ingress token")
	}
	for _, tc := range []struct {
		token string
		want  int
	}{{token, http.StatusOK}, {"forged", http.StatusForbidden}} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL+"/api/v1/health", nil)
		req.Header.Set(netaccess.IngressTokenHeader, tc.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("health with token %q: %d, want %d", tc.token, resp.StatusCode, tc.want)
		}
	}

	// --- Lifecycle propagation ----------------------------------------------
	disabled := false
	if err := installationStore.Update(ctx, installationID, plugins.UpdateInstallationInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	apiService.OnLifecycleChange(ctx)
	waitFor(t, "proxy resident stopped after the disable", 60*time.Second, func() bool {
		_, tracked := proxyPlugins.service.Residents().State(installationID)
		_, clientErr := proxyPlugins.host.Client(installationID)
		return !tracked && errors.Is(clientErr, pluginhost.ErrClientNotFound)
	})
	if origins := proxyPlugins.broker.Status.ConnectedOrigins(); len(origins) != 0 {
		t.Fatalf("proxy still advertises origins after the disable: %v", origins)
	}
	// The next health pull clears the row's origin.
	healthy, activeJobs, egress, _, lastStats, networkAccess = nodepool.CheckNode(ctx, node)
	if !healthy || len(networkAccess) != 0 {
		t.Fatalf("health after disable: healthy=%v network_access=%+v", healthy, networkAccess)
	}
	if err := nodeRepo.UpdateHealth(ctx, node.ID, node.URL, healthy, activeJobs, egress, lastStats, networkAccess); err != nil {
		t.Fatal(err)
	}
	stored, err = nodeRepo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.ClientURLFor(netaccess.Path{Provider: "stub"}); got != "" {
		t.Fatalf("ClientURLFor(stub) after disable = %q", got)
	}
}
