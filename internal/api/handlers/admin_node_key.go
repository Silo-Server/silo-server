package handlers

import (
	"context"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// NodeKeyIssuer stores a new key on a transcode node's row and returns it.
// It is the only place the key is ever returned. A proxy is
// nodepool.ErrNodeKeyNotIssuable and a missing row nodepool.ErrNodeNotFound.
type NodeKeyIssuer func(ctx context.Context, id int) (string, error)

// SetNodeKeyIssuance wires node key issuance: issue writes the key.
func (h *NodeHandler) SetNodeKeyIssuance(issue NodeKeyIssuer) {
	if h == nil {
		return
	}
	h.issueNodeKey = issue
}

// SetNodeKeyRegistry wires this server's credential registry. Every admin
// write that changes a node's key, its existence, URL or type reloads it
// before answering, and every reconciliation pass that sees another server's
// node change asks it to reload.
func (h *NodeHandler) SetNodeKeyRegistry(registry *nodepool.KeyRegistry) {
	if h == nil {
		return
	}
	h.nodeKeyRegistry = registry
}

// refreshNodeKeys reloads this server's credential registry after a committed
// node write and returns once the registry reflects it, so a deleted node or
// a replaced key no longer signs or verifies here. The write is committed, so
// a client that disconnects now must not leave the registry behind: the
// reload is detached from ctx's cancellation and bounded. A failure is
// logged; the registry then catches up on the event, the reconciliation pass
// or its periodic reload.
func (h *NodeHandler) refreshNodeKeys(ctx context.Context, nodeID int) {
	if h.nodeKeyRegistry == nil {
		return
	}
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodePostCommitTimeout)
	defer cancel()
	if err := h.nodeKeyRegistry.Refresh(refreshCtx); err != nil {
		slog.WarnContext(ctx, "node credential reload after an admin change failed; this server applies the change on its next reload",
			"component", "api", "node_id", nodeID, "error", err)
	}
}

// IssueAdminNodeKey issues or rotates a transcode node's key and returns it.
// Each call rotates: the previous key is gone from this server's registry
// before this returns, and from the other API servers and proxies once the
// node_pool_changed event (or their periodic reload) reaches them.
func (h *NodeHandler) IssueAdminNodeKey(ctx context.Context, id int) (string, error) {
	if h == nil || h.issueNodeKey == nil {
		return "", ErrAdminNodesUnavailable
	}
	key, err := h.issueNodeKey(ctx, id)
	if err != nil {
		return "", err
	}
	h.refreshNodeKeys(ctx, id)
	// The pools carry key_issued. The event is published whether or not this
	// server's pool reload worked: it is what moves every other replica's and
	// proxy's registry to the new key, which a failed read here says nothing
	// about.
	h.reloadLocalPools(ctx)
	h.publishPoolChanged(ctx)
	return key, nil
}
