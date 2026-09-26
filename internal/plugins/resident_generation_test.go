package plugins

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// seqFakeHost is a Host whose first Start assigns its start sequence and then
// parks until the test releases it, without registering the client. That is
// the window the real host has between spawning a process and completing the
// handshake, during which a newer supervisor generation can join the
// in-flight launch through Service.ensureClient's singleflight.
type seqFakeHost struct {
	mu          sync.Mutex
	seq         atomic.Uint64
	clients     map[int]*seqFakeClient
	starts      int
	stoppedSeqs []uint64
	gate        chan struct{}
	entered     chan struct{}
	release     sync.Once
	floorReads  atomic.Int32
}

type seqFakeClient struct {
	*pluginhost.Client
	manifest *pluginv1.PluginManifest
	seq      uint64
}

func (c *seqFakeClient) Manifest() *pluginv1.PluginManifest { return c.manifest }
func (c *seqFakeClient) StartSeq() uint64                   { return c.seq }

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
	client := &seqFakeClient{manifest: req.Manifest, seq: seq}
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

// NextStartSeq mirrors pluginhost.Host. The second floor read is the newer
// generation's runStart, one instruction before it joins the in-flight
// launch, so releasing the parked leader here makes the join as likely as it
// can be without a hook inside singleflight. The assertions hold either way:
// a generation that misses the join relaunches through manifest drift.
func (h *seqFakeHost) NextStartSeq() uint64 {
	if h.floorReads.Add(1) == 2 {
		h.release.Do(func() { close(h.gate) })
	}
	return h.seq.Load()
}

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
	service.AddLifecycleHook(func(context.Context) { service.invalidateInstallationCache() })
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
