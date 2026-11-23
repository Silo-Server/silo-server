package plugins

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

func TestResidentRestartCanCancelWhileReconcileIsInProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := &Service{installations: newFakeServiceInstallationStore()}
		supervisor := newResidentSupervisor(service, ResidentOptions{MaxFailures: 1})
		supervisor.entries[5] = &residentEntry{id: 5, state: ResidentRunning}
		supervisor.reconcileMu.Lock()
		defer supervisor.reconcileMu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- supervisor.Restart(ctx, 5)
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Restart returned %v, want deadline exceeded", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("restart waited for reconciliation after its deadline")
		}
		if state, tracked := supervisor.State(5); !running(state, tracked) {
			t.Fatalf("canceled restart changed the resident state: %+v", state)
		}
	})
}

func TestResidentFailedReplacementKeepsPreviouslyRegisteredClient(t *testing.T) {
	manifest := testPluginManifest(t, "silo.vpn", "1.0.0")
	manifest.Capabilities = append(manifest.Capabilities, &pluginv1.CapabilityDescriptor{
		Type: capability.NetworkAccessProvider, Id: "vpn", DisplayName: "VPN",
		NetworkAccessProvider: &pluginv1.NetworkAccessProviderDescriptor{Provider: "vpn"},
	})
	store := installedPluginStore(t, 7, manifest)
	old := &fakePluginClient{manifest: manifest}
	host := &countingHerdHost{startResult: old}
	service := &Service{installations: store, host: host}
	ctx := t.Context()
	if _, err := service.Start(ctx, 7); err != nil {
		t.Fatal(err)
	}
	service.resident = newResidentSupervisor(service, ResidentOptions{})
	service.resident.entries[7] = &residentEntry{id: 7, state: ResidentRunning}

	host.startErr = errors.New("bind process host identity")
	if _, err := service.Start(ctx, 7); !errors.Is(err, host.startErr) {
		t.Fatalf("replacement failure = %v, want the host identity bind error", err)
	}
	if client, err := service.ensureClient(ctx, 7); err != nil || client != old {
		t.Fatalf("resident client after failed replacement = %v, %v", client, err)
	}
}

// A real resident is required to observe the ingress token revoked by Stop.
func TestResidentReplacementRevokesOldIngressWhenTheRowReadFails(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{MaxFailures: 1})
	reader := &unavailableResidentRowStore{serviceInstallationStore: f.store}
	f.service.installations = reader
	ctx := t.Context()
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "old resident running", running)
	if _, ok := f.broker.IngressToken(5); !ok {
		t.Fatal("old resident had no ingress token")
	}

	if err := f.store.Update(ctx, 5, UpdateInstallationInput{Restart: true}); err != nil {
		t.Fatal(err)
	}
	reader.unavailable.Store(true)
	f.service.reconcileLifecycle(ctx, false)
	waitState(t, f.service, 5, "replacement failed", func(state RuntimeState, tracked bool) bool {
		return tracked && state.State == ResidentFailed
	})
	if _, ok := f.broker.IngressToken(5); ok || f.host.Running(5) {
		t.Fatal("superseded resident was still serving after the replacement read failed")
	}

	reader.unavailable.Store(false)
	if err := f.service.RestartInstallation(ctx, 5); err != nil {
		t.Fatal(err)
	}
	waitState(t, f.service, 5, "replacement running after administrator restart", running)
	if _, ok := f.broker.IngressToken(5); !ok {
		t.Fatal("replacement did not receive an ingress token")
	}
}

type unavailableResidentRowStore struct {
	serviceInstallationStore
	unavailable atomic.Bool
}

func (s *unavailableResidentRowStore) GetByID(ctx context.Context, id int) (*Installation, error) {
	if s.unavailable.Load() {
		return nil, errors.New("installation row temporarily unavailable")
	}
	return s.serviceInstallationStore.GetByID(ctx, id)
}

// seqFakeHost is a Host whose first Start assigns its start sequence and then
// parks until the test releases it, without registering the client. That is
// the window the real host has between spawning a process and completing the
// handshake, during which the supervisor can observe a newer generation.
type seqFakeHost struct {
	mu          sync.Mutex
	seq         atomic.Uint64
	clients     map[int]*seqFakeClient
	starts      int
	stoppedSeqs []uint64
	gate        chan struct{}
	entered     chan struct{}
	release     sync.Once
}

