package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// NodeRepository defines the operations the NodeHandler needs on the node store.
type NodeRepository interface {
	List(ctx context.Context) ([]*nodepool.Node, error)
	GetByID(ctx context.Context, id int) (*nodepool.Node, error)
	Create(ctx context.Context, input nodepool.CreateNodeInput) (*nodepool.Node, error)
	Update(ctx context.Context, id int, input nodepool.UpdateNodeInput) (*nodepool.Node, error)
	Delete(ctx context.Context, id int) error
	UpdateHealth(ctx context.Context, id int, checkedURL string, healthy bool, activeJobs, egressKbps int, lastStats []byte) error
}

// NodeListEnabled queries enabled nodes by type for pool reload.
type NodeListEnabled interface {
	ListEnabled(ctx context.Context, nodeType string) ([]*nodepool.Node, error)
}

// NodeCapabilityRefresher fetches, persists, and publishes one node's
// capability report immediately rather than waiting for the background sweep.
//
// It is an interface only so the re-probe handler can be tested without a live
// sweep; *nodepool.HealthChecker is the single implementation, and the handler
// deliberately does not reimplement any of the fetch, drift, or persist logic
// that lives behind it.
type NodeCapabilityRefresher interface {
	RefreshNodeCapabilities(ctx context.Context, n *nodepool.Node) error
}

// NodeHandler handles CRUD operations and health checks for stream nodes.
type NodeHandler struct {
	repo          NodeRepository
	proxyPool     *nodepool.ProxyPool
	transcodePool *nodepool.TranscodePool
	lister        NodeListEnabled
	eventBus      cache.EventBus
	redisClient   *redis.Client // for reading session keys
	jwtSecret     string        // for bearer auth when calling force-reload on nodes
	// capabilities refreshes a node's stored inventory on demand; nil in a
	// deployment with no health checker, where a re-probe still runs on the node
	// and the stored row catches up on the next sweep.
	capabilities NodeCapabilityRefresher
	// invalidateCapabilityCache drops one node's cached protocol-v3 planning
	// inventory. nil outside integrated mode, where there is no playback handler
	// holding one.
	invalidateCapabilityCache func(nodeURL string)
	// afterNodeUpdate fires once the post-commit work an update kicks off has
	// finished. Tests wait on it rather than on a sleep; production leaves it
	// nil.
	afterNodeUpdate func()
	// clusterPlayback reports the cluster-wide acceleration policy, which is
	// what a node without an override of its own runs. Read live rather than
	// snapshotted, because it is hot-reloadable. nil where nothing wired it, and
	// then a node's own override is all this handler can price from.
	clusterPlayback func() config.PlaybackConfig
}

// SetClusterPlaybackPolicy wires the live cluster-wide acceleration policy,
// which the re-probe budget needs to resolve a node that carries no override of
// its own. Set after construction like the other collaborators here.
func (h *NodeHandler) SetClusterPlaybackPolicy(policy func() config.PlaybackConfig) {
	if h == nil {
		return
	}
	h.clusterPlayback = policy
}

// playbackPolicy is the cluster-wide acceleration policy, or the zero policy
// where none was wired — which prices the default device set rather than
// nothing.
func (h *NodeHandler) playbackPolicy() config.PlaybackConfig {
	if h == nil || h.clusterPlayback == nil {
		return config.PlaybackConfig{}
	}
	return h.clusterPlayback()
}

// SetCapabilityInvalidator wires the planning-cache drop used after a node's
// acceleration policy changes. Like SetCapabilityRefresher it is set after
// construction, because the playback handler that owns the cache is built
// before the router reaches this route group.
func (h *NodeHandler) SetCapabilityInvalidator(invalidate func(nodeURL string)) {
	if h == nil {
		return
	}
	h.invalidateCapabilityCache = invalidate
}

// SetCapabilityRefresher wires the on-demand capability refresh used after a
// re-probe. It is set after construction because the health checker is built
// before the router and owned by the process, not by this handler.
func (h *NodeHandler) SetCapabilityRefresher(refresher NodeCapabilityRefresher) {
	if h == nil {
		return
	}
	h.capabilities = refresher
}

