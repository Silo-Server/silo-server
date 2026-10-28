package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/telemetry"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/redis/go-redis/v9"
)

// NodeRepository defines the operations the NodeHandler needs on the node store.
type NodeRepository interface {
	List(ctx context.Context) ([]*nodepool.Node, error)
	GetByID(ctx context.Context, id int) (*nodepool.Node, error)
	Create(ctx context.Context, input nodepool.CreateNodeInput) (*nodepool.Node, error)
	Update(ctx context.Context, id int, input nodepool.UpdateNodeInput) (*nodepool.Node, error)
	Delete(ctx context.Context, id int) error
	UpdateHealth(ctx context.Context, id int, checkedURL string, healthy bool, activeJobs, egressKbps int, lastStats []byte, networkAccess netaccess.NodeNetworkAccess) error
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
	configuration     AdminNodeConfigurationStore
	configurationWake chan struct{}
	repo              NodeRepository
	proxyPool         *nodepool.ProxyPool
	transcodePool     *nodepool.TranscodePool
	lister            NodeListEnabled
	eventBus          cache.EventBus
	redisClient       *redis.Client // for reading session keys
	jwtSecret         string        // signs calls to nodes (see nodeauth)
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
	if nodeauth.Sign(req, node.URL, h.jwtSecret) != nil {
		return false
	}
	resp, err := telemetry.DoTrustedNode(&http.Client{Timeout: nodeConfigReloadTimeout}, req, "reload")
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

// applyHealthToPools publishes one check's result to whichever pool holds the
// node, so an operator-triggered check lands everywhere the sweep's would.
//
// Fenced on the node's URL by the pools themselves, like every other health
// write: the row can be repointed while a check is in flight.
func (h *NodeHandler) applyHealthToPools(node *nodepool.Node, report nodepool.HealthReport) {
	checkedAt := time.Now()
	switch node.Type {
	case nodepool.NodeTypeProxy:
		if h.proxyPool != nil {
			h.proxyPool.ApplyHealthReport(node.ID, node.URL, report, checkedAt)
		}
	case nodepool.NodeTypeTranscode:
		if h.transcodePool != nil {
			h.transcodePool.ApplyHealthReport(node.ID, node.URL, report, checkedAt)
		}
	}
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

// nodeReprobeResponse is the node's own answer to /admin/reprobe-capabilities.
type nodeReprobeResponse struct {
	Resolved       string `json:"resolved"`
	CapabilityHash string `json:"capability_hash"`
}

// reprobeNode performs the signed re-probe call against one node,
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
	if err := nodeauth.Sign(req, node.URL, h.jwtSecret); err != nil {
		return nodeReprobeResponse{}, err
	}

	resp, err := telemetry.DoTrustedNode(client, req, "reprobe")
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
