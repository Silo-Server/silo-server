package plugins

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
)

func TestRuntimeGenerationCountsConcurrentConfigSavesAndRestarts(t *testing.T) {
	pool := builtinGuardTestPool(t)
	store := NewInstallationStore(pool)
	configs := NewRuntimeConfigStore(pool, instanceStateTestCipher(t))
	id := seedResidentQueryInstallation(t, pool, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const requests = 16
	errs := make(chan error, requests)
	start := make(chan struct{})
	for i := range requests {
		go func() {
			<-start
			if i%2 == 0 {
				saved, err := configs.CompareAndSwapGlobalConfig(ctx, id, fmt.Sprintf("config-%d", i), map[string]any{"enabled": true}, nil)
				if err == nil && !saved {
					err = fmt.Errorf("new config %d was not saved", i)
				}
				errs <- err
				return
			}
			errs <- store.Update(ctx, id, UpdateInstallationInput{Restart: true})
		}()
	}
	close(start)
	for range requests {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent config save/restart: %v", err)
		}
	}
	installation, err := store.GetByID(ctx, id)
	if err != nil || installation.RuntimeGeneration != requests {
		t.Fatalf("runtime generation after %d requests: %+v, %v", requests, installation, err)
	}
}

func newResidentDatabaseFixture(t *testing.T, opts ResidentOptions) (*residentFixture, *InstallationStore, *RuntimeConfigStore, int) {
	t.Helper()
	pool := builtinGuardTestPool(t)
	f := newResidentFixture(t, opts)
	store := NewInstallationStore(pool)
	source := f.store.byID[5]
	installation, err := store.Create(context.Background(), CreateInstallationInput{
		PluginID: source.PluginID, Version: source.Version, InstallPath: source.InstallPath, Enabled: true,
		Capabilities: []Capability{{Type: capability.NetworkAccessProvider, ID: "stub"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installation.ID)
	})
	configs := NewRuntimeConfigStore(pool, instanceStateTestCipher(t))
	f.service.installations, f.service.configs = store, configs
	return f, store, configs, installation.ID
}

func TestResidentPollRecoversMissedConfigSave(t *testing.T) {
	f, store, configs, id := newResidentDatabaseFixture(t, ResidentOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.service.StartResidents(ctx)
	waitState(t, f.service, id, "initial running", running)
	first, err := f.host.Client(id)
	if err != nil {
		t.Fatal(err)
	}
	// This is the admin save's durable transaction; no lifecycle event reaches
	// the follower, as happens while its Redis subscription is disconnected.
	if saved, err := configs.CompareAndSwapGlobalConfig(ctx, id, "connection", map[string]any{"token": "replacement"}, nil); err != nil || !saved {
		t.Fatalf("save config: saved=%v err=%v", saved, err)
	}
	installation, err := store.GetByID(ctx, id)
	if err != nil || installation.RuntimeGeneration != 1 {
		t.Fatalf("saved generation: %+v, %v", installation, err)
	}
	if err := f.service.FollowLifecycleChanges(ctx, nil, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	waitState(t, f.service, id, "running with the saved config", func(state RuntimeState, tracked bool) bool {
		current, err := f.host.Client(id)
		return tracked && state.State == ResidentRunning && err == nil && current != first
	})
	current, _ := f.host.Client(id)
	// Provider callbacks persist their own state without requesting a restart.
	if err := configs.PutGlobalConfig(ctx, id, "connection", map[string]any{"token": "rotated"}); err != nil {
		t.Fatal(err)
	}
	f.service.OnLifecycleChange(ctx)
	if again, err := f.host.Client(id); err != nil || again != current {
		t.Fatalf("provider config write restarted its process: %v", err)
	}
	// A lost compare-and-swap race must not request another restart.
	if saved, err := configs.CompareAndSwapGlobalConfig(ctx, id, "connection", map[string]any{"token": "stale"}, nil); err != nil || saved {
		t.Fatalf("stale config save: saved=%v err=%v", saved, err)
	}
	installation, err = store.GetByID(ctx, id)
	if err != nil || installation.RuntimeGeneration != 1 {
		t.Fatalf("generation after provider/stale writes: %+v, %v", installation, err)
	}
}

func TestResidentPollRecoversMissedRestartOfFailedProvider(t *testing.T) {
	f, store, _, id := newResidentDatabaseFixture(t, ResidentOptions{MaxFailures: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.service.StartResidents(ctx)
	waitState(t, f.service, id, "initial running", running)
	f.crash(t)
	waitState(t, f.service, id, "failed", func(state RuntimeState, tracked bool) bool {
		return tracked && state.State == ResidentFailed
	})
	f.heal(t)
	// The API persists the restart but has no event bus connected to this
	// follower. The generation must clear the follower's failure budget.
	publisher := &Service{installations: store}
	if err := publisher.RestartInstallation(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := f.service.FollowLifecycleChanges(ctx, nil, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	state := waitState(t, f.service, id, "running after missed restart event", running)
	if state.RestartCount != 0 || state.LastError != "" {
		t.Fatalf("restart did not clear failure budget: %+v", state)
	}
}