type seqFakeClient struct {
	*pluginhost.Client
	manifest *pluginv1.PluginManifest
	config   []*pluginv1.ConfigEntry
	seq      uint64
}

func (c *seqFakeClient) Manifest() *pluginv1.PluginManifest { return c.manifest }
func (c *seqFakeClient) AuthProvider(string) (*pluginhost.AuthProviderClient, error) {
	return nil, nil
}

func newSeqFakeHost() *seqFakeHost {
	return &seqFakeHost{clients: map[int]*seqFakeClient{}, gate: make(chan struct{}), entered: make(chan struct{})}
}

func (h *seqFakeHost) Start(_ context.Context, req pluginhost.StartRequest) (pluginClient, error) {
	seq := h.seq.Add(1)
	h.mu.Lock()
	h.starts++
	first := h.starts == 1
	h.mu.Unlock()
	if first {
		close(h.entered)
		<-h.gate
	}
	client := &seqFakeClient{manifest: req.Manifest, config: req.Config, seq: seq}
	h.mu.Lock()
	h.clients[req.InstallationID] = client
	h.mu.Unlock()
	return client, nil
}

func (h *seqFakeHost) Client(installationID int) (pluginClient, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if client, ok := h.clients[installationID]; ok {
		return client, nil
	}
	return nil, pluginhost.ErrClientNotFound
}

func (h *seqFakeHost) Stop(installationID int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	client, ok := h.clients[installationID]
	if !ok {
		return pluginhost.ErrClientNotFound
	}
	h.stoppedSeqs = append(h.stoppedSeqs, client.seq)
	delete(h.clients, installationID)
	return nil
}

func (h *seqFakeHost) Shutdown(context.Context) error { return nil }

func (h *seqFakeHost) snapshot() (starts int, stopped []uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts, append([]uint64(nil), h.stoppedSeqs...)
}

func TestResidentSupervisorNewGenerationNeverAdoptsInFlightOlderLaunch(t *testing.T) {
	old := buildResidentFixture(t)
	store := newFakeServiceInstallationStore(&Installation{ID: 5, PluginID: "silo.test.resident", Version: "0.1.0", InstallPath: old, Enabled: true, Kind: KindPlugin})
	store.listCapabilities = []*Capability{{InstallationID: 5, Type: capability.NetworkAccessProvider, ID: "stub"}}
	host := newSeqFakeHost()
	service := &Service{installations: store, host: host}
	service.resident = newResidentSupervisor(service, ResidentOptions{MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond})
	service.AddLifecycleHook(func(context.Context) { service.InvalidateInstallationCache() })
	service.AddLifecycleHook(func(ctx context.Context) { service.resident.Reconcile(ctx) })
	t.Cleanup(func() {
		host.release.Do(func() { close(host.gate) })
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = service.StopResidents(ctx)
	})

	ctx := context.Background()
	service.StartResidents(ctx)
	select {
	case <-host.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first launch never reached the host")
	}

	// The row changes while generation 1's launch is parked before it
	// registered anything, exactly like an auto-update racing boot.
	next := buildResidentFixtureVersion(t, "0.2.0")
	version := "0.2.0"
	if err := store.Update(ctx, 5, UpdateInstallationInput{Version: &version, InstallPath: &next}); err != nil {
		t.Fatal(err)
	}
	service.OnLifecycleChange(ctx)
	host.release.Do(func() { close(host.gate) })

	state := waitState(t, service, 5, "generation 2 running", running)
	if state.LastError != "" || state.RestartCount != 0 {
		t.Fatalf("replacement recorded as a failure: %+v", state)
	}
	current, err := host.Client(5)
	if err != nil {
		t.Fatal(err)
	}
	if got := current.Manifest().GetVersion(); got != "0.2.0" {
		t.Fatalf("running version = %q, want the generation 2 release", got)
	}
	if seq := current.(*seqFakeClient).seq; seq != 2 {
		t.Fatalf("running client start seq = %d, want 2 (the launch generation 2 issued)", seq)
	}
	starts, stopped := host.snapshot()
	if starts != 2 {
		t.Fatalf("host starts = %d, want 2", starts)
	}
	if len(stopped) != 1 || stopped[0] != 1 {
		t.Fatalf("stopped seqs = %v, want the adopted generation 1 process (seq 1) stopped exactly once", stopped)
	}
}

func (h *seqFakeHost) Running(id int) bool {
	_, err := h.Client(id)
	return err == nil
}
