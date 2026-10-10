package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestConfigureS3Clients_SetsCORSOnPublicAssetsBucket(t *testing.T) {
	publicServer := newS3BucketRecorder(t)

	cfg := &config.Config{
		S3: config.S3Config{
			Public: config.S3PublicAssetsSettings{
				S3BucketSettings: config.S3BucketSettings{
					Endpoint:  publicServer.URL(),
					Region:    "us-east-1",
					Bucket:    "public-assets",
					AccessKey: "test",
					SecretKey: "test",
					PathStyle: true,
				},
			},
		},
	}

	deps := &api.Dependencies{}
	configureS3Clients(cfg, deps)

	if deps.S3Public == nil {
		t.Fatal("S3Public should be configured")
	}
	if got := publicServer.CORSRequests(); got != 1 {
		t.Fatalf("public assets bucket CORS requests = %d, want 1", got)
	}
}

func TestWaitForShutdownWorkWaitsForEveryCleanup(t *testing.T) {
	one := make(chan struct{})
	two := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- waitForShutdownWork(context.Background(), []<-chan struct{}{one, two})
	}()

	close(one)
	select {
	case err := <-done:
		t.Fatalf("wait returned before every cleanup completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(two)
	if err := <-done; err != nil {
		t.Fatalf("waitForShutdownWork: %v", err)
	}
}

func TestRunShutdownWorkWithTimeoutUsesFreshContext(t *testing.T) {
	called := false
	err := runShutdownWorkWithTimeout(100*time.Millisecond, func(ctx context.Context) error {
		called = true
		if err := ctx.Err(); err != nil {
			t.Fatalf("cleanup context was already canceled: %v", err)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 {
			t.Fatalf("cleanup context deadline = %v, ok=%v", deadline, ok)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("runShutdownWorkWithTimeout: %v", err)
	}
	if !called {
		t.Fatal("cleanup work was not called")
	}
}

func TestRunShutdownWorkWithTimeoutHonorsDeadline(t *testing.T) {
	err := runShutdownWorkWithTimeout(10*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runShutdownWorkWithTimeout error = %v, want deadline exceeded", err)
	}
}

func TestWaitForShutdownWorkHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForShutdownWork(ctx, []<-chan struct{}{make(chan struct{})}); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForShutdownWork error = %v, want context cancellation", err)
	}
}

func TestConfigureS3Clients_PassesPublicKeyPrefix(t *testing.T) {
	publicServer := newS3BucketRecorder(t)

	cfg := &config.Config{
		S3: config.S3Config{
			Public: config.S3PublicAssetsSettings{
				S3BucketSettings: config.S3BucketSettings{
					Endpoint:  publicServer.URL(),
					Region:    "us-east-1",
					Bucket:    "public-assets",
					KeyPrefix: "silo/dev",
					AccessKey: "test",
					SecretKey: "test",
					PathStyle: true,
				},
			},
		},
	}

	deps := &api.Dependencies{}
	configureS3Clients(cfg, deps)

	if deps.S3Public == nil {
		t.Fatal("S3Public should be configured")
	}

	url, err := deps.S3Public.PublicURL(deps.S3Public.Bucket(), "catalog-seeds/export.json.gz")
	if err != nil {
		t.Fatalf("PublicURL() returned error: %v", err)
	}
	if !strings.Contains(url, "/silo/dev/catalog-seeds/export.json.gz") {
		t.Fatalf("PublicURL() = %q, want prefixed path", url)
	}
}

type s3BucketRecorder struct {
	server       *httptest.Server
	mu           sync.Mutex
	corsRequests int
}

func newS3BucketRecorder(t *testing.T) *s3BucketRecorder {
	t.Helper()

	recorder := &s3BucketRecorder{}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()

		if r.Method == http.MethodPut && r.URL.Query().Has("cors") {
			recorder.mu.Lock()
			recorder.corsRequests++
			recorder.mu.Unlock()
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(recorder.server.Close)

	return recorder
}

func (r *s3BucketRecorder) URL() string {
	return r.server.URL
}

func (r *s3BucketRecorder) CORSRequests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.corsRequests
}

func TestBuildLiveSessionSyncOutputFormat(t *testing.T) {
	got := buildLiveSessionSync(&playback.Session{OutputContainer: "fmp4", OutputProtocol: "hls"}, "node")
	if got.OutputContainer != "fmp4" || got.OutputProtocol != "hls" {
		t.Fatal("output format was lost by session sync")
	}
}

// TestBuildLiveSessionSync_UsesTransportPlayMethod verifies sync reports the active transport method.
func TestBuildLiveSessionSync_UsesTransportPlayMethod(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		session playback.Session
		want    string
	}{
		{
			name: "transcode transport remains transcode when base method is remux",
			session: playback.Session{
				ID:                   "session-1",
				UserID:               7,
				ProfileID:            "profile-1",
				MediaFileID:          42,
				RequestedMediaFileID: 41,
				PlayMethod:           playback.PlayTranscode,
				BasePlayMethod:       playback.PlayRemux,
				TranscodeHWAccel:     "qsv",
				ToneMapMode:          tonemap.ModeHardware,
				Position:             125.5,
				IsPaused:             true,
			},
			want: "transcode",
		},
		{
			name: "remux transport stays remux",
			session: playback.Session{
				ID:                   "session-2",
				UserID:               8,
				ProfileID:            "profile-2",
				MediaFileID:          99,
				RequestedMediaFileID: 99,
				PlayMethod:           playback.PlayRemux,
				BasePlayMethod:       playback.PlayRemux,
			},
			want: "remux",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := buildLiveSessionSync(&tc.session, "node-a")
			if got.PlayMethod != tc.want {
				t.Fatalf("PlayMethod = %q, want %q", got.PlayMethod, tc.want)
			}
			if got.ReportingNode != "node-a" {
				t.Fatalf("ReportingNode = %q, want %q", got.ReportingNode, "node-a")
			}
			if got.SessionID != tc.session.ID {
				t.Fatalf("SessionID = %q, want %q", got.SessionID, tc.session.ID)
			}
			if got.ProfileID != tc.session.ProfileID {
				t.Fatalf("ProfileID = %q, want %q", got.ProfileID, tc.session.ProfileID)
			}
			if got.PositionSeconds != tc.session.Position {
				t.Fatalf("PositionSeconds = %v, want %v", got.PositionSeconds, tc.session.Position)
			}
			if got.IsPaused != tc.session.IsPaused {
				t.Fatalf("IsPaused = %v, want %v", got.IsPaused, tc.session.IsPaused)
			}
			if got.TranscodeHWAccel != tc.session.TranscodeHWAccel {
				t.Fatalf("TranscodeHWAccel = %q, want %q", got.TranscodeHWAccel, tc.session.TranscodeHWAccel)
			}
			if got.ToneMapMode != string(tc.session.ToneMapMode) {
				t.Fatalf("ToneMapMode = %q, want %q", got.ToneMapMode, tc.session.ToneMapMode)
			}
		})
	}
}

