package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/Silo-Server/silo-server/internal/cache"
)

func TestServiceLifecycleChangeContinuesAfterHookPanic(t *testing.T) {
	var service Service
	called := false
	service.AddLifecycleHook(func(context.Context) {
		panic("boom")
	})
	service.AddLifecycleHook(func(context.Context) {
		called = true
	})

	service.OnLifecycleChange(context.Background())

	if !called {
		t.Fatal("expected later lifecycle hook to run after earlier hook panicked")
	}
}

func TestLifecyclePollRetriesOnlyFailedRefresh(t *testing.T) {
	ctx := t.Context()
	store := newFakeServiceInstallationStore(&Installation{ID: 7, Enabled: true})
	service := &Service{installations: store}
	var refreshes, otherCalls int
	loadedGeneration := int64(-1)
	fail := false
	service.AddRetryableLifecycleHook(ctx, "test registry", func(ctx context.Context) error {
		refreshes++
		if fail {
			return errors.New("capabilities temporarily unavailable")
		}
		row, err := store.GetByID(ctx, 7)
		if err != nil {
			return err
		}
		loadedGeneration = row.RuntimeGeneration
		return nil
	})
	service.AddLifecycleHook(func(context.Context) { otherCalls++ })
	service.reconcileLifecycle(ctx, false)

	fail = true
	if err := store.Update(ctx, 7, UpdateInstallationInput{Restart: true}); err != nil {
		t.Fatal(err)
	}
	service.reconcileLifecycle(ctx, false)
	if loadedGeneration != 0 {
		t.Fatalf("failed refresh replaced the previous generation: %d", loadedGeneration)
	}

	fail = false
	service.reconcileLifecycle(ctx, false)
	if loadedGeneration != 1 || otherCalls != 2 {
		t.Fatalf("retry on unchanged state: loaded generation = %d, other hook calls = %d", loadedGeneration, otherCalls)
	}
	afterRetry := refreshes
	service.reconcileLifecycle(ctx, false)
	if refreshes != afterRetry || otherCalls != 2 {
		t.Fatal("unchanged poll refreshed the registry again")
	}
}

func TestLifecyclePollSkipsBusyRuntimeAndRetriesItsStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeServiceInstallationStore(&Installation{ID: 7, Enabled: true, RuntimeGeneration: 1})
		service := &Service{installations: store, host: newSeqFakeHost()}
		service.running.store(7, installationRuntime{enabled: true})
		lock := service.runtimeLock(7)
		lock.Lock()
		done := make(chan struct{})
		go func() {
			service.reconcileInstallations(t.Context())
			close(done)
		}()
		synctest.Wait()
		select {
		case <-done:
			lock.Unlock()
		default:
			lock.Unlock()
			<-done
			t.Fatal("busy installation blocked reconciliation")
		}
		if _, ok := service.running.load(7); !ok {
			t.Fatal("poll stopped the busy installation")
		}
		service.reconcileInstallations(t.Context())
		if _, ok := service.running.load(7); ok {
			t.Fatal("later poll retained the stale runtime")
		}
	})
}

