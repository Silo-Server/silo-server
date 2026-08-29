package nodepool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// healthResponse is the JSON response from a node's /health endpoint.
type healthResponse struct {
	Status     string `json:"status"`
	ActiveJobs int    `json:"active_jobs"`
	EgressKbps int    `json:"egress_kbps"`
	// CapabilitiesHash identifies the node's current hardware capability
	// snapshot. A node that predates capability snapshots reports none, which
	// is how the sweep tells "nothing changed" from "cannot say".
	CapabilitiesHash string `json:"capabilities_hash"`
	// System and GPU are the node's latest resource sample, carried opaquely.
	// This package deliberately does not parse them: node metrics are display
	// data, nothing here routes on them, and decoding them would make nodepool
	// depend on the sampler's schema — so a node running a newer build can add
	// fields without an API-side change.
	System json.RawMessage `json:"system"`
	GPU    json.RawMessage `json:"gpu"`
}

// maxHealthResponseBytes bounds a node's whole /health body.
//
// A node is a worker that may run on remote, less trusted hardware, and its
// health answer is the one node-controlled payload this process decodes every
// 30 seconds. An honest sample is under 2 KB; this leaves three orders of
// magnitude of headroom while keeping a buggy or hostile build from making the
// API allocate an arbitrary body on a fixed cadence.
const maxHealthResponseBytes = 256 << 10

// maxLastStatsBytes bounds the resource blob that is persisted and served.
//
// Past this, the stats are dropped and the health verdict is kept: whether a
// node is alive routes streams, while its dashboard numbers do not, and an
// oversized blob would otherwise be rewritten into a jsonb column every sweep
// and echoed to every admin listing nodes.
const maxLastStatsBytes = 32 << 10

// CheckNode pings a node's /health endpoint and returns its health status,
// active job count, reported egress bandwidth, capability hash, and the opaque
// resource-stats blob to persist (nil when the node reported none).
func CheckNode(ctx context.Context, n *Node) (healthy bool, activeJobs, egressKbps int, capabilitiesHash string, lastStats []byte) {
	client := &http.Client{Timeout: 5 * time.Second}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, NodeEndpoint(n.URL, "/api/v1/health"), nil)
	if err != nil {
		return false, 0, 0, "", nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, 0, 0, "", nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, 0, 0, "", nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHealthResponseBytes+1))
	if err != nil {
		return false, 0, 0, "", nil
	}
	if len(body) > maxHealthResponseBytes {
		// Nothing in the body can be trusted to be well-formed at that size, so
		// the node is treated as not answering rather than partially believed.
		slog.WarnContext(ctx, "node health response too large to read", "component", "nodepool",
			"id", n.ID, "name", n.Name, "url", n.URL, "limit_bytes", maxHealthResponseBytes)
		return false, 0, 0, "", nil
	}

	var hr healthResponse
	if err := json.Unmarshal(body, &hr); err != nil {
		return false, 0, 0, "", nil
	}

	return true, hr.ActiveJobs, hr.EgressKbps, hr.CapabilitiesHash, marshalLastStats(ctx, n, hr)
}

// marshalLastStats packs a health response's resource fields into the blob
// stored on the node row, or nil when the node sent neither.
//
// nil is what a node predating resource sampling produces, and it must persist
// as SQL NULL rather than as an empty object: "this node cannot report" and
// "this node reported nothing in use" are different states, and only the second
// would justify drawing a zero on a dashboard.
func marshalLastStats(ctx context.Context, n *Node, hr healthResponse) []byte {
	system := trimJSONNull(hr.System)
	gpu := trimJSONNull(hr.GPU)
	if system == nil && gpu == nil {
		return nil
	}
	payload := struct {
		System json.RawMessage `json:"system,omitempty"`
		GPU    json.RawMessage `json:"gpu,omitempty"`
	}{System: system, GPU: gpu}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	if len(encoded) > maxLastStatsBytes {
		slog.WarnContext(ctx, "node resource sample too large to store", "component", "nodepool",
			"id", n.ID, "name", n.Name, "url", n.URL,
			"bytes", len(encoded), "limit_bytes", maxLastStatsBytes)
		return nil
	}
	return encoded
}

// trimJSONNull normalizes an absent field. encoding/json leaves a RawMessage
// nil when the key is missing but sets it to the literal "null" when the key is
// present and null, and both mean the node has nothing to say.
func trimJSONNull(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	return trimmed
}

// CapabilityFetcher retrieves one node's full capability report and the hash
// the payload identifies itself by. The payload is stored opaquely, so this
// package never has to understand — or import — the playback capability model.
// An empty hash means the payload cannot be tracked for change and must not be
// persisted.
// The whole node is passed rather than its URL because the answer's cost is a
// property of the node: a worker builds its probe matrix from its *effective*
// acceleration policy, so one carrying an hw_device override with two devices
// legitimately takes longer to answer than the cluster-wide setting predicts. A
// fetcher that sizes its request from the cluster setting alone cancels such a
// node mid-probe every sweep, and its inventory never lands.
type CapabilityFetcher func(ctx context.Context, node *Node) (payload []byte, hash string, err error)

// capabilityFetchTimeout is the floor under the backstop on one capability
// fetch. It is not the budget.
//
// The budget belongs to the fetcher, which knows the configured hardware and
// sizes each request against the node's own advertised probe matrix. That matrix
// grows with the device count without bound — nine render devices legitimately
// ask for over five minutes — so no fixed number here can be both a real
// backstop and safe against cutting a node short. What this stops is the other
// failure: a fetcher that never returns, pinning a goroutine and a node's
// inventory forever.
//
// So the backstop is derived from the fetcher's own budget and this floor is
// only what applies when no budget is wired. A backstop that trips during
// ordinary operation is indistinguishable from the bug it was meant to catch.
const capabilityFetchTimeout = 5 * time.Minute