type failingWatchSyncCapabilityStore struct{}

func (failingWatchSyncCapabilityStore) ListEnabled(context.Context) ([]*plugins.Installation, error) {
	return []*plugins.Installation{{ID: 2, Enabled: true, Kind: plugins.KindPlugin}}, nil
}

func (failingWatchSyncCapabilityStore) ListCapabilities(context.Context, int) ([]*plugins.Capability, error) {
	return nil, errors.New("database unavailable")
}

type staticWatchSyncCapabilityStore struct {
	capabilities []*plugins.Capability
}

func (s staticWatchSyncCapabilityStore) ListEnabled(context.Context) ([]*plugins.Installation, error) {
	return []*plugins.Installation{{ID: 4, Enabled: true, Kind: plugins.KindPlugin}}, nil
}

func (s staticWatchSyncCapabilityStore) ListCapabilities(context.Context, int) ([]*plugins.Capability, error) {
	return s.capabilities, nil
}

func TestReloadWatchSyncPluginProvidersPreservesConnectionForm(t *testing.T) {
	manifest := &pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{{
		Type: "watch_sync_provider.v1", Id: "floppy", DisplayName: "Floppy",
		ConfigSchema: []*pluginv1.ConfigSchema{{
			Key: "floppy", Title: "Your Floppy server", Required: true,
			JsonSchema: `{"type":"object","properties":{"base_url":{"type":"string"}},"required":["base_url"]}`,
			AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{{
				Key: "base_url", Label: "Server URL", Required: true,
				Control: pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT,
			}}},
		}},
		WatchSyncProvider: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods: []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		},
	}}}
	records, err := plugins.CapabilityRecordsFromManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := make([]*plugins.Capability, 0, len(records))
	for i := range records {
		record := records[i]
		capabilities = append(capabilities, &record)
	}

	registry := watchsync.NewRegistry()
	if err := reloadWatchSyncPluginProviders(
		context.Background(), registry, staticWatchSyncCapabilityStore{capabilities: capabilities}, &plugins.Service{}, nil, nil,
	); err != nil {
		t.Fatal(err)
	}
	provider, ok := registry.Get("plugin:4:floppy")
	if !ok {
		t.Fatal("Floppy provider was not registered")
	}
	configurable, ok := provider.(interface {
		ConnectionConfigSchema() []plugins.ConfigSchemaView
	})
	if !ok {
		t.Fatal("Floppy provider does not expose connection configuration")
	}
	schemas := configurable.ConnectionConfigSchema()
	if len(schemas) != 1 || schemas[0].AdminForm == nil || len(schemas[0].AdminForm.Fields) != 1 ||
		schemas[0].AdminForm.Fields[0].Control != "TEXT" {
		t.Fatalf("connection config schema = %#v", schemas)
	}
}

