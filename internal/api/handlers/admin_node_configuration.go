package handlers

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type AdminNodeConfigurationStore interface {
	Snapshot(context.Context) ([]*nodepool.Node, int64, error)
	Create(context.Context, nodepool.CreateNodeInput) (*nodepool.Node, error)
	// Update returns the committed row and the locked pre-image it replaced.
	Update(context.Context, int, nodepool.UpdateNodeInput, func(int64) error) (node, previous *nodepool.Node, err error)
	Delete(context.Context, int, func(int64) error) error
	// Declare applies a node's own declaration and returns the committed row
	// and the pre-image it replaced.
	Declare(context.Context, int, nodepool.UpdateNodeInput) (node, previous *nodepool.Node, err error)
}

func (h *NodeHandler) SetConfigurationStore(store AdminNodeConfigurationStore) {
	h.configuration = store
	h.configurationWake = make(chan struct{}, 1)
}

func (h *NodeHandler) configurationChanged() {
	select {
	case h.configurationWake <- struct{}{}:
	default:
	}
}

// nodesChanged follows a committed create, update or delete. This server's
// credential registry reflects the write before nodesChanged returns, so a
// deleted node's key neither signs nor verifies here once the response is
// sent; its pools follow on the reconciliation pass this wakes; and
// node_pool_changed moves every other API server's pools and registry and
// every proxy's registry within seconds.
func (h *NodeHandler) nodesChanged(ctx context.Context, nodeID int) {
	h.refreshNodeKeys(ctx, nodeID)
	h.configurationChanged()
	h.publishPoolChanged(ctx)
}

func (h *NodeHandler) CreateAdminNode(ctx context.Context, input nodepool.CreateNodeInput) (*nodepool.Node, error) {
	if h.configuration == nil {
		return nil, ErrAdminNodesUnavailable
	}
	node, err := h.configuration.Create(ctx, input)
	if err != nil {
		return nil, err
	}
	h.nodesChanged(ctx, node.ID)
	return node, nil
}
func (h *NodeHandler) UpdateAdminNode(ctx context.Context, id int, input nodepool.UpdateNodeInput, guard func(int64) error) (*nodepool.Node, error) {
	if h.configuration == nil {
		return nil, ErrAdminNodesUnavailable
	}
	node, previous, err := h.configuration.Update(ctx, id, input, guard)
	if err != nil {
		return nil, err
	}
	h.nodesChanged(ctx, id)
	h.afterNodeConfigured(ctx, previous, node, true)
	return node, nil
}

// DeclareNode applies the configuration a transcode node declares from its
// environment (PUT /api/internal/nodes/self/registration), at startup and
// again every minute while it runs, then does what an administrator update
// does after a change. An unchanged declaration commits no configuration
// revision, so it publishes nothing and reloads nothing. It does not ask the
// node to reload: the node runs with what it declares and reads the rest at
// its next poll. changed reports a declaration that changed the row or its
// locks.
func (h *NodeHandler) DeclareNode(ctx context.Context, id int, input nodepool.UpdateNodeInput) (*nodepool.Node, bool, error) {
	if h.configuration == nil {
		return nil, false, ErrAdminNodesUnavailable
	}
	node, previous, err := h.configuration.Declare(ctx, id, input)
	if err != nil {
		return nil, false, err
	}
	configured := node.AdminRevision != previous.AdminRevision
	if configured {
		h.nodesChanged(ctx, id)
		h.afterNodeConfigured(ctx, previous, node, false)
	}
	return node, configured || !slices.Equal(node.EnvManaged, previous.EnvManaged), nil
}