// capabilityFetchSlack is how far the sweep's backstop sits above the budget the
// fetcher gave itself, so the fetcher's own deadline is always the one that
// fires first and the failure an operator sees names the probe rather than this.
const capabilityFetchSlack = time.Minute

// CapabilityRefreshTimeout is the floor under that bound, and all a caller can
// assume when it has no health checker to ask. It is exported for the one caller
// that has to hold an HTTP connection open across a refresh and must therefore
// size its own write deadline to include it — see CapabilityRefreshBound, which
// is the number that caller should actually use.
const CapabilityRefreshTimeout = capabilityFetchTimeout

// CapabilityRefreshBound is how long RefreshNodeCapabilities may take for this
// node, which is the same backstop the sweep's own fetches run under.
//
// It is exported because a caller holding an HTTP connection open across a
// refresh has to reserve the real number, not the floor: the backstop is derived
// from the node's advertised probe budget, and a node with a large device set
// asks for well past five minutes. Reserving the floor there means the
// connection's write deadline can fire after the refresh succeeded but before
// its response is written, and the operator is told an action failed that has
// already changed the node.
func (hc *HealthChecker) CapabilityRefreshBound(n *Node) time.Duration {
	if hc == nil {
		return capabilityFetchTimeout
	}
	return hc.capabilityFetchBackstop(n)
}

// HealthChecker runs periodic health checks on all nodes in both pools,
// updating in-memory state and optionally persisting to the database.
type HealthChecker struct {
	proxyPool     *ProxyPool
	transcodePool *TranscodePool
	repo          *Repository // may be nil (proxy/transcode modes have no DB)
	interval      time.Duration

	// mu guards the two injected hooks. Both are wired after construction —
	// the capability-change callback because the playback handler that consumes
	// it is built later, during router assembly — while the sweep may already
	// be running.
	mu                    sync.RWMutex
	capFetch              CapabilityFetcher
	capFetchBudget        func(*Node) time.Duration
	onCapabilitiesChanged func(nodeURL string)

	// capabilityRefreshes tracks the detached capability fetches so shutdown —
	// and tests — can wait for them. The sweep itself must never wait on one.
	capabilityRefreshes sync.WaitGroup
	// capabilityRefreshInFlight holds the node ids currently being fetched, so
	// a fetch that outlives the sweep that started it is not started again by
	// the next sweep. Node ids are unique across both pools (one table).
	capabilityRefreshInFlight sync.Map
}

// NewHealthChecker creates a health checker for the given pools.
func NewHealthChecker(proxyPool *ProxyPool, transcodePool *TranscodePool, repo *Repository) *HealthChecker {
	return &HealthChecker{
		proxyPool:     proxyPool,
		transcodePool: transcodePool,
		repo:          repo,
		interval:      30 * time.Second,
	}
}

// SetCapabilityFetchBudget wires how long the fetcher will allow itself for one
// node, so the sweep's backstop can be derived from it rather than guessed.
//
// Without it the backstop falls back to capabilityFetchTimeout, which is right
// for the common configuration and too tight for a node with many devices —
// hence this, supplied by the same wiring that supplies the fetcher.
func (hc *HealthChecker) SetCapabilityFetchBudget(budget func(*Node) time.Duration) {
	if hc == nil {
		return
	}
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.capFetchBudget = budget
}

// capabilityFetchBackstop bounds one fetch above whatever the fetcher allowed
// itself, so the fetcher's deadline always fires first.
func (hc *HealthChecker) capabilityFetchBackstop(n *Node) time.Duration {
	hc.mu.RLock()
	budget := hc.capFetchBudget
	hc.mu.RUnlock()
	if budget == nil {
		return capabilityFetchTimeout
	}
	return max(capabilityFetchTimeout, budget(n)+capabilityFetchSlack)
}

// SetCapabilityFetcher wires how the sweep retrieves a node's capability report
// once the node reports a hash it has not stored. Without one the sweep behaves
// exactly as it did before capability tracking.
func (hc *HealthChecker) SetCapabilityFetcher(fetch CapabilityFetcher) {
	if hc == nil {
		return
	}
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.capFetch = fetch
}

// SetCapabilitiesChangedCallback wires the notification fired after a node's
// capabilities were refetched and stored, so caches keyed on node capability
// can be invalidated without waiting for their own TTL.
func (hc *HealthChecker) SetCapabilitiesChangedCallback(fn func(nodeURL string)) {
	if hc == nil {
		return
	}
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.onCapabilitiesChanged = fn
}

func (hc *HealthChecker) hooks() (CapabilityFetcher, func(nodeURL string)) {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return hc.capFetch, hc.onCapabilitiesChanged
}

// Start runs health checks in a background goroutine. Stops when ctx is cancelled.
func (hc *HealthChecker) Start(ctx context.Context) {
	go func() {
		hc.checkAll(ctx)
		ticker := time.NewTicker(hc.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hc.checkAll(ctx)
			}
		}
	}()
}

// applyHealthFunc is a pool's copy-on-write health writer.
type applyHealthFunc func(id int, checkedURL string, healthy bool, activeJobs, egressKbps int, advertisedHash string, lastStats []byte, checkedAt time.Time)

// applyCapabilitiesFunc is a pool's copy-on-write capability writer.
type applyCapabilitiesFunc func(id int, fetchedFrom string, capabilities []byte, hash string, refreshedAt time.Time, drift *string, driftBaseline []byte)

