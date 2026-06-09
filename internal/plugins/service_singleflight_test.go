package plugins

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// countingHerdHost simulates the real Host: Start records the launched client so
// a later Client() hits the warm cache; it counts Start calls and can delay each
// launch to force concurrent overlap.
type countingHerdHost struct {
	mu          sync.Mutex
	current     map[int]pluginClient
	starts      int
	startResult pluginClient
	startErr    error
	startDelay  time.Duration
}

func (h *countingHerdHost) Client(id int) (pluginClient, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.current[id]; ok {
		return c, nil
	}
	return nil, pluginhost.ErrClientNotFound
}

func (h *countingHerdHost) Start(_ context.Context, req pluginhost.StartRequest) (pluginClient, error) {
	if h.startDelay > 0 {
		time.Sleep(h.startDelay)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts++
	if h.startErr != nil {
		return nil, h.startErr
	}
	if h.current == nil {
		h.current = map[int]pluginClient{}
	}
	h.current[req.InstallationID] = h.startResult
	return h.startResult, nil
}

func (h *countingHerdHost) Stop(id int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.current, id)
	return nil
}

func (h *countingHerdHost) Shutdown(context.Context) error { return nil }

func (h *countingHerdHost) startCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts
}

func TestEnsureClientSingleFlightsConcurrentLaunch(t *testing.T) {
	manifest := testPluginManifest(t, "silo.metadb", "0.0.36")
	installPath := writeInstalledPluginManifest(t, manifest)
	store := newFakeServiceInstallationStore(&Installation{
		ID: 7, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
		InstallPath: installPath, Enabled: true,
	})
	host := &countingHerdHost{
		startResult: &fakePluginClient{manifest: manifest},
		startDelay:  25 * time.Millisecond,
	}
	service := &Service{installations: store, host: host}

	const n = 20
	var wg sync.WaitGroup
	release := make(chan struct{})
	clients := make([]pluginClient, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			clients[i], errs[i] = service.ensureClient(context.Background(), 7)
		}(i)
	}
	close(release)
	wg.Wait()

	if got := host.startCount(); got != 1 {
		t.Fatalf("host.Start calls = %d, want 1 (singleflighted)", got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d err = %v", i, errs[i])
		}
		if clients[i] != host.startResult {
			t.Fatalf("caller %d got %#v, want shared launched client", i, clients[i])
		}
	}

	if _, err := service.ensureClient(context.Background(), 7); err != nil {
		t.Fatalf("post-warm ensureClient err = %v", err)
	}
	if got := host.startCount(); got != 1 {
		t.Fatalf("post-warm host.Start calls = %d, want still 1", got)
	}
}

func TestEnsureClientDistinctInstallationsLaunchIndependently(t *testing.T) {
	mA := testPluginManifest(t, "silo.metadb", "0.0.36")
	mB := testPluginManifest(t, "silo.other", "1.0.0")
	pathA := writeInstalledPluginManifest(t, mA)
	pathB := writeInstalledPluginManifest(t, mB)
	store := newFakeServiceInstallationStore(
		&Installation{ID: 7, PluginID: mA.GetPluginId(), Version: mA.GetVersion(), InstallPath: pathA, Enabled: true},
		&Installation{ID: 8, PluginID: mB.GetPluginId(), Version: mB.GetVersion(), InstallPath: pathB, Enabled: true},
	)
	host := &countingHerdHost{startResult: &fakePluginClient{manifest: mA}, startDelay: 10 * time.Millisecond}
	service := &Service{installations: store, host: host}

	var wg sync.WaitGroup
	for _, id := range []int{7, 8, 7, 8} {
		wg.Add(1)
		go func(id int) { defer wg.Done(); _, _ = service.ensureClient(context.Background(), id) }(id)
	}
	wg.Wait()

	if got := host.startCount(); got != 2 {
		t.Fatalf("host.Start calls = %d, want 2 (one per installation)", got)
	}
}