func TestReloadWatchSyncPluginProvidersDropsStaleProvidersOnCapabilityReadFailure(t *testing.T) {
	registry := watchsync.NewRegistry()
	provider, err := watchsync.NewPluginProvider(watchsync.PluginProviderOptions{
		InstallationID: 1,
		ProviderKey:    "plugin:1:tracker",
		CapabilityID:   "tracker",
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
			ExportWatched: true,
		},
		ResolveClient: func(context.Context, int, string) (watchsync.WatchSyncPluginClient, error) {
			return nil, errors.New("not used")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}

	if err := reloadWatchSyncPluginProviders(
		context.Background(), registry, failingWatchSyncCapabilityStore{}, &plugins.Service{}, nil, nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get(provider.Key()); ok {
		t.Fatalf("stale provider %q remained registered", provider.Key())
	}
}

// The sampler treats the returned root set as the whole truth: paths outside it
// are pruned — losing their cached capacity readings — and omitted from the
// sample. Returning nothing on a transient database error would therefore blank
// every library mount from the admin panel and from Prometheus, and leave the
// next pass reporting them unavailable until fresh probes land, all while the
// mounts are healthy.
func TestCachedLibraryPathsReusesTheLastGoodSetOnError(t *testing.T) {
	var (
		paths []string
		err   error
	)
	provider := cachedLibraryPaths(func(context.Context) ([]string, error) { return paths, err })

	paths = []string{"/mnt/movies", "/mnt/shows"}
	if got := provider(context.Background()); !slices.Equal(got, paths) {
		t.Fatalf("first read = %v, want %v", got, paths)
	}

	paths, err = nil, errors.New("database is not answering")
	if got := provider(context.Background()); !slices.Equal(got, []string{"/mnt/movies", "/mnt/shows"}) {
		t.Fatalf("read after an error = %v, want the last good set", got)
	}

	// Recovery replaces it rather than merging.
	paths, err = []string{"/mnt/movies"}, nil
	if got := provider(context.Background()); !slices.Equal(got, []string{"/mnt/movies"}) {
		t.Fatalf("read after recovery = %v, want the fresh set", got)
	}
}

// An empty set the database actually returned is a real answer: an operator who
// removed their last library has no roots, and holding the old ones would keep
// reporting mounts the deployment no longer has.
func TestCachedLibraryPathsCachesADeliberateEmptyResult(t *testing.T) {
	var (
		paths = []string{"/mnt/movies"}
		err   error
	)
	provider := cachedLibraryPaths(func(context.Context) ([]string, error) { return paths, err })
	provider(context.Background())

	paths = nil
	if got := provider(context.Background()); len(got) != 0 {
		t.Fatalf("read = %v, want the deliberate empty result", got)
	}
	// And that empty result is what a later failure falls back to.
	err = errors.New("database is not answering")
	if got := provider(context.Background()); len(got) != 0 {
		t.Fatalf("read after an error = %v, want the cached empty result", got)
	}
}

// The cache is what a failed read falls back to, so a caller scribbling on the
// slice it was handed must not be able to corrupt it — in either direction: the
// value returned from a successful read, or the one returned from the fallback
// itself.
func TestCachedLibraryPathsDoesNotShareItsCachedSlice(t *testing.T) {
	failing := false
	provider := cachedLibraryPaths(func(context.Context) ([]string, error) {
		if failing {
			return nil, errors.New("database is not answering")
		}
		return []string{"/mnt/movies"}, nil
	})

	provider(context.Background())[0] = "/tmp/clobbered"

	failing = true
	fallback := provider(context.Background())
	if !slices.Equal(fallback, []string{"/mnt/movies"}) {
		t.Fatalf("fallback = %v, want the cache untouched by the caller's mutation", fallback)
	}

	fallback[0] = "/tmp/clobbered-again"
	if got := provider(context.Background()); !slices.Equal(got, []string{"/mnt/movies"}) {
		t.Fatalf("fallback = %v, want the cache untouched by a mutation of an earlier fallback", got)
	}
}

// The stored capability payload has to be the node's own bytes.
//
// Re-marshaling the decoded struct drops every field this build has no member
// for, which is exactly what happens during a rolling upgrade where a node is
// newer than the API reading it — and the truncation is then stored under the
// node's own hash. After the API is upgraded, the sweep sees the hashes agree
// and never refetches, so the durable inventory stays missing fields the new
// code reads until something unrelated moves the hash.
func TestNodeCapabilityFetcherStoresTheNodesOwnBytes(t *testing.T) {
	const body = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"capability_hash":"sha256:abc","a_field_this_build_has_never_heard_of":{"nested":[1,2,3]}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	payload, hash, err := nodeCapabilityFetcher("secret", nodeCapabilityProbeBudget(nil))(
		context.Background(), &nodepool.Node{ID: 1, URL: server.URL})
	if err != nil {
		t.Fatalf("nodeCapabilityFetcher: %v", err)
	}
	if hash != "sha256:abc" {
		t.Fatalf("hash = %q, want the node's own", hash)
	}

	var stored map[string]any
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatalf("stored payload is not valid JSON: %v (%s)", err, payload)
	}
	if _, ok := stored["a_field_this_build_has_never_heard_of"]; !ok {
		t.Fatalf("a field this build does not know was dropped, but its hash was kept: %s", payload)
	}
	if stored["resolved"] != "nvenc" {
		t.Fatalf("resolved = %v, want the report's own value", stored["resolved"])
	}
}

// A cold node's answer runs the whole FFmpeg probe matrix, and that matrix grows
// with the configured device count: a node with two render devices legitimately
// advertises a request budget past two minutes. Bounding every fetch at a fixed
// two minutes abandons such a node mid-probe and reports a failure for a node
// operating inside its published contract.
func TestNodeCapabilityProbeBudgetTracksTheConfiguredDevices(t *testing.T) {
	single := &config.Config{}
	single.Playback.HWAccel, single.Playback.HWDevice = "qsv", "/dev/dri/renderD128"
	pair := &config.Config{}
	pair.Playback.HWAccel, pair.Playback.HWDevice = "qsv", "/dev/dri/renderD128,/dev/dri/renderD129"

	clusterNode := &nodepool.Node{ID: 1, URL: "http://gpu-1"}
	oneDevice := nodeCapabilityProbeBudget(func() *config.Config { return single })(clusterNode)
	twoDevices := nodeCapabilityProbeBudget(func() *config.Config { return pair })(clusterNode)

	if want := playback.CapabilityRequestTimeout("qsv", pair.Playback.HWDevice); twoDevices != want {
		t.Fatalf("two-device budget = %v, want the node's own advertised %v", twoDevices, want)
	}
	if twoDevices <= nodeCapabilityRequestTimeout {
		t.Fatalf("two-device budget = %v, want more than the %v floor — that is the case that was being cut short",
			twoDevices, nodeCapabilityRequestTimeout)
	}
	if twoDevices <= oneDevice {
		t.Fatalf("two-device budget %v is not above the one-device %v; the budget does not track the matrix",
			twoDevices, oneDevice)
	}

	// A node's own override wins over the cluster setting, because the worker
	// probes the policy it will actually run. This is the case a cluster-wide
	// read cannot see: one device configured centrally, two on this node.
	twoDeviceOverride := pair.Playback.HWDevice
	overridden := &nodepool.Node{ID: 1, URL: "http://gpu-1", HWDeviceOverride: &twoDeviceOverride}
	if got := nodeCapabilityProbeBudget(func() *config.Config { return single })(overridden); got != twoDevices {
		t.Fatalf("overridden node budget = %v, want the two-device %v its own policy needs", got, twoDevices)
	}

	// An override set to the empty string means "inherit", not "no devices".
	empty := ""
	inheriting := &nodepool.Node{ID: 1, URL: "http://gpu-1", HWDeviceOverride: &empty, HWAccelOverride: &empty}
	if got := nodeCapabilityProbeBudget(func() *config.Config { return pair })(inheriting); got != twoDevices {
		t.Fatalf("inheriting node budget = %v, want the cluster's %v", got, twoDevices)
	}

	// Nothing configured, no live config, or no node at all still gets a usable
	// bound rather than zero.
	if got := nodeCapabilityProbeBudget(nil)(clusterNode); got < nodeCapabilityRequestTimeout {
		t.Fatalf("budget with no configuration = %v, want at least the %v floor", got, nodeCapabilityRequestTimeout)
	}
	if got := nodeCapabilityProbeBudget(func() *config.Config { return nil })(nil); got < nodeCapabilityRequestTimeout {
		t.Fatalf("budget with a nil config and nil node = %v, want at least the %v floor", got, nodeCapabilityRequestTimeout)
	}
}

// The backstop the health sweep puts around the same fetch must never be the
// thing that cuts it short, or the budget above is decorative.
func TestCapabilityFetchBackstopExceedsTheAdvertisedBudget(t *testing.T) {
	pair := &config.Config{}
	pair.Playback.HWAccel, pair.Playback.HWDevice = "qsv", "/dev/dri/renderD128,/dev/dri/renderD129"
	budget := nodeCapabilityProbeBudget(func() *config.Config { return pair })(&nodepool.Node{ID: 1, URL: "http://gpu-1"})

	if nodepool.CapabilityRefreshTimeout <= budget {
		t.Fatalf("health sweep backstop %v does not exceed the %v a two-device node advertises",
			nodepool.CapabilityRefreshTimeout, budget)
	}
}

type firstPartyWatchSyncStore struct {
	installations []*plugins.Installation
	capabilities  []*plugins.Capability
}

func (s firstPartyWatchSyncStore) ListEnabled(context.Context) ([]*plugins.Installation, error) {
	return s.installations, nil
}

func (s firstPartyWatchSyncStore) ListCapabilities(context.Context, int) ([]*plugins.Capability, error) {
	return s.capabilities, nil
}

// fakeWatchSyncPluginService treats every installation as Silo-managed. Its
// seeding is create-only like the real one, and a seed that saves config runs
// onSeed, as the lifecycle hook a config save fires does.
type fakeWatchSyncPluginService struct {
	cacheInvalidations int
	onSeed             func(context.Context)
	seeded             []int
	created            bool
}

func (f *fakeWatchSyncPluginService) InvalidateInstallationCache() {
	f.cacheInvalidations++
}

func (f *fakeWatchSyncPluginService) InstalledFromSiloRepository(context.Context, *plugins.Installation) (bool, error) {
	return true, nil
}

func (f *fakeWatchSyncPluginService) WatchSyncProviderClient(context.Context, int, string) (*pluginhost.WatchSyncProviderClient, error) {
	return nil, errors.New("not used")
}

func (f *fakeWatchSyncPluginService) WatchSyncProviderConfig(context.Context, int) (*pluginv1.WatchSyncProviderConfig, error) {
	return &pluginv1.WatchSyncProviderConfig{}, nil
}

func (f *fakeWatchSyncPluginService) WatchSyncConfigReady(context.Context, int) (bool, error) {
	return true, nil
}

func (f *fakeWatchSyncPluginService) SeedGlobalConfig(ctx context.Context, installationID int, _ string, _ map[string]any) (bool, error) {
	f.seeded = append(f.seeded, installationID)
	if f.created {
		return false, nil
	}
	f.created = true
	if f.onSeed != nil {
		f.onSeed(ctx)
	}
	return true, nil
}

type fakeWatchSyncMigrationStore struct {
	mu       sync.Mutex
	settings map[string]string
}

func (f *fakeWatchSyncMigrationStore) GetServerSetting(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings[key], nil
}

func (f *fakeWatchSyncMigrationStore) SetServerSetting(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings[key] = value
	return nil
}

func (f *fakeWatchSyncMigrationStore) HasConnections(context.Context, string) (bool, error) {
	return true, nil
}

func traktPluginCapabilities(t *testing.T) []*plugins.Capability {
	t.Helper()
	records, err := plugins.CapabilityRecordsFromManifest(&pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{{
		Type: "watch_sync_provider.v1", Id: "trakt", DisplayName: "Trakt",
		WatchSyncProvider: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE},
			ExportWatched: true,
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := make([]*plugins.Capability, 0, len(records))
	for i := range records {
		record := records[i]
		capabilities = append(capabilities, &record)
	}
	return capabilities
}

// Two API nodes can auto-install the same first-party plugin at once. The
// older installation keeps the legacy key, and the reload still succeeds.
func TestReloadWatchSyncPluginProvidersKeepsOneFirstPartyInstallation(t *testing.T) {
	store := firstPartyWatchSyncStore{
		installations: []*plugins.Installation{
			{ID: 9, PluginID: "silo.watchprovider.trakt", Enabled: true, Kind: plugins.KindPlugin},
			{ID: 7, PluginID: "silo.watchprovider.trakt", Enabled: true, Kind: plugins.KindPlugin},
		},
		capabilities: traktPluginCapabilities(t),
	}
	registry := watchsync.NewRegistry()
	migration := &fakeWatchSyncMigrationStore{settings: map[string]string{"watchsync.trakt.client_id": "id"}}
	service := &fakeWatchSyncPluginService{}
	if err := reloadWatchSyncPluginProviders(context.Background(), registry, store, service, nil, migration); err != nil {
		t.Fatal(err)
	}
	if service.cacheInvalidations != 1 {
		t.Fatalf("cache invalidations = %d, want one before reloading providers", service.cacheInvalidations)
	}
	summaries := registry.List()
	if len(summaries) != 1 || summaries[0].Key != "trakt" {
		t.Fatalf("registered providers = %#v, want only trakt", summaries)
	}
	if !slices.Equal(service.seeded, []int{7}) {
		t.Fatalf("seeded installations = %v, want the older one", service.seeded)
	}
	if migration.settings["watchsync.plugin_migration.trakt"] == "" {
		t.Fatal("migration was not recorded")
	}
}

// Seeding saves plugin config, and the save fires the lifecycle hook that
// reloads again. The nested reload must not wait on the outer one.
func TestReloadWatchSyncPluginProvidersSeedsOutsideTheReloadLock(t *testing.T) {
	store := firstPartyWatchSyncStore{
		installations: []*plugins.Installation{{ID: 7, PluginID: "silo.watchprovider.trakt", Enabled: true, Kind: plugins.KindPlugin}},
		capabilities:  traktPluginCapabilities(t),
	}
	registry := watchsync.NewRegistry()
	migration := &fakeWatchSyncMigrationStore{settings: map[string]string{
		"watchsync.trakt.client_id":     "id",
		"watchsync.trakt.client_secret": "secret",
	}}
	service := &fakeWatchSyncPluginService{}
	nested := make(chan error, 1)
	service.onSeed = func(ctx context.Context) {
		nested <- reloadWatchSyncPluginProviders(ctx, registry, store, service, nil, migration)
	}
	done := make(chan error, 1)
	go func() {
		done <- reloadWatchSyncPluginProviders(context.Background(), registry, store, service, nil, migration)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reload deadlocked while seeding")
	}
	if err := <-nested; err != nil {
		t.Fatal(err)
	}
	// The nested reload found the app config already saved, so its seed wrote
	// nothing and fired no further reload. Later reloads skip seeding.
	if len(service.seeded) != 2 {
		t.Fatalf("seed calls = %d, want the outer seed and one no-op nested seed", len(service.seeded))
	}
	if err := reloadWatchSyncPluginProviders(context.Background(), registry, store, service, nil, migration); err != nil {
		t.Fatal(err)
	}
	if len(service.seeded) != 2 {
		t.Fatalf("seed calls after seeding finished = %d, want 2", len(service.seeded))
	}
}