func (hc *HealthChecker) checkAll(ctx context.Context) {
	var wg sync.WaitGroup
	check := func(n *Node, applyHealth applyHealthFunc, applyCapabilities applyCapabilitiesFunc) {
		wg.Go(func() {
			healthy, activeJobs, egressKbps, capabilitiesHash, lastStats := CheckNode(ctx, n)

			// Publish the result through the pool lock so readers never see
			// a Node struct mutated in place (the pool swaps in a copy). Fenced
			// on the checked URL, like the database write below: the pool can be
			// reloaded with a different worker on this id while the check runs.
			applyHealth(n.ID, n.URL, healthy, activeJobs, egressKbps, capabilitiesHash, lastStats, time.Now())

			if n.Healthy && !healthy {
				slog.WarnContext(ctx, "stream node unhealthy", "component", "nodepool", "id", n.ID, "name", n.Name, "url", n.URL)
			} else if !n.Healthy && healthy {
				slog.InfoContext(ctx, "stream node recovered", "component", "nodepool", "id", n.ID, "name", n.Name, "url", n.URL)
			}

			if hc.repo != nil {
				// Fenced on the URL that was checked: last_stats feeds transcode
				// admission, so one worker's disk reading must never land on a
				// row an administrator has since repointed at another.
				if err := hc.repo.UpdateHealth(ctx, n.ID, n.URL, healthy, activeJobs, egressKbps, lastStats); err != nil {
					if errors.Is(err, ErrNodeMoved) {
						slog.InfoContext(ctx, "discarded a health result for a node that changed identity mid-check",
							"component", "nodepool", "id", n.ID, "name", n.Name, "url", n.URL)
					} else {
						slog.ErrorContext(ctx, "failed to persist node health", "component", "nodepool", "id", n.ID, "error", err)
					}
				}
			}

			if healthy && capabilitiesHash != "" && capabilitiesHash != storedCapabilitiesHash(n) {
				hc.startCapabilityRefresh(ctx, n, applyCapabilities)
			}
		})
	}
	for _, n := range hc.proxyPool.Nodes() {
		check(n, hc.proxyPool.ApplyHealth, hc.proxyPool.ApplyCapabilities)
	}
	for _, n := range hc.transcodePool.Nodes() {
		check(n, hc.transcodePool.ApplyHealth, hc.transcodePool.ApplyCapabilities)
	}
	wg.Wait()
}

// startCapabilityRefresh runs one node's capability fetch off the sweep's
// WaitGroup, deduplicated per node id.
//
// The fetch budget is larger than the sweep interval by design (a cold node
// runs ffmpeg probes to answer), so waiting for it inside the sweep would let
// one slow node stretch every other node's health cadence past that interval —
// and pool health is what routes streams away from a node that died. The
// in-flight guard is what keeps the detached fetches from stacking up: without
// it, a node that answers slower than the interval would collect one goroutine
// per sweep, since a fetch that has not completed cannot have moved the stored
// hash that triggered it.
func (hc *HealthChecker) startCapabilityRefresh(ctx context.Context, n *Node, applyCapabilities applyCapabilitiesFunc) {
	if fetch, _ := hc.hooks(); fetch == nil {
		return
	}
	if _, loaded := hc.capabilityRefreshInFlight.LoadOrStore(n.ID, struct{}{}); loaded {
		return
	}
	hc.capabilityRefreshes.Add(1)
	go func() {
		defer hc.capabilityRefreshes.Done()
		defer hc.capabilityRefreshInFlight.Delete(n.ID)
		// Errors are already logged inside; the sweep has no caller to report to.
		_ = hc.refreshCapabilities(ctx, n, applyCapabilities)
	}()
}

// waitForCapabilityRefreshes blocks until every detached capability fetch
// started so far has finished. Callers must not hold the sweep open on it.
func (hc *HealthChecker) waitForCapabilityRefreshes() {
	hc.capabilityRefreshes.Wait()
}

// RefreshNodeCapabilities fetches, stores, and publishes one node's capability
// report immediately, on the caller's goroutine, using exactly the machinery the
// background sweep uses.
//
// It exists for the operator-triggered re-probe: the node has just recomputed
// its inventory, and waiting up to a sweep interval for the API to notice would
// make the action look like it did nothing. Every rule the sweep applies still
// applies here — drift is computed and persisted the same way, a report without
// a hash is refused, a failed fetch leaves the stored row alone — so there is no
// second, divergent persist path.
//
// It participates in the sweep's per-node in-flight guard, so a manual refresh
// and a sweep refresh of the same node cannot run at once; the loser reports
// ErrCapabilityRefreshInFlight rather than starting a duplicate fetch.
func (hc *HealthChecker) RefreshNodeCapabilities(ctx context.Context, n *Node) error {
	if hc == nil || n == nil {
		return errors.New("no node health checker configured")
	}
	if fetch, _ := hc.hooks(); fetch == nil {
		return errors.New("no node capability fetcher configured")
	}
	if _, loaded := hc.capabilityRefreshInFlight.LoadOrStore(n.ID, struct{}{}); loaded {
		return ErrCapabilityRefreshInFlight
	}
	defer hc.capabilityRefreshInFlight.Delete(n.ID)
	return hc.refreshCapabilities(ctx, n, hc.applyCapabilitiesFor(n))
}

// ErrCapabilityRefreshInFlight reports that a capability refresh for the node
// was already running, so the caller's own refresh was not started. The report
// in flight is at least as fresh as the one the caller wanted.
var ErrCapabilityRefreshInFlight = errors.New("node capability refresh already in flight")