// ctxCaptureHost blocks inside Start until released, and records whether the
// launch context was canceled. It lets a test cancel the singleflight leader's
// ctx while a launch is in flight.
type ctxCaptureHost struct {
	mu          sync.Mutex
	current     map[int]pluginClient
	startResult pluginClient
	entered     chan struct{}
	proceed     chan struct{}
	sawCancel   bool
}

func (h *ctxCaptureHost) Client(id int) (pluginClient, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.current[id]; ok {
		return c, nil
	}
	return nil, pluginhost.ErrClientNotFound
}

func (h *ctxCaptureHost) Start(ctx context.Context, req pluginhost.StartRequest) (pluginClient, error) {
	close(h.entered)
	select {
	case <-ctx.Done():
		h.mu.Lock()
		h.sawCancel = true
		h.mu.Unlock()
		return nil, ctx.Err()
	case <-h.proceed:
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil {
		h.current = map[int]pluginClient{}
	}
	h.current[req.InstallationID] = h.startResult
	return h.startResult, nil
}

func (h *ctxCaptureHost) Stop(id int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.current, id)
	return nil
}

func (h *ctxCaptureHost) Shutdown(context.Context) error { return nil }

func (h *ctxCaptureHost) canceledDuringLaunch() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sawCancel
}

// TestEnsureClientLaunchIsolatedFromLeaderCancellation pins that a deduped
// launch is not coupled to the cancellation of whichever caller happened to be
// the singleflight leader: if that caller's request is canceled mid-launch, the
// shared launch (which other waiters depend on) must continue.
func TestEnsureClientLaunchIsolatedFromLeaderCancellation(t *testing.T) {
	manifest := testPluginManifest(t, "silo.metadb", "0.0.36")
	installPath := writeInstalledPluginManifest(t, manifest)
	store := newFakeServiceInstallationStore(&Installation{
		ID: 7, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
		InstallPath: installPath, Enabled: true,
	})
	host := &ctxCaptureHost{
		startResult: &fakePluginClient{manifest: manifest},
		entered:     make(chan struct{}),
		proceed:     make(chan struct{}),
	}
	service := &Service{installations: store, host: host}

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		c   pluginClient
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, err := service.ensureClient(ctx, 7)
		done <- result{c, err}
	}()

	<-host.entered // launch is in flight inside Start
	cancel()       // cancel the leader's ctx mid-launch

	select {
	case r := <-done:
		t.Fatalf("ensureClient returned early after leader cancel (err=%v); launch was coupled to leader ctx", r.err)
	case <-time.After(100 * time.Millisecond):
		// still launching — isolated from the leader's cancellation, as required
	}

	close(host.proceed)
	r := <-done
	if r.err != nil {
		t.Fatalf("ensureClient err = %v, want nil", r.err)
	}
	if r.c != host.startResult {
		t.Fatalf("got %#v, want launched client", r.c)
	}
	if host.canceledDuringLaunch() {
		t.Fatalf("launch ctx was canceled by the leader; expected isolation")
	}
}

func TestEnsureClientFailedLaunchPropagatesToAllCallers(t *testing.T) {
	manifest := testPluginManifest(t, "silo.metadb", "0.0.36")
	installPath := writeInstalledPluginManifest(t, manifest)
	store := newFakeServiceInstallationStore(&Installation{
		ID: 7, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
		InstallPath: installPath, Enabled: true,
	})
	host := &countingHerdHost{startErr: context.DeadlineExceeded, startDelay: 15 * time.Millisecond}
	service := &Service{installations: store, host: host}

	const n = 10
	var wg sync.WaitGroup
	release := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-release; _, errs[i] = service.ensureClient(context.Background(), 7) }(i)
	}
	close(release)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] == nil {
			t.Fatalf("caller %d err = nil, want the launch error", i)
		}
	}
	if got := host.startCount(); got != 1 {
		t.Fatalf("failed launch host.Start calls = %d, want 1 (still single-flighted)", got)
	}
}
