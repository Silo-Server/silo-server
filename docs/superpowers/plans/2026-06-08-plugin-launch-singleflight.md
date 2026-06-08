# Single-flight plugin client launch — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make concurrent `Service.ensureClient` calls for the same cold installation launch at most one plugin process (fix the cold-start thundering herd).

**Architecture:** Wrap the existing `ensureClient` body in a per-installation `golang.org/x/sync/singleflight.Group` keyed by installation id, so concurrent first-use collapses to one `Host.Start`; the rest share its result. No `Host.Start` API change.

**Tech Stack:** Go, `golang.org/x/sync/singleflight` (already a dependency, already used in `internal/metadata`/`internal/sections`).

**Repo:** `silo-server` (`/opt/silo`, branch `feat/requests-pluginization`). Go via `PATH=$PATH:/tmp/go/bin`. `internal/plugins` is pure-Go (tests run bare-host).

---

### Task 1: Single-flight `ensureClient`

**Files:**
- Modify: `internal/plugins/service.go` (`Service` struct + `ensureClient`)
- Test: `internal/plugins/service_singleflight_test.go` (new)

- [ ] **Step 1: Write the failing concurrency test**

Create `internal/plugins/service_singleflight_test.go`. It reuses same-package test helpers (`testPluginManifest`, `writeInstalledPluginManifest`, `newFakeServiceInstallationStore`, `fakePluginClient`, `pluginhost`):

```go
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

	// Post-completion: a fresh call hits the now-warm cache, no second launch.
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
```

- [ ] **Step 2: Run the test to verify it fails (race detector on)**

Run: `cd /opt/silo && PATH=$PATH:/tmp/go/bin go test ./internal/plugins/ -run TestEnsureClient -race -count=1`
Expected: `TestEnsureClientSingleFlightsConcurrentLaunch` FAILS (`host.Start calls = 20, want 1`) and `...FailedLaunch...` likely FAILS (multiple Start calls) because `ensureClient` isn't yet single-flighted. (`...DistinctInstallations...` may already pass.)

- [ ] **Step 3: Implement the single-flight wrapper**

In `internal/plugins/service.go`:

1. Add imports `"strconv"` and `"golang.org/x/sync/singleflight"` to the import block (keep imports grouped/sorted per goimports).

2. Add a field to the `Service` struct (a zero-value `singleflight.Group` needs no initialization, so struct-literal construction in tests stays valid):

```go
type Service struct {
	repositories   *RepositoryStore
	installations  serviceInstallationStore
	configs        serviceConfigStore
	catalog        *CatalogService
	installer      *Installer
	archiveCache   *ArchiveCache
	host           Host
	testConfigSeq  atomic.Int64
	dispatcher     *EventDispatcher
	lifecycleMu    sync.RWMutex
	lifecycleHooks []func(context.Context)
	launchGroup    singleflight.Group
}
```

3. Rename the existing `func (s *Service) ensureClient(...)` to `doEnsureClient` (body unchanged), and add a thin single-flighting `ensureClient` in front of it:

```go
// ensureClient returns a running client for the installation, collapsing
// concurrent first-use of a cold installation into a single launch so a burst of
// callers does not spawn redundant plugin processes (Host.Start releases its lock
// during the slow launch and cannot dedupe). After the flight completes the key
// is freed, so subsequent callers re-run and hit the now-warm cache.
func (s *Service) ensureClient(ctx context.Context, installationID int) (pluginClient, error) {
	v, err, _ := s.launchGroup.Do(strconv.Itoa(installationID), func() (any, error) {
		return s.doEnsureClient(ctx, installationID)
	})
	if err != nil {
		return nil, err
	}
	return v.(pluginClient), nil
}

func (s *Service) doEnsureClient(ctx context.Context, installationID int) (pluginClient, error) {
	// ... the entire EXISTING ensureClient body, unchanged ...
}
```

(Move the existing body verbatim into `doEnsureClient`; do not alter its logic.)

- [ ] **Step 4: Run the tests to verify they pass (with race detector)**

Run: `cd /opt/silo && PATH=$PATH:/tmp/go/bin go test ./internal/plugins/ -run TestEnsureClient -race -count=1`
Expected: all three new tests PASS, no race warnings. Then run the whole package: `PATH=$PATH:/tmp/go/bin go test ./internal/plugins/ -count=1` (existing ensureClient tests must still pass) and `PATH=$PATH:/tmp/go/bin go vet ./internal/plugins/`.

- [ ] **Step 5: Commit**

```bash
cd /opt/silo
git add internal/plugins/service.go internal/plugins/service_singleflight_test.go
git commit -m "fix(plugins): single-flight ensureClient to prevent cold-start launch herd"
```

---

### Task 2: Build, deploy, verify

- [ ] **Step 1: Re-vendor (no SDK change, but keep vendor consistent) + rebuild image**

```bash
cd /opt/silo && PATH=$PATH:/tmp/go/bin
go mod vendor
docker build -f Dockerfile.deploy -t silo-server:main --build-arg BUILD_REVISION="$(git rev-parse --short HEAD)" .
```
Expected: build succeeds.

- [ ] **Step 2: Redeploy + health**

```bash
cd /opt/silo
docker compose up -d silo
until [ "$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1)" = "healthy" ]; do sleep 2; done
docker ps --format '{{.Names}} {{.Status}}' | grep silo-silo
```
Expected: healthy.

- [ ] **Step 3: Verify only ONE arr process launches under concurrent cold-start**

Restart to force a cold plugin, then hit options for all connections and count arr process launches:
```bash
docker compose restart silo
until [ "$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1)" = "healthy" ]; do sleep 2; done
# (operator: hard-refresh Admin -> Requests so all connection cards probe options)
# then:
docker logs silo-silo-1 --since 2m 2>&1 | grep -c '"@message":"plugin address"'
docker logs silo-silo-1 --since 2m 2>&1 | grep -E "request-integrations.*/options" | grep -oE "status=[0-9]+ duration_ms=[0-9]+"
```
Expected: far fewer plugin-address launches than before; option calls return 200 with low duration (no 12–30s pile-up, no 500 timeouts).

- [ ] **Step 4: Update deploy-state memory**

Append to `/opt/deployarr/.claude/projects/-opt-silo/memory/requests-pluginization-deploy-state.md`: plugin-launch singleflight fix shipped (ensureClient single-flighted; cold-start herd that caused "couldn't load options" timeouts on multi-connection page loads is fixed); note the new silo HEAD.

---

## Notes for the implementer
- Do not edit the spec or this plan during implementation.
- The fix is host-only (`internal/plugins`) — no SDK, plugin, frontend, or migration changes; the arr plugin does NOT need reinstalling for this.
- Keep `doEnsureClient`'s body byte-for-byte identical to today's `ensureClient`; only the single-flight wrapper is new.
- Run tests with `-race`; this is a concurrency fix and the race detector is the point.
