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

// SetNodeKeyIssuance wires node key issuance: issue writes the key, and
// registry, when set, is this server's credential registry, reloaded right
// after so this server signs with the new key at once.
func (h *NodeHandler) SetNodeKeyIssuance(issue NodeKeyIssuer, registry *nodepool.KeyRegistry) {
	if h == nil {
		return
	}
	h.issueNodeKey = issue
	h.nodeKeyRegistry = registry
}

// IssueAdminNodeKey issues or rotates a transcode node's key and returns it.
// Each call rotates: the previous key stops working on this server before
// this returns, and on the other API servers and proxies once the
// node_pool_changed event (or their periodic reload) reaches them.
func (h *NodeHandler) IssueAdminNodeKey(ctx context.Context, id int) (string, error) {
	if h == nil || h.issueNodeKey == nil {
		return "", ErrAdminNodesUnavailable
	}
	key, err := h.issueNodeKey(ctx, id)
	if err != nil {
		return "", err
	}
	if h.nodeKeyRegistry != nil {
		// The key is committed, so a client that disconnects now must not
		// leave this server signing with the old one.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodePostCommitTimeout)
		if err := h.nodeKeyRegistry.Refresh(refreshCtx); err != nil {
			slog.WarnContext(ctx, "node credential reload after issuing a key failed; this server adopts the key on its next reload",
				"component", "api", "node_id", id, "error", err)
		}
		cancel()
	}
	// The pools carry key_issued, and the event moves every other replica's
	// registry along with its pools.
	h.reloadPools(ctx)
	return key, nil
}