// afterNodeConfigured is the post-commit work of a configuration change, off
// the request goroutine: when the URL or an acceleration override moved, nudge
// the worker to re-read its row (when reloadWorker) and drop this server's
// cached capabilities before the pool publishes the new policy. The response
// is the committed row and never waits on or reflects the worker's answer.
func (h *NodeHandler) afterNodeConfigured(ctx context.Context, previous, node *nodepool.Node, reloadWorker bool) {
	policyChanged := nodePolicyTargetChanged(previous, node)
	// A transcode node takes its artifact storage and its source access from
	// the core too, so a change to either is pushed the same way; neither
	// moves a capability.
	configChanged := policyChanged || transcodeNodeArtifactOverridesChanged(previous, node) ||
		transcodeNodeSourceAccessChanged(previous, node)
	detached := context.WithoutCancel(ctx)
	go func() {
		if reloadWorker && configChanged && !h.reloadNodeConfig(detached, node) && policyChanged {
			slog.WarnContext(detached, "node has not adopted its new acceleration policy yet; transcodes dispatched to it may fail until its next config poll",
				"component", "api", "node_id", node.ID, "name", node.Name)
		}
		if policyChanged && h.invalidateCapabilityCache != nil {
			h.invalidateCapabilityCache(node.URL)
		}
		if h.afterNodeUpdate != nil {
			h.afterNodeUpdate()
		}
	}()
}
func (h *NodeHandler) DeleteAdminNode(ctx context.Context, id int, guard func(int64) error) error {
	if h.configuration == nil {
		return ErrAdminNodesUnavailable
	}
	if err := h.configuration.Delete(ctx, id, guard); err != nil {
		return err
	}
	h.nodesChanged(ctx, id)
	return nil
}

// StartConfigurationReconciliation runs once per API replica, after dependency
// wiring. Startup and periodic reads recover missed wakeups, process death and
// failed reads. Every pass reapplies the durable snapshot: a late legacy event
// read can otherwise replace a newer pool even when the generation is unchanged.
// Worker policy watching remains the worker's responsibility; this loop makes no
// worker requests and does not claim a worker has reloaded or stopped sessions.
func (h *NodeHandler) StartConfigurationReconciliation(ctx context.Context) {
	if ctx == nil || h.configuration == nil {
		return
	}
	go func() {
		var previous []*nodepool.Node
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			attempt, cancel := context.WithTimeout(ctx, nodePostCommitTimeout)
			nodes, _, err := h.configuration.Snapshot(attempt)
			if err == nil {
				h.applyConfigurationSnapshot(previous, nodes)
				previous = nodes
			} else if ctx.Err() == nil {
				slog.WarnContext(ctx, "node configuration reconciliation will retry", "component", "api")
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-h.configurationWake:
			}
		}
	}()
}

// applyConfigurationSnapshot applies one reconciliation read. A node that
// appeared, disappeared or changed its configuration since the previous read
// (another server's write, or this one's) also reloads this server's
// credential registry, so the registry follows the pools.
func (h *NodeHandler) applyConfigurationSnapshot(previous, nodes []*nodepool.Node) {
	current := make(map[int]*nodepool.Node, len(nodes))
	for _, node := range nodes {
		current[node.ID] = node
	}
	changed := len(previous) != len(nodes)
	for _, old := range previous {
		next := current[old.ID]
		if next != nil && next.AdminRevision == old.AdminRevision {
			continue
		}
		changed = true
		if h.invalidateCapabilityCache != nil {
			h.invalidateCapabilityCache(old.URL)
			if next != nil && next.URL != old.URL {
				h.invalidateCapabilityCache(next.URL)
			}
		}
	}
	if changed && h.nodeKeyRegistry != nil {
		h.nodeKeyRegistry.Invalidate()
	}
	var proxy, transcode []*nodepool.Node
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		copy := *node // SetNodes normalizes URLs; retain the stored configuration.
		switch node.Type {
		case nodepool.NodeTypeProxy:
			proxy = append(proxy, &copy)
		case nodepool.NodeTypeTranscode:
			transcode = append(transcode, &copy)
		}
	}
	if h.proxyPool != nil {
		h.proxyPool.SetNodes(proxy)
	}
	if h.transcodePool != nil {
		h.transcodePool.SetNodes(transcode)
	}
}