// adoptStoredCapabilities brings this replica's in-memory node up to whatever
// is stored, after another writer won the capability compare-and-set.
//
// Without it the losing replica keeps the pre-fetch hash in memory, compares
// against it on every sweep, refetches, and loses the same write again — while
// its pools serve GPU identities and a drift note that the row has moved past.
// Reading the row is cheap next to the capability fetch that preceded it.
func (hc *HealthChecker) adoptStoredCapabilities(
	ctx context.Context, n *Node, applyCapabilities applyCapabilitiesFunc, onChanged func(string),
) {
	if hc.repo == nil {
		return
	}
	stored, err := hc.repo.GetByID(ctx, n.ID)
	if err != nil || stored == nil || stored.CapabilitiesHash == nil {
		return
	}
	refreshedAt := time.Now()
	if stored.CapabilitiesRefreshedAt != nil {
		refreshedAt = *stored.CapabilitiesRefreshedAt
	}
	if applyCapabilities != nil {
		applyCapabilities(stored.ID, stored.URL, stored.Capabilities, *stored.CapabilitiesHash,
			refreshedAt, stored.CapabilityDrift, stored.CapabilityDriftBaseline)
	}
	// The planning cache is per replica, so the winner's invalidation did not
	// reach this one. A capability change is exactly what it exists to hear.
	if onChanged != nil && !sameOptionalHash(n.CapabilitiesHash, stored.CapabilitiesHash) {
		onChanged(stored.URL)
	}
}