func TestLifecycleFollowerRebindsLazyAuthAfterMissedEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rootA, rootB := t.TempDir(), t.TempDir()
	manifest := connectionTestManifest(t, "silo.auth.oidc", "0.1.0")
	manifest.Capabilities = []*pluginv1.CapabilityDescriptor{{Type: "auth_provider.v1", Id: "oidc", AuthModes: []string{"oauth2"}}}
	binary := []byte("#!/bin/sh\nexit 0\n")
	manifest.Checksum = sha256Hex(binary)
	raw, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := buildBinaryPluginArchive(raw, binary)
	if err != nil {
		t.Fatal(err)
	}
	// A legacy writer used the version directory as the release directory.
	recorded := filepath.Join(rootA, manifest.GetPluginId(), manifest.GetVersion(), "plugin")
	store := newFakeServiceInstallationStore(&Installation{
		ID: 7, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(), InstallPath: recorded, Enabled: true,
	})
	store.archives = map[int]*InstallationArchive{7: {InstallationID: 7, Bytes: archived, ManifestJSON: raw, Checksum: manifest.GetChecksum()}}
	configuration := &rotatingConfigStore{}
	configuration.secret.Store("initial")
	followerHost := newSeqFakeHost()
	followerHost.release.Do(func() { close(followerHost.gate) })
	follower := &Service{installations: store, configs: configuration, host: followerHost, archiveCache: NewArchiveCacheAt(store, rootB)}
	bus := &testLifecycleBus{}
	follower.PublishLifecycleChanges(bus)
	writer := &Service{installations: store}
	writer.PublishLifecycleChanges(bus)
	changed := make(chan struct{}, 8)
	follower.AddLifecycleHook(func(context.Context) { changed <- struct{}{} })
	if err := follower.FollowLifecycleChanges(ctx, bus, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	waitForLifecycleTestSignal(t, changed)

	if _, err := follower.AuthProviderClient(ctx, 7, "oidc"); err != nil {
		t.Fatal(err)
	}
	assertRunningAuthConfig(t, followerHost, "initial")
	localPath := requireLocalInstallPath(t, follower.archiveCache, store.byID[7])
	if _, err := os.Stat(localPath); err != nil || !filepath.IsAbs(localPath) {
		t.Fatalf("follower did not rehydrate its local binary: %v", err)
	}
	if _, err := os.Stat(recorded); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("follower wrote to the writer's cache root: %v", err)
	}

	bus.drop.Store(true)
	configuration.secret.Store("rotated")
	if err := store.Update(ctx, 7, UpdateInstallationInput{Restart: true}); err != nil {
		t.Fatal(err)
	}
	writer.OnLifecycleChange(ctx)
	waitForLifecycleTestSignal(t, changed)
	if _, err := follower.AuthProviderClient(ctx, 7, "oidc"); err != nil {
		t.Fatal(err)
	}
	assertRunningAuthConfig(t, followerHost, "rotated")

	// Repeat the same receive-only reconcile the worker runs for events and
	// polls. Neither may stop the current process or publish another event.
	follower.reconcileLifecycle(ctx, false)
	follower.reconcileLifecycle(ctx, false)
	select {
	case <-changed:
		t.Fatal("an unchanged database snapshot ran lifecycle hooks again")
	default:
	}
	if starts, stopped := followerHost.snapshot(); starts != 2 || len(stopped) != 1 || stopped[0] != 1 {
		t.Fatalf("starts = %d, stopped = %v, want one replacement", starts, stopped)
	}
	if publications := bus.published.Load(); publications != 1 {
		t.Fatalf("published %d events, want only the writer's event", publications)
	}
}

func TestLazyStartRejectsConfigSupersededDuringHandshake(t *testing.T) {
	manifest := connectionTestManifest(t, "silo.auth.oidc", "0.1.0")
	store := installedPluginStore(t, 7, manifest)
	configuration := &rotatingConfigStore{}
	configuration.secret.Store("initial")
	host := newSeqFakeHost()
	t.Cleanup(func() { host.release.Do(func() { close(host.gate) }) })
	service := &Service{installations: store, configs: configuration, host: host}

	type launchResult struct {
		client pluginClient
		err    error
	}
	finished := make(chan launchResult, 1)
	go func() {
		client, err := service.ensureClient(t.Context(), 7)
		finished <- launchResult{client, err}
	}()
	waitForLifecycleTestSignal(t, host.entered)
	configuration.secret.Store("rotated")
	if err := store.Update(t.Context(), 7, UpdateInstallationInput{Restart: true}); err != nil {
		t.Fatal(err)
	}
	host.release.Do(func() { close(host.gate) })
	select {
	case result := <-finished:
		if result.err != nil || result.client.(*seqFakeClient).seq != 2 {
			t.Fatalf("request did not wait for the new generation: %v", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lazy start did not converge")
	}
	assertRunningAuthConfig(t, host, "rotated")
	if starts, stopped := host.snapshot(); starts != 2 || len(stopped) != 1 || stopped[0] != 1 {
		t.Fatalf("starts = %d, stopped = %v, want only the stale launch stopped", starts, stopped)
	}
}

type rotatingConfigStore struct {
	fakeServiceConfigStore
	secret atomic.Value
}

func (s *rotatingConfigStore) ListGlobalConfigs(context.Context, int) ([]*RuntimeConfig, error) {
	return []*RuntimeConfig{{Key: "connection", Value: map[string]any{"client_secret": s.secret.Load()}}}, nil
}

func assertRunningAuthConfig(t *testing.T, host *seqFakeHost, expected string) {
	t.Helper()
	client, err := host.Client(7)
	if err != nil {
		t.Fatal(err)
	}
	config := client.(*seqFakeClient).config
	if len(config) != 1 || config[0].GetValue().GetFields()["client_secret"].GetStringValue() != expected {
		t.Fatal("plugin start did not receive the current secret")
	}
}

func waitForLifecycleTestSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for plugin lifecycle")
	}
}

type testLifecycleBus struct {
	mu        sync.Mutex
	handler   cache.EventHandler
	drop      atomic.Bool
	published atomic.Int32
}

func (b *testLifecycleBus) Publish(_ context.Context, _ string, event cache.Event) error {
	b.published.Add(1)
	b.mu.Lock()
	handler := b.handler
	b.mu.Unlock()
	if handler != nil && !b.drop.Load() {
		handler(event)
	}
	return nil
}

func (b *testLifecycleBus) Subscribe(_ context.Context, _ string, handler cache.EventHandler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handler = handler
	return nil
}

func (*testLifecycleBus) Close() error { return nil }