// NewNodeHandler creates a new NodeHandler.
func NewNodeHandler(repo NodeRepository, proxyPool *nodepool.ProxyPool, transcodePool *nodepool.TranscodePool, lister NodeListEnabled, eventBus cache.EventBus, redisClient *redis.Client, jwtSecret string) *NodeHandler {
	return &NodeHandler{
		repo:          repo,
		proxyPool:     proxyPool,
		transcodePool: transcodePool,
		lister:        lister,
		eventBus:      eventBus,
		redisClient:   redisClient,
		jwtSecret:     jwtSecret,
	}
}

// ForceReloadResult represents the result of a force-reload on a single node.
type ForceReloadResult struct {
	NodeID   int    `json:"node_id"`
	NodeName string `json:"node_name"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// checkNodeResult is the JSON response for a node health check.
type checkNodeResult struct {
	Healthy    bool `json:"healthy"`
	ActiveJobs int  `json:"active_jobs"`
	EgressKbps int  `json:"egress_kbps"`
	// CapabilitiesHash is what the node advertised on this check. It is not the
	// stored hash: an unequal pair means the background sweep has a refetch to
	// do, which is exactly what an operator checking a node wants to see.
	CapabilitiesHash string `json:"capabilities_hash,omitempty"`
}

// HandleListNodes handles GET /admin/nodes.
//
// The stored rows are served as they are: physical_gpu_keys is derived by the
// node store when a row is scanned, so the same identities the planner routes
// on are the ones an operator sees. The one thing added is the hash each node
// advertised on its last health check, which lives only in the pools — see
// overlayAdvertisedHashes.
func (h *NodeHandler) HandleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.repo.List(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "listing nodes", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list nodes")
		return
	}
	if nodes == nil {
		nodes = []*nodepool.Node{}
	}
	h.overlayAdvertisedHashes(nodes)
	writeJSON(w, http.StatusOK, nodes)
}

// overlayAdvertisedHashes copies each node's last advertised capability hash
// from the pools onto the rows this endpoint serves.
//
// The hash is an observation from the health sweep rather than stored state, so
// it exists only on the pool's copy of a node while the row comes from the
// database. Without this the field is always absent and the admin page cannot
// tell a report the node has already contradicted from a current one — the very
// case a recent last_health_check cannot rule out, because that check keeps
// succeeding while the refetch behind it fails.
func (h *NodeHandler) overlayAdvertisedHashes(nodes []*nodepool.Node) {
	advertised := make(map[int]*string, len(nodes))
	collect := func(pooled []*nodepool.Node) {
		for _, n := range pooled {
			if n != nil && n.AdvertisedCapabilitiesHash != nil {
				advertised[n.ID] = n.AdvertisedCapabilitiesHash
			}
		}
	}
	if h.proxyPool != nil {
		collect(h.proxyPool.Nodes())
	}
	if h.transcodePool != nil {
		collect(h.transcodePool.Nodes())
	}
	for _, n := range nodes {
		if n != nil {
			n.AdvertisedCapabilitiesHash = advertised[n.ID]
		}
	}
}

// HandleCreateNode handles POST /admin/nodes.
func (h *NodeHandler) HandleCreateNode(w http.ResponseWriter, r *http.Request) {
	var input nodepool.CreateNodeInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	node, err := h.repo.Create(r.Context(), input)
	if err != nil {
		// Validation errors from CreateNodeInput.Validate() are treated as 400.
		if !errors.Is(err, nodepool.ErrNodeNotFound) {
			// Check if it's a validation error (non-sentinel, non-wrapped).
			// The repository calls input.Validate() which returns plain errors.
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "creating node", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create node")
		return
	}

	writeJSON(w, http.StatusCreated, node)
	h.reloadPools(r.Context())
}

// HandleUpdateNode handles PUT /admin/nodes/{id}.
func (h *NodeHandler) HandleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid node ID")
		return
	}

	var input nodepool.UpdateNodeInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Read the row before the write so the nudge below can tell a real policy
	// change from a resubmit: the admin form posts every field on each save, so
	// a field being present says nothing about it moving.
	//
	// The URL counts for the same reason the overrides do. A partial PUT that
	// carries only a new url still repoints the row's existing overrides at a
	// different worker, and without the old row to compare against
	// nodePolicyTargetChanged has nothing to see — the replacement would keep
	// running on what it inherited until its own 60s poll.
	var previous *nodepool.Node
	if input.URL != nil || input.HWAccelOverride != nil || input.HWDeviceOverride != nil {
		previous, _ = h.repo.GetByID(r.Context(), id)
	}

	node, err := h.repo.Update(r.Context(), id, input)
	if err != nil {
		if errors.Is(err, nodepool.ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Node not found")
			return
		}
		if errors.Is(err, nodepool.ErrInvalidNodeInput) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "updating node", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update node")
		return
	}

	writeJSON(w, http.StatusOK, node)
	// Order matters: the node has to adopt its policy before this server starts
	// dispatching under it. reloadPools publishes the updated row, and
	// EffectiveHWAccel then names its backend on every start request; the node
	// itself only re-reads its row on the config watcher's 60s poll. Without the
	// nudge, an operator switching both overlays at once (QSV on a render node
	// to NVENC on a CUDA index, say) — or repointing the row at a worker that
	// has been running on what it inherited — gets up to a minute of requests
	// pairing one side's backend with the other side's device.
	// Off the request goroutine, not merely detached from its context.
	//
	// writeJSON above does not end the response — the handler returning does —
	// so anything after it is latency the operator waits through. The node nudge
	// alone is bounded at ten seconds against a worker that may be unreachable,
	// which is long enough for the admin form to sit in "Saving..." and time out
	// after a database write that already succeeded. The row is committed at
	// this point; none of what follows can change the answer already written, so
	// none of it belongs in front of the client.
	policyChanged := nodePolicyTargetChanged(previous, node)
	ctx := context.WithoutCancel(r.Context())
	go func() {
		if policyChanged {
			if !h.reloadNodeConfig(ctx, node) {
				// The node did not confirm. Its backend now comes from this
				// server's pool while its device still comes from its own
				// configuration, so until its poll catches up (within 60s) a
				// start request can pair the new backend with the old device
				// and fail.
				//
				// The policy is published anyway. Withholding it would leave an
				// override an operator has saved and can see stored never
				// reaching dispatch at all — nothing else re-reads the column —
				// which is a silent permanent misconfiguration rather than a
				// loud, bounded, self-healing one. Closing the window properly
				// means sending the effective device alongside the backend so
				// both come from one source; that is a change to the node start
				// contract, not something to slip into a policy edit.
				slog.WarnContext(ctx, "node has not adopted its new acceleration policy yet; transcodes dispatched to it may fail until its next config poll",
					"component", "api", "node_id", node.ID, "name", node.Name)
			}
			// And drop what this server believes the node can do. The v3
			// planning cache holds the tone-map executors and transformation
			// inventory the *old* backend advertised, and it stays valid for
			// its own TTL — so without this a session started in the next
			// minute is planned against the previous backend's filters and then
			// rejected by the worker that has already moved on. Dropped before
			// the pool reload, for the same reason the worker is nudged first:
			// nothing should dispatch under the new policy while stale
			// capabilities are still readable.
			if h.invalidateCapabilityCache != nil {
				h.invalidateCapabilityCache(node.URL)
			}
		}
		h.reloadPools(ctx)
		if h.afterNodeUpdate != nil {
			h.afterNodeUpdate()
		}
	}()
}

// nodePolicyTargetChanged reports whether this update changed which effective
// acceleration policy applies, or to which worker.
//
// Two ways that happens, and both open the same window. The obvious one is an
// override moving. The other is the row being repointed: the overrides may be
// byte-identical, but they now describe a *different* worker, one that has been
// running under whatever it inherited and will not learn otherwise until its own
// 60s poll. reloadPools publishes the new URL immediately, so between those two
// moments this server dispatches the row's overridden backend to a worker still
// holding its inherited device — the same mismatch, reached by a different edit.
//
// The admin form submits every field on each save, so their presence in the body
// is not evidence of a change; without this, renaming a node or editing its
// capacity would nudge it too. An unreadable previous row reports no change: the
// nudge is an optimization over the node's own config poll, and skipping it
// costs at most that interval.
func nodePolicyTargetChanged(before, after *nodepool.Node) bool {
	if before == nil || after == nil {
		return false
	}
	if !sameNodeURL(before.URL, after.URL) {
		return true
	}
	return !sameOptionalString(before.HWAccelOverride, after.HWAccelOverride) ||
		!sameOptionalString(before.HWDeviceOverride, after.HWDeviceOverride)
}

// sameNodeURL compares two stored node URLs the way the pools and the database
// fences do, so a trailing slash is not a repoint.
func sameNodeURL(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// reloadNodeConfig asks one node to re-read its configuration now, reporting
// whether it confirmed.
//
// It targets /admin/reload-config, never /admin/force-reload: the latter tears
// down every live playback session on a transcode node, which is a reasonable
// thing for an operator to ask for explicitly and an unacceptable side effect
// of saving a policy edit that the UI says applies to new transcodes.
//
// Best effort by design: the override is already stored, and the node's own
// watcher poll is the backstop, so a node that is unreachable or predates the
// route must not turn a successful update into a failed one. It is bounded
// tightly for the same reason — this runs after the response is written, but
// still on the request's goroutine and context. The caller uses the result to
// say how far out of step the node may be, not to fail the update.
func (h *NodeHandler) reloadNodeConfig(ctx context.Context, node *nodepool.Node) bool {
	if node == nil || node.URL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, nodeConfigReloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nodepool.NodeEndpoint(node.URL, "/admin/reload-config"), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+h.jwtSecret)
	resp, err := (&http.Client{Timeout: nodeConfigReloadTimeout}).Do(req)
	if err != nil {
		slog.WarnContext(ctx, "node did not reload after an acceleration override change; it will pick it up on its next config poll",
			"component", "api", "node_id", node.ID, "name", node.Name, "error", logredact.SanitizeURLError(err))
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	// The route answers 204; accepting the whole 2xx family keeps this from
	// warning about a reload that in fact succeeded, and leaves room for the
	// node's answer to gain a body later.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		slog.WarnContext(ctx, "node refused the reload after an acceleration override change; it will pick it up on its next config poll",
			"component", "api", "node_id", node.ID, "name", node.Name, "status", resp.StatusCode)
		return false
	}
	return true
}

// nodeConfigReloadTimeout bounds the post-update nudge. A config reload is a
// database read on the node, not a probe, so this only has to cover a round
// trip to a node that is up.
const nodeConfigReloadTimeout = 10 * time.Second

// HandleDeleteNode handles DELETE /admin/nodes/{id}.
func (h *NodeHandler) HandleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid node ID")
		return
	}

	err = h.repo.Delete(r.Context(), id)
	if err != nil {
		if errors.Is(err, nodepool.ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Node not found")
			return
		}
		slog.ErrorContext(r.Context(), "deleting node", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete node")
		return
	}

	w.WriteHeader(http.StatusNoContent)
	h.reloadPools(r.Context())
}

// HandleCheckNode handles POST /admin/nodes/{id}/check.
func (h *NodeHandler) HandleCheckNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid node ID")
		return
	}

	node, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, nodepool.ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Node not found")
			return
		}
		slog.ErrorContext(r.Context(), "fetching node for check", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch node")
		return
	}

	healthy, activeJobs, egressKbps, capabilitiesHash, lastStats := nodepool.CheckNode(r.Context(), node)

	if err := h.repo.UpdateHealth(r.Context(), id, node.URL, healthy, activeJobs, egressKbps, lastStats); err != nil {
		slog.ErrorContext(r.Context(), "persisting health check result", "component", "api", "node_id", id, "error", err)
	}
	// The pools get it too, exactly as the background sweep would. A manual
	// check that only wrote the row would leave the planner admitting work to a
	// node whose scratch volume this check just found full, and would pair a
	// fresh last_health_check in the database with the pool's older advertised
	// hash — which is the combination the Nodes page reads as "this inventory
	// was reconfirmed", for up to the next 30 seconds.
	h.applyHealthToPools(node, healthy, activeJobs, egressKbps, capabilitiesHash, lastStats)

	writeJSON(w, http.StatusOK, checkNodeResult{
		Healthy:          healthy,
		ActiveJobs:       activeJobs,
		EgressKbps:       egressKbps,
		CapabilitiesHash: capabilitiesHash,
	})
}

// applyHealthToPools publishes one check's result to whichever pool holds the
// node, so an operator-triggered check lands everywhere the sweep's would.
//
// Fenced on the node's URL by the pools themselves, like every other health
// write: the row can be repointed while a check is in flight.
func (h *NodeHandler) applyHealthToPools(
	node *nodepool.Node, healthy bool, activeJobs, egressKbps int, capabilitiesHash string, lastStats []byte,
) {
	checkedAt := time.Now()
	switch node.Type {
	case nodepool.NodeTypeProxy:
		if h.proxyPool != nil {
			h.proxyPool.ApplyHealth(node.ID, node.URL, healthy, activeJobs, egressKbps, capabilitiesHash, lastStats, checkedAt)
		}
	case nodepool.NodeTypeTranscode:
		if h.transcodePool != nil {
			h.transcodePool.ApplyHealth(node.ID, node.URL, healthy, activeJobs, egressKbps, capabilitiesHash, lastStats, checkedAt)
		}
	}
}

// HandleForceReloadNodes handles POST /admin/nodes/force-reload — sends a
// force-reload signal to every enabled node in parallel.
func (h *NodeHandler) HandleForceReloadNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	allNodes, err := h.repo.List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var nodes []*nodepool.Node
	for _, n := range allNodes {
		if n.Enabled {
			nodes = append(nodes, n)
		}
	}

	results := make([]ForceReloadResult, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(idx int, node *nodepool.Node) {
			defer wg.Done()
			result := ForceReloadResult{NodeID: node.ID, NodeName: node.Name}
			client := &http.Client{Timeout: 10 * time.Second}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, nodepool.NodeEndpoint(node.URL, "/admin/force-reload"), nil)
			if err != nil {
				result.Status = "error"
				result.Error = err.Error()
				results[idx] = result
				return
			}
			req.Header.Set("Authorization", "Bearer "+h.jwtSecret)
			resp, err := client.Do(req)
			if err != nil {
				result.Status = "error"
				result.Error = err.Error()
				results[idx] = result
				return
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
				result.Status = "ok"
			} else {
				result.Status = "error"
				result.Error = fmt.Sprintf("unexpected status %d", resp.StatusCode)
			}
			results[idx] = result
		}(i, n)
	}
	wg.Wait()

	type forceReloadResponse struct {
		Results []ForceReloadResult `json:"results"`
	}
	writeJSON(w, http.StatusOK, forceReloadResponse{Results: results})
}

// HandleForceReloadNode handles POST /admin/nodes/{id}/force-reload — sends a
// force-reload signal to a single node identified by its ID.
func (h *NodeHandler) HandleForceReloadNode(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "node ID must be an integer")
		return
	}

	node, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, nodepool.NodeEndpoint(node.URL, "/admin/force-reload"), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+h.jwtSecret)

	resp, err := client.Do(req)
	if err != nil {
		type forceReloadResponse struct {
			Results []ForceReloadResult `json:"results"`
		}
		writeJSON(w, http.StatusOK, forceReloadResponse{Results: []ForceReloadResult{{
			NodeID: node.ID, NodeName: node.Name,
			Status: "error", Error: err.Error(),
		}}})
		return
	}
	resp.Body.Close()

	status := "ok"
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		status = "error"
	}
	type forceReloadResponse struct {
		Results []ForceReloadResult `json:"results"`
	}
	writeJSON(w, http.StatusOK, forceReloadResponse{Results: []ForceReloadResult{{
		NodeID: node.ID, NodeName: node.Name, Status: status,
	}}})
}

// ReprobeNodeResult is the JSON response for an operator-triggered capability
// re-probe of one node.
type ReprobeNodeResult struct {
	NodeID   int    `json:"node_id"`
	NodeName string `json:"node_name"`
	// Status is "ok" when the node re-probed successfully, "error" otherwise.
	// A node that refused or could not be reached is reported here rather than
	// as an HTTP error status, matching the per-node check and force-reload
	// routes: the request to the API succeeded, the node is what failed.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Resolved is the backend the node picked after re-probing.
	Resolved string `json:"resolved,omitempty"`
	// CapabilityHash identifies the snapshot the node published. Comparing it
	// against the hash in the node list before the call is what tells an
	// operator whether the re-probe changed anything.
	CapabilityHash string `json:"capability_hash,omitempty"`
	// CapabilitiesRefreshed reports whether this server also refetched and
	// stored the node's new inventory before answering. False means the stored
	// row will catch up on a later health sweep instead.
	CapabilitiesRefreshed bool `json:"capabilities_refreshed"`
}

// nodeReprobeFallbackTimeout bounds the re-probe request to a node whose stored
// capability report does not advertise a probe budget — one that has never been
// inventoried, or that runs a build predating the advertisement.
//
// A re-probe deliberately discards the node's probe caches, so it pays the full
// cold cost: a hardware walk plus the whole tone-map matrix, each bounded by
// several ffmpeg execs. This is the same order of magnitude as the health
// sweep's own two-minute capability fetch bound, plus request slack, and is only
// a fallback: a node that has been inventoried once advertises its real budget.
const nodeReprobeFallbackTimeout = 150 * time.Second

// nodeReprobeTimeout is how long the API waits for one node's re-probe.
//
// The node itself publishes the budget its probe matrix needs in every
// capability report (probe_request_timeout_ms), which is exactly what a cold
// capability fetch is given; a node with different hardware or a slower ffmpeg
// therefore gets its own number rather than a cluster-wide guess.
//
// Floored at what the node's current policy prices, for the same reason the
// planning and download paths are: a report stored before an operator widened
// hw_device_override advertises a budget for the smaller device set, and a
// re-probe deliberately discards every cache on the node, so it runs the larger
// matrix and gets canceled at the old deadline. Nothing here learns its way out
// of that — a re-probe is exactly the request that never completes.
func (h *NodeHandler) nodeReprobeTimeout(n *nodepool.Node) time.Duration {
	cluster := h.playbackPolicy()
	return playback.ColdCapabilityRequestTimeout(
		n.StoredCapabilities(),
		n.EffectiveHWAccel(cluster.HWAccel),
		n.EffectiveHWDevice(cluster.HWDevice),
		nodeReprobeFallbackTimeout,
	)
}

// capabilityRefreshBound is how long the refresh after a re-probe may run for
// this node, asked of the thing that will enforce it.
//
// The bound is derived from the node's own advertised probe budget, so it is
// node-specific and routinely past the exported floor — a node with a large
// device set asks for well over five minutes. Computing it here from a second
// rule would be the same number derived twice, and the two would disagree
// exactly where it matters. Without a refresher wired there is no refresh to
// wait for, so the floor is all this can promise.
func (h *NodeHandler) capabilityRefreshBound(n *nodepool.Node) time.Duration {
	if bounder, ok := h.capabilities.(nodeCapabilityRefreshBounder); ok {
		return bounder.CapabilityRefreshBound(n)
	}
	return nodepool.CapabilityRefreshTimeout
}

// nodeCapabilityRefreshBounder reports how long a refresh of one node may take.
// Optional on NodeCapabilityRefresher, like the other collaborators here;
// *nodepool.HealthChecker implements it.
type nodeCapabilityRefreshBounder interface {
	CapabilityRefreshBound(n *nodepool.Node) time.Duration
}

// nodeReprobeWriteSlack covers the repository round trips and the JSON write
// that bracket the two long calls this handler makes.
const nodeReprobeWriteSlack = 15 * time.Second

// extendReprobeWriteDeadline lifts this connection's write deadline to cover the
// whole action: the node's own probe budget, then the capability refetch that
// stores the result.
//
// The API listener's WriteTimeout is 120 seconds, and this route can legitimately
// outlive it — a node with two render devices advertises a probe budget past two
// minutes on its own, before the refetch adds its bound. Without this the
// deadline expires after the node has already re-probed and this server has
// already persisted the report: the operator's browser sees a torn connection,
// the UI toasts a failure for an action that succeeded, and the obvious response
// is to run the whole cold FFmpeg matrix again. The diagnostics upload route
// extends its deadlines for exactly the same reason.
func (h *NodeHandler) extendReprobeWriteDeadline(
	w http.ResponseWriter, r *http.Request, n *nodepool.Node, probeBudget time.Duration,
) {
	budget := probeBudget + h.capabilityRefreshBound(n) + nodeReprobeWriteSlack
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(budget)); err != nil {
		// A ResponseWriter that cannot carry a deadline (a test recorder, a
		// wrapper that does not unwrap) is not a reason to refuse the action.
		slog.WarnContext(r.Context(), "node re-probe write deadline not extended", "component", "api",
			"budget", budget, "error", err)
	}
}

// HandleReprobeNode handles POST /admin/nodes/{id}/reprobe — asks one node to
// discard its cached hardware-probe verdicts and re-verify against live
// hardware, then stores the resulting inventory immediately.
//
// This is the operator's answer to hardware that stopped working underneath a
// running node. A node caches a successful probe for its whole process lifetime
// (re-verifying per request would put ffmpeg execs on the playback path), so a
// GPU that has since been removed, or whose driver was replaced, keeps reading
// "verified" until the node restarts. Nothing about that is visible in a health
// check, because the node is perfectly healthy either way. The opposite
// direction self-heals: a failed GPU probe is retried after a short negative
// TTL, so a repaired driver reaches the list on the next capability snapshot
// without this action.
//
// A node that is transcoding refuses: the probe smoke-encodes on the GPU, and a
// busy encoder would report working hardware as failed.
//
// Both halves matter: the node recomputes, and this server then refetches and
// persists the report so the admin list, the pools, and the planner's GPU
// identities agree with it without waiting up to a sweep interval. The refresh
// reuses the health checker's own machinery, so drift detection and persistence
// have exactly one implementation.
func (h *NodeHandler) HandleReprobeNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid node ID")
		return
	}

	node, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, nodepool.ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Node not found")
			return
		}
		slog.ErrorContext(r.Context(), "fetching node for re-probe", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch node")
		return
	}

	h.extendReprobeWriteDeadline(w, r, node, h.nodeReprobeTimeout(node))

	result := ReprobeNodeResult{NodeID: node.ID, NodeName: node.Name, Status: "ok"}
	reprobed, err := h.reprobeNode(r.Context(), node)
	if err != nil {
		slog.WarnContext(r.Context(), "node capability re-probe failed", "component", "api",
			"node_id", node.ID, "name", node.Name, "error", err)
		result.Status = "error"
		result.Error = err.Error()
		writeJSON(w, http.StatusOK, result)
		return
	}
	result.Resolved = reprobed.Resolved
	result.CapabilityHash = reprobed.CapabilityHash

	// The node has already recomputed at this point, so a refresh failure is
	// reported alongside a successful re-probe rather than turning it into one:
	// the next sweep will store the report, and saying the re-probe failed
	// would invite an operator to run it again for nothing.
	if h.capabilities == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	if err := h.capabilities.RefreshNodeCapabilities(r.Context(), node); err != nil {
		slog.WarnContext(r.Context(), "storing re-probed node capabilities failed", "component", "api",
			"node_id", node.ID, "name", node.Name, "error", err)
	} else {
		result.CapabilitiesRefreshed = true
	}
	writeJSON(w, http.StatusOK, result)
}

// nodeReprobeResponse is the node's own answer to /admin/reprobe-capabilities.
type nodeReprobeResponse struct {
	Resolved       string `json:"resolved"`
	CapabilityHash string `json:"capability_hash"`
}

// reprobeNode performs the bearer-authenticated re-probe call against one node,
// mirroring the force-reload client.
func (h *NodeHandler) reprobeNode(ctx context.Context, node *nodepool.Node) (nodeReprobeResponse, error) {
	timeout := h.nodeReprobeTimeout(node)
	client := &http.Client{Timeout: timeout}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nodepool.NodeEndpoint(node.URL, "/admin/reprobe-capabilities"), nil)
	if err != nil {
		return nodeReprobeResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+h.jwtSecret)

	resp, err := client.Do(req)
	if err != nil {
		return nodeReprobeResponse{}, logredact.SanitizeURLError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Read the small error body rather than discarding it: it carries the
		// node's own explanation, and reading it also lets the transport reuse
		// the connection.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		switch resp.StatusCode {
		case http.StatusServiceUnavailable:
			// The node's own degraded-snapshot answer: it kept its previous
			// hash rather than publishing a partial probe.
			return nodeReprobeResponse{}, errors.New("node could not complete its hardware probe; its previous capability report was kept")
		case http.StatusConflict:
			// The node refused because it is transcoding: a probe's smoke encode
			// competes with live sessions for encoder slots, and losing that
			// race would be published as failed hardware.
			if message := strings.TrimSpace(string(body)); message != "" {
				return nodeReprobeResponse{}, errors.New(message)
			}
			return nodeReprobeResponse{}, errors.New("node is busy transcoding; retry the re-probe when it is idle")
		}
		return nodeReprobeResponse{}, fmt.Errorf("node re-probe returned status %d", resp.StatusCode)
	}
	var decoded nodeReprobeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxNodeReprobeResponseBytes)).Decode(&decoded); err != nil {
		return nodeReprobeResponse{}, err
	}
	return decoded, nil
}

// maxNodeReprobeResponseBytes bounds the node's answer. It carries two short
// strings; the bound is only there because a node is a worker that may run on
// remote hardware.
const maxNodeReprobeResponseBytes = 8 << 10

// HandleListSessions handles GET /admin/nodes/sessions — lists active playback
// sessions from Redis, optionally filtered by node_id query parameter.
func (h *NodeHandler) HandleListSessions(w http.ResponseWriter, r *http.Request) {
	if h.redisClient == nil {
		writeError(w, http.StatusServiceUnavailable, "redis_unavailable", "Redis not configured")
		return
	}

	ctx := r.Context()
	pattern := "silo:sessions:*"

	if nodeIDStr := r.URL.Query().Get("node_id"); nodeIDStr != "" {
		nodeID, err := strconv.Atoi(nodeIDStr)
		if err == nil {
			node, err := h.repo.GetByID(ctx, nodeID)
			if err == nil {
				hashBytes := sha256.Sum256([]byte(node.URL))
				nodeHash := hex.EncodeToString(hashBytes[:4])
				pattern = "silo:sessions:" + nodeHash + ":*"
			}
		}
	}

	var sessions []json.RawMessage
	var cursor uint64
	for {
		keys, next, err := h.redisClient.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "redis_error", err.Error())
			return
		}
		for _, key := range keys {
			val, err := h.redisClient.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			sessions = append(sessions, json.RawMessage(val))
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}

	if sessions == nil {
		sessions = []json.RawMessage{}
	}

	type sessionsResponse struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	writeJSON(w, http.StatusOK, sessionsResponse{Sessions: sessions})
}

// nodePostCommitTimeout bounds work that runs after the response is written and
// therefore no longer has a client waiting on it.
const nodePostCommitTimeout = 15 * time.Second

// reloadPools refreshes the in-memory proxy and transcode pools from the
// database and tells every replica to do the same.
//
// Every caller reaches here after its response is written, so the request
// context is the wrong lifetime: a client that disconnects — or an admin who
// navigates away from a slow save — cancels it, and this would then fail both
// database reads and return without publishing EventNodePoolChanged. The row is
// already committed at that point, so this instance and every replica would go
// on dispatching under the old policy indefinitely; nothing else re-reads the
// column. Detaching from that cancellation, bounded, is what makes the write and
// its publication one outcome instead of two.
func (h *NodeHandler) reloadPools(ctx context.Context) {
	if h.lister == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodePostCommitTimeout)
	defer cancel()
	proxyNodes, proxyErr := h.lister.ListEnabled(ctx, nodepool.NodeTypeProxy)
	transcodeNodes, tcErr := h.lister.ListEnabled(ctx, nodepool.NodeTypeTranscode)
	if proxyErr != nil || tcErr != nil {
		slog.WarnContext(ctx, "node pool reload failed", "component", "api", "proxy_err", proxyErr, "transcode_err", tcErr)
		return
	}
	if h.proxyPool != nil {
		h.proxyPool.SetNodes(proxyNodes)
	}
	if h.transcodePool != nil {
		h.transcodePool.SetNodes(transcodeNodes)
	}

	if h.eventBus != nil {
		_ = h.eventBus.Publish(ctx, cache.ChannelAdmin, cache.Event{Type: cache.EventNodePoolChanged})
	}
}