// sameOptionalHash compares two stored capability hashes, treating absence as a
// value rather than as a match.
func sameOptionalHash(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// applyCapabilitiesFor returns the pool writer for a node's type, or nil when
// this checker has no pool for it — which is the normal state for a node row
// that is disabled and therefore in no pool.
func (hc *HealthChecker) applyCapabilitiesFor(n *Node) applyCapabilitiesFunc {
	switch n.Type {
	case NodeTypeProxy:
		if hc.proxyPool != nil {
			return hc.proxyPool.ApplyCapabilities
		}
	case NodeTypeTranscode:
		if hc.transcodePool != nil {
			return hc.transcodePool.ApplyCapabilities
		}
	}
	return nil
}

// refreshCapabilities fetches and stores one node's capability report. A
// failure leaves the stored row alone and is retried on the next sweep, because
// a fetch that failed is no evidence about what the node has.
//
// The returned error is for a caller that asked for this refresh explicitly; the
// sweep ignores it, since every failure is already logged here.
func (hc *HealthChecker) refreshCapabilities(ctx context.Context, n *Node, applyCapabilities applyCapabilitiesFunc) error {
	fetch, onChanged := hc.hooks()
	if fetch == nil {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, hc.capabilityFetchBackstop(n))
	defer cancel()
	payload, hash, err := fetch(fetchCtx, n)
	if err != nil {
		slog.WarnContext(ctx, "node capability fetch failed", "component", "nodepool",
			"id", n.ID, "name", n.Name, "url", n.URL, "error", err)
		return err
	}
	if hash == "" || len(payload) == 0 {
		// A hash is what makes the payload trackable; storing one without it
		// would refetch every sweep forever. Never synthesize one here — the
		// node is the only thing that knows what it hashed.
		slog.WarnContext(ctx, "node capability report carries no hash", "component", "nodepool",
			"id", n.ID, "name", n.Name, "url", n.URL)
		return errors.New("node capability report carries no hash")
	}

	// Computed before the write, because the comparison is against the report
	// this one replaces, and stored with it so a reader never sees a note
	// describing a different payload.
	//
	// Not for a proxy. Drift is a statement about transcode hardware, and a
	// proxy's report deliberately carries no hardware inventory at all: it
	// relays streams and runs remux recipes, so it advertises transformations
	// and nothing else. Comparing a hardware-free report against one an older
	// build stored with render devices in it reads as every device disappearing
	// at once, and nothing can ever clear that note — recovery is evidenced by
	// probes the proxy will never run again. So drift is skipped and both
	// columns are written nil, which also erases whatever an older build latched.
	drift, parsed := computeCapabilityDrift(n.Capabilities, payload)
	var note *string
	var driftBaseline []byte
	if n.Type != NodeTypeProxy {
		note, driftBaseline = resolveDriftNote(n.CapabilityDrift, n.CapabilityDriftBaseline, drift, parsed, payload)
	}
	refreshedAt := time.Now()
	if hc.repo != nil {
		// Fenced on the URL this payload was fetched from — the fetch is
		// detached and bounded, so the row may since have been repointed at a
		// different worker — and on the report it is replacing, so a slower
		// sweep on another replica cannot land an older report on top of a
		// newer one.
		if err := hc.repo.UpdateCapabilities(ctx, n.ID, n.URL, payload, hash, refreshedAt, note, driftBaseline, n.CapabilitiesHash); err != nil {
			if errors.Is(err, ErrCapabilitiesSuperseded) {
				// Another replica stored a report first. Its answer is the
				// current one, so this replica adopts the row rather than
				// discarding and retrying: left alone it would keep comparing
				// against a hash the row no longer has, lose the same write
				// every sweep, and serve stale GPU identities and drift state
				// until something unrelated reloaded the pools.
				hc.adoptStoredCapabilities(ctx, n, applyCapabilities, onChanged)
				return err
			}
			if errors.Is(err, ErrNodeMoved) {
				// Not a failure to report loudly: the node was edited or
				// removed while this fetch ran, and the next sweep will fetch
				// whatever the row addresses now.
				slog.InfoContext(ctx, "discarded a capability report the row no longer expects",
					"component", "nodepool", "id", n.ID, "name", n.Name, "url", n.URL)
				return err
			}
			slog.WarnContext(ctx, "failed to persist node capabilities", "component", "nodepool",
				"id", n.ID, "name", n.Name, "error", err)
			return err
		}
	}
	logCapabilityChange(ctx, n, drift, parsed)
	if applyCapabilities != nil {
		applyCapabilities(n.ID, n.URL, payload, hash, refreshedAt, note, driftBaseline)
	}
	if onChanged != nil {
		onChanged(n.URL)
	}
	return nil
}

func storedCapabilitiesHash(n *Node) string {
	if n == nil || n.CapabilitiesHash == nil {
		return ""
	}
	return *n.CapabilitiesHash
}

// capabilityDriftView is the minimal projection this package parses out of an
// otherwise opaque capability payload. It is deliberately local and partial:
// nodepool must not depend on playback, and drift only needs to know which
// backends were verified and which render devices existed.
type capabilityDriftView struct {
	Resolved         string             `json:"resolved"`
	DetectedBackends []driftBackendView `json:"detected_backends"`
	RenderDevices    []string           `json:"render_devices"`
	// RenderDeviceDetails carries each device's stable identity. Comparing
	// enumeration paths alone reports a GPU as gone whenever DRM hands out a
	// different renderD number, which a reboot is free to do; the uuid and the
	// PCI slot survive that.
	RenderDeviceDetails []struct {
		Path       string `json:"path"`
		PCIAddress string `json:"pci_address"`
		GPUUUID    string `json:"gpu_uuid"`
	} `json:"render_device_details"`
	// NVIDIAGPUUUIDs is where an NVIDIA card's identity lives when it has no
	// readable DRM node — the ordinary NVENC container: /dev/nvidia* and the
	// toolkit, no /dev/dri. Such a node reports no render devices at all, so
	// without this a card disappearing from it moves nothing this comparison
	// looks at, and the backend comparison does not cover it either: NVENC stops
	// being a candidate the moment /dev/nvidia* goes away, and an absent backend
	// is deliberately not a lost one.
	NVIDIAGPUUUIDs []string `json:"nvidia_gpu_uuids"`
}

// driftBackendView is one backend's probe outcome as drift reads it.
type driftBackendView struct {
	Backend  string `json:"backend"`
	Verified bool   `json:"verified"`
	// Skipped reports that no probe was attempted because the node cannot open
	// any of the backend's candidate devices. That is a statement about access,
	// not about hardware, so it never counts as a failure.
	Skipped bool `json:"skipped"`
}

// nvidiaBackend is the backend name whose presence in a report means the node
// still has NVIDIA device nodes; detection drops it as a candidate when they go
// away.
const nvidiaBackend = "nvenc"

// nvidiaIdentityBlind reports that this report cannot name the node's NVIDIA
// cards even though the node still has them.
//
// nvidia-smi is queried behind a circuit breaker and can be absent from an
// image entirely, so an empty uuid list is not by itself evidence that anything
// is gone — the same "identity strength is not constant" problem renderDeviceAliases
// exists for. What separates the two is the backend list: NVENC is only probed
// where /dev/nvidia* can be opened, so a report that still carries NVENC and no
// uuids is a node whose cards are present and whose query tool is not.
func (v capabilityDriftView) nvidiaIdentityBlind() bool {
	if len(v.NVIDIAGPUUUIDs) > 0 {
		return false
	}
	return slices.ContainsFunc(v.DetectedBackends, func(backend driftBackendView) bool {
		return backend.Backend == nvidiaBackend
	})
}

// renderDeviceAliases lists every stable name each device in a report answers
// to, alongside the path an operator recognizes it by.
//
// All of them, not just the strongest: a report's identity strength is not
// constant. nvidia-smi is queried behind a circuit breaker and may be missing
// on one pass and present on the next, so the same NVIDIA card alternates
// between publishing a PCI address alone and publishing a uuid as well. Keeping
// only the strongest name would make those two reports describe different
// devices and persist a "render device gone" note for a card that never moved —
// the same false positive as comparing enumeration paths, one level up. Two
// reports describe the same device when they share any alias.
type renderDeviceAliases struct {
	// path is what the note names the device by; it is the least stable of the
	// aliases, which is why it is display only.
	path string
	// uuid is the card's permanent identity where it published one. It is held
	// apart from the aliases because it is the only name that can prove two
	// devices are *different*: a slot and a render path are properties of the
	// machine and outlive the card in them.
	uuid    string
	aliases []string
	// nvidiaOnly marks a card known only through nvidia-smi, with no render
	// node behind it. Its identity depends on a tool that comes and goes, so a
	// report that lost it has to be read differently — see nvidiaIdentityBlind.
	nvidiaOnly bool
}

// sameDevice reports whether two reports describe one card.
//
// Two permanent uuids that disagree settle it on their own — a replacement card
// in the same slot keeps the slot's PCI address and usually the same render
// path, and letting those weaker names match would hide the old card's
// disappearance entirely. Only when at least one side published no uuid does a
// shared weaker alias stand in for one.
func (a renderDeviceAliases) sameDevice(b renderDeviceAliases) bool {
	if a.uuid != "" && b.uuid != "" {
		return a.uuid == b.uuid
	}
	return slices.ContainsFunc(a.aliases, func(alias string) bool {
		return slices.Contains(b.aliases, alias)
	})
}

func renderDeviceAliasSets(view capabilityDriftView) []renderDeviceAliases {
	devices := make([]renderDeviceAliases, 0, len(view.RenderDevices)+len(view.NVIDIAGPUUUIDs))
	covered := make(map[string]bool, len(view.RenderDeviceDetails))
	coveredUUIDs := make(map[string]bool, len(view.RenderDeviceDetails)+len(view.NVIDIAGPUUUIDs))
	for _, device := range view.RenderDeviceDetails {
		entry := renderDeviceAliases{path: device.Path, uuid: device.GPUUUID}
		for _, alias := range []string{device.GPUUUID, device.PCIAddress, device.Path} {
			if alias != "" && !slices.Contains(entry.aliases, alias) {
				entry.aliases = append(entry.aliases, alias)
			}
		}
		if len(entry.aliases) == 0 {
			continue
		}
		covered[device.Path] = true
		if device.GPUUUID != "" {
			coveredUUIDs[device.GPUUUID] = true
		}
		devices = append(devices, entry)
	}
	// A report that lists paths without details (a node predating them) still
	// has to be comparable, so any uncovered path stands for itself.
	for _, path := range view.RenderDevices {
		if path == "" || covered[path] {
			continue
		}
		devices = append(devices, renderDeviceAliases{path: path, aliases: []string{path}})
	}
	// A card nvidia-smi named and no render node covers. Deduplicated against
	// the details above by uuid, because a card with both a DRM node and an
	// nvidia-smi entry is one card, not two. It gets no path: the uuid is the
	// only name it has, and lostRenderDevices falls back to naming it by that.
	for _, uuid := range view.NVIDIAGPUUUIDs {
		if uuid == "" || coveredUUIDs[uuid] {
			continue
		}
		coveredUUIDs[uuid] = true
		devices = append(devices, renderDeviceAliases{uuid: uuid, aliases: []string{uuid}, nvidiaOnly: true})
	}
	return devices
}

// lostRenderDeviceEntries returns the devices in previous that nothing in
// current answers to, with their full alias sets: the note displays the path,
// while the drift baseline keeps every identity so the device can be recognized
// when it comes back under a different one.
func lostRenderDeviceEntries(previous, current capabilityDriftView) []renderDeviceAliases {
	currentDevices := renderDeviceAliasSets(current)
	blind := current.nvidiaIdentityBlind()
	var lost []renderDeviceAliases
	for _, device := range renderDeviceAliasSets(previous) {
		if slices.ContainsFunc(currentDevices, device.sameDevice) {
			continue
		}
		if blind && device.nvidiaOnly {
			// The node still has its NVIDIA devices; only the tool that names
			// them is missing. Latching a note here would demand a uuid come
			// back that nothing on the node can currently produce.
			continue
		}
		lost = append(lost, device)
	}
	return lost
}

// lostRenderDevices names the devices in previous that nothing in current
// answers to.
func lostRenderDevices(previous, current capabilityDriftView) []string {
	entries := lostRenderDeviceEntries(previous, current)
	lost := make([]string, 0, len(entries))
	for _, device := range entries {
		name := device.path
		if name == "" {
			name = device.aliases[0]
		}
		lost = append(lost, name)
	}
	// The order devices are reported in is incidental, and this note is
	// persisted and compared against the one it replaces.
	slices.Sort(lost)
	return slices.Compact(lost)
}

// capabilityDrift is what a refetch lost relative to the report it replaces.
// The zero value means nothing was lost, which is also what a node's very first
// report produces.
type capabilityDrift struct {
	// first reports that there was no stored report to compare against.
	first bool
	// lostBackends are backends that used to pass their probe and no longer do.
	lostBackends []string
	// lostDevices are render devices present in the previous report and absent
	// from this one.
	lostDevices []string
	// lostDeviceAliases carries every identity each lost device answered to, so
	// the drift baseline can recognize it if it comes back under another one.
	lostDeviceAliases []renderDeviceAliases
	// previousResolved and resolved are the backend each report resolved to;
	// carried for the log line, which is where an operator reads the effect.
	previousResolved string
	resolved         string
}

// regressed reports whether this refetch lost something worth telling an
// operator about.
func (d capabilityDrift) regressed() bool {
	return len(d.lostBackends) > 0 || len(d.lostDevices) > 0
}

// maxCapabilityDriftNoteBytes bounds the stored note. The inputs are a node's
// own device and backend lists, so an honest note is a line long; the bound is
// only there because the lists come from a worker that may run elsewhere, and a
// text column echoed to every admin listing nodes is not the place to trust
// them.
const maxCapabilityDriftNoteBytes = 512

// persistedNote renders this refetch's regression for the
// stream_nodes.capability_drift column, or nil when this refetch lost nothing.
// nil is not by itself a reason to clear a note the node already carries — see
// resolveDriftNote, which owns that decision.
func (d capabilityDrift) persistedNote() *string {
	if !d.regressed() {
		return nil
	}
	parts := make([]string, 0, 3)
	if len(d.lostBackends) > 0 {
		parts = append(parts, "verified hardware backends lost: "+strings.Join(d.lostBackends, ", "))
	}
	if len(d.lostDevices) > 0 {
		parts = append(parts, "render devices gone: "+strings.Join(d.lostDevices, ", "))
	}
	if d.previousResolved != d.resolved {
		parts = append(parts, "resolved backend "+d.previousResolved+" -> "+d.resolved)
	}
	note := truncateDriftNote(strings.Join(parts, "; "))
	return &note
}

// truncateDriftNote bounds a note to maxCapabilityDriftNoteBytes without ever
// cutting a rune in half.
//
// The column is Postgres text, which rejects invalid UTF-8 outright, and a
// rejected UPDATE takes capabilities and capabilities_hash down with it — the
// stored hash then never advances, so every later sweep refetches and fails
// again. Slicing bytes is exactly how the untrusted device and backend names
// this bound exists to contain would produce that.
func truncateDriftNote(note string) string {
	if len(note) <= maxCapabilityDriftNoteBytes {
		return note
	}
	cut := note[:maxCapabilityDriftNoteBytes]
	// A trailing partial sequence decodes as RuneError with size 1; a real
	// U+FFFD in the input decodes with size 3 and is left alone.
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + "..."
}

// resolveDriftNote decides what stream_nodes.capability_drift should say after
// this refetch, given the note it already carries.
//
// Setting the note is a delta — a regression against the report being replaced.
// Clearing it must not be, because a delta against an already-degraded report
// finds nothing newly lost and would erase a standing regression on the next
// unrelated hash change: a reboot moves boot_id, a reworded FFmpeg failure moves
// the probe reason, and the operator-triggered re-probe refetches
// unconditionally. That would tell an operator the node recovered while its
// backend is still failing its probe, which is the one reading this column must
// never produce. So the note is latched, and only a report whose probes all pass
// clears it.
func resolveDriftNote(stored *string, storedBaseline []byte, drift capabilityDrift, parsed bool, payload []byte) (*string, []byte) {
	outstanding := mergeDriftBaseline(storedBaseline, drift)
	if drift.regressed() {
		// The note names everything still outstanding, not just this pass's
		// delta. Both accumulate, and if only the baseline did, two GPUs going
		// one at a time would leave the operator reading about the second while
		// the note stayed latched — after that one returned — for a first loss
		// nothing on screen ever mentioned.
		note := outstanding.note(drift)
		return &note, marshalDriftBaseline(outstanding)
	}
	if stored == nil || strings.TrimSpace(*stored) == "" {
		return nil, nil
	}
	if !parsed || !hardwareProbesEvidenced(payload) {
		// Nothing new was lost, but this report is not evidence of recovery.
		return stored, marshalDriftBaseline(outstanding)
	}
	if outstanding.empty() && !hardwareProbesClean(payload) {
		// A note written before baselines existed names nothing to wait for, so
		// a wholly clean report is the only evidence available for it.
		return stored, marshalDriftBaseline(outstanding)
	}
	if !outstanding.recoveredBy(payload) {
		// Recovery is the *originally lost* hardware coming back, not the
		// inventory merely looking healthy. A surviving sibling probes just as
		// cleanly with its partner still missing, and an unrelated GPU added
		// later is growth without repair. Only the baseline can tell those from
		// a genuine return, which is why it is kept rather than re-derived: once
		// the degraded report is stored, every later comparison is
		// degraded-to-degraded and finds nothing at all.
		return stored, marshalDriftBaseline(outstanding)
	}
	return nil, nil
}

// driftBaseline is the hardware a standing capability_drift note is waiting on.
type driftBaseline struct {
	// Backends must verify again.
	Backends []string `json:"backends,omitempty"`
	// Devices are the cards that must reappear, each carrying every name it
	// answered to so a renumbered render node — or a pass where nvidia-smi did
	// not answer — still matches it.
	Devices []driftBaselineDevice `json:"devices,omitempty"`
}

// driftBaselineDevice is one lost card's identity, in the same shape the loss
// comparison uses so both sides apply the same matching rule.
type driftBaselineDevice struct {
	// UUID is the card's permanent identity where it published one. It is held
	// apart from Aliases because it is the only name that can prove two devices
	// are *different*: a replacement in the same slot inherits the slot and
	// usually the render path, and matching on those would read as the lost
	// card returning.
	UUID    string   `json:"uuid,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

func (d driftBaselineDevice) matches(candidate renderDeviceAliases) bool {
	return renderDeviceAliases{uuid: d.UUID, aliases: d.Aliases}.sameDevice(candidate)
}

func (b driftBaseline) empty() bool { return len(b.Backends) == 0 && len(b.Devices) == 0 }

// note renders what this baseline is still waiting on, plus the resolution
// change this pass saw.
//
// The hardware half comes from the baseline so the visible text and the latch
// agree: an operator watching the note disappear should be watching the same
// thing the clearing rule is watching. The resolution transition comes from the
// delta, because "qsv -> none" describes this refresh rather than a standing
// debt, and the previous pass's transition is stale once another has happened.
func (b driftBaseline) note(drift capabilityDrift) string {
	parts := make([]string, 0, 3)
	if len(b.Backends) > 0 {
		parts = append(parts, "verified hardware backends lost: "+strings.Join(b.Backends, ", "))
	}
	if names := b.deviceNames(); len(names) > 0 {
		parts = append(parts, "render devices gone: "+strings.Join(names, ", "))
	}
	if drift.previousResolved != drift.resolved {
		parts = append(parts, "resolved backend "+drift.previousResolved+" -> "+drift.resolved)
	}
	return truncateDriftNote(strings.Join(parts, "; "))
}

// deviceNames picks one readable name per outstanding device: the render path an
// operator would recognize, else the uuid, else whatever alias there is.
func (b driftBaseline) deviceNames() []string {
	names := make([]string, 0, len(b.Devices))
	for _, device := range b.Devices {
		name := device.UUID
		for _, alias := range device.Aliases {
			if strings.HasPrefix(alias, "/dev/") {
				name = alias
				break
			}
			if name == "" {
				name = alias
			}
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// recoveredBy reports whether every backend and device the note is waiting on is
// accounted for in this report.
func (b driftBaseline) recoveredBy(payload []byte) bool {
	if b.empty() {
		// Nothing recorded to wait for — a note written before baselines
		// existed. A clean report is the best evidence available, and holding
		// such a note forever would strand it.
		return true
	}
	var current capabilityDriftView
	if json.Unmarshal(payload, &current) != nil {
		return false
	}
	verified := make(map[string]bool, len(current.DetectedBackends))
	for _, backend := range current.DetectedBackends {
		verified[backend.Backend] = backend.Verified
	}
	for _, backend := range b.Backends {
		if !verified[backend] {
			return false
		}
	}
	// Matched with sameDevice, not by any shared alias: a replacement card in
	// the same slot shares the PCI address and usually the render path, and
	// treating that as the lost card returning is exactly the false recovery
	// the loss side already refuses to call a match.
	currentDevices := renderDeviceAliasSets(current)
	for _, device := range b.Devices {
		if !slices.ContainsFunc(currentDevices, device.matches) {
			return false
		}
	}
	return true
}

// mergeDriftBaseline adds this refetch's losses to whatever the note was already
// waiting on.
func mergeDriftBaseline(stored []byte, drift capabilityDrift) driftBaseline {
	var baseline driftBaseline
	if len(stored) > 0 {
		// An unreadable baseline is treated as absent rather than as a reason to
		// discard the losses this pass found.
		_ = json.Unmarshal(stored, &baseline)
	}
	for _, backend := range drift.lostBackends {
		if !slices.Contains(baseline.Backends, backend) {
			baseline.Backends = append(baseline.Backends, backend)
		}
	}
	for _, device := range drift.lostDeviceAliases {
		if slices.ContainsFunc(baseline.Devices, func(existing driftBaselineDevice) bool {
			return existing.matches(device)
		}) {
			continue
		}
		baseline.Devices = append(baseline.Devices, driftBaselineDevice{
			UUID:    device.uuid,
			Aliases: slices.Clone(device.aliases),
		})
	}
	slices.Sort(baseline.Backends)
	return baseline
}

func marshalDriftBaseline(baseline driftBaseline) []byte {
	if baseline.empty() {
		return nil
	}
	encoded, err := json.Marshal(baseline)
	if err != nil {
		return nil
	}
	return encoded
}

// hardwareProbesClean reports whether the node probed at least one backend and
// every backend it probed passed. A skipped backend is not a failure — it means
// the node cannot open the devices, which is the normal reading for a proxy
// pointed at a cluster-wide hw_device — and a report that cannot be parsed is
// not evidence of anything.
//
// The "at least one" half matters as much as the "every" half. A GPU that
// disappears completely leaves a report with no detected_backends at all, and a
// loop over an empty list finds no failure: taking that as clean would clear a
// standing drift note on the next unrelated hash change — a reboot moving
// boot_id is enough — and tell an operator the node recovered while its card is
// still gone. Recovery has to be evidenced by hardware that was actually
// probed, not by the absence of anything to probe.
func hardwareProbesClean(payload []byte) bool {
	var current capabilityDriftView
	if json.Unmarshal(payload, &current) != nil {
		return false
	}
	probed := false
	for _, backend := range current.DetectedBackends {
		if backend.Skipped {
			continue
		}
		if !backend.Verified {
			return false
		}
		probed = true
	}
	return probed
}

// hardwareProbesEvidenced reports whether this report contains probe evidence at
// all: at least one backend that was actually probed rather than skipped.
//
// It is the weaker of the two, and it is what a note with a baseline is judged
// against. Requiring every backend to verify latches a note forever on a node
// that carries one which has never worked — VAAPI failing beside a working QSV
// is an ordinary mixed host — even after the render device the note is about
// comes back and the backend that used it verifies again. Which backends had to
// return is the baseline's question, and recoveredBy answers it precisely; this
// only rules out the empty report, where a loop over no backends finds no
// failure and would otherwise read as recovery.
func hardwareProbesEvidenced(payload []byte) bool {
	var current capabilityDriftView
	if json.Unmarshal(payload, &current) != nil {
		return false
	}
	for _, backend := range current.DetectedBackends {
		if !backend.Skipped {
			return true
		}
	}
	return false
}

// computeCapabilityDrift compares the report a node just served against the one
// it replaces. parsed is false when either payload cannot be read, in which case
// the drift is unknown rather than empty and callers must not treat it as
// evidence of recovery.
func computeCapabilityDrift(stored, payload []byte) (drift capabilityDrift, parsed bool) {
	if len(stored) == 0 {
		return capabilityDrift{first: true}, true
	}
	var previous, current capabilityDriftView
	if json.Unmarshal(stored, &previous) != nil || json.Unmarshal(payload, &current) != nil {
		return capabilityDrift{}, false
	}
	drift.previousResolved = previous.Resolved
	drift.resolved = current.Resolved
	// Skipped is carried, not flattened into "not verified": it means no probe
	// ran because the node cannot open the backend's configured devices, which
	// is a statement about access rather than about hardware. Treating it as a
	// loss also contradicted hardwareProbesClean, which counts a skipped
	// backend as clean — the note would be set by one rule and cleared by the
	// other on the next hash change, flapping without anything having changed.
	type probeOutcome struct{ verified, skipped bool }
	now := make(map[string]probeOutcome, len(current.DetectedBackends))
	for _, backend := range current.DetectedBackends {
		now[backend.Backend] = probeOutcome{verified: backend.Verified, skipped: backend.Skipped}
	}
	for _, backend := range previous.DetectedBackends {
		if !backend.Verified {
			continue
		}
		outcome, reported := now[backend.Backend]
		// Absent is not lost. Detection only probes the backends the configured
		// hw_device gives it candidates for, so a backend vanishes from the
		// report both when its hardware went away *and* when an operator simply
		// pointed the node at something else — moving hw_device from a QSV
		// render path to an NVENC index legitimately stops QSV being reported.
		// Latching drift on that demands a backend verify again that the node is
		// deliberately no longer configured for, which nothing can ever satisfy.
		// Hardware actually disappearing shows up in render_devices, which is
		// the host's own inventory and owes nothing to the configuration, and is
		// compared separately below.
		if !reported || outcome.verified || outcome.skipped {
			continue
		}
		drift.lostBackends = append(drift.lostBackends, backend.Backend)
	}
	drift.lostDevices = lostRenderDevices(previous, current)
	drift.lostDeviceAliases = lostRenderDeviceEntries(previous, current)
	return drift, true
}

// logCapabilityChange records what changed between the stored report and the
// new one. Losing a verified backend or a render device is the case worth
// waking an operator for: it means a node that was picked for hardware work
// silently became less capable, which otherwise only shows up as slow or
// failing transcodes.
func logCapabilityChange(ctx context.Context, n *Node, drift capabilityDrift, parsed bool) {
	if !parsed {
		return
	}
	if drift.first {
		slog.InfoContext(ctx, "node capabilities stored", "component", "nodepool",
			"id", n.ID, "name", n.Name, "url", n.URL)
		return
	}
	if !drift.regressed() || n.Type == NodeTypeProxy {
		// See refreshCapabilities: a proxy reports no hardware, so a "lost"
		// backend or device here is the old report's inventory going away rather
		// than the node's, and warning about it would page an operator for an
		// upgrade.
		return
	}
	slog.WarnContext(ctx, "node capability drift", "component", "nodepool",
		"id", n.ID, "name", n.Name, "url", n.URL,
		"lost_verified_backends", drift.lostBackends, "lost_render_devices", drift.lostDevices,
		"previous_resolved", drift.previousResolved, "resolved", drift.resolved)
}
