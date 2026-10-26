package handlers

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// ErrNodeNotDrainable is a drain request for a node that cannot drain. Only
// transcode nodes do: a proxy holds no session it could finish on its own, so
// nothing would tell when stopping it is safe.
var ErrNodeNotDrainable = errors.New("only transcode nodes can drain")

// nodeDrainRepository records an administrator's drain request.
// *nodepool.Repository implements it.
type nodeDrainRepository interface {
	SetDrainRequested(ctx context.Context, id int, requested bool) (*nodepool.Node, error)
}

// The production repository must keep the optional methods the handlers look
// for; a missing one would quietly degrade drain and health persistence.
var (
	_ nodeDrainRepository        = (*nodepool.Repository)(nil)
	_ nodeHealthReportRepository = (*nodepool.Repository)(nil)
)

// SetAdminNodeDrain asks a transcode node to drain, or lifts the request. The
// node stays in the pools, health-checked, but takes no new work while its
// sessions finish. This server's pools change before it returns; the
// node_pool_changed event moves the other API servers and the node itself,
// which rereads its own row.
func (h *NodeHandler) SetAdminNodeDrain(ctx context.Context, id int, requested bool) (*nodepool.Node, error) {
	if h == nil || h.repo == nil {
		return nil, ErrAdminNodesUnavailable
	}
	repo, ok := h.repo.(nodeDrainRepository)
	if !ok {
		return nil, ErrAdminNodesUnavailable
	}
	node, err := h.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if node.Type != nodepool.NodeTypeTranscode {
		return nil, ErrNodeNotDrainable
	}
	node, err = repo.SetDrainRequested(ctx, id, requested)
	if err != nil {
		return nil, err
	}
	h.reloadPools(ctx)
	h.configurationChanged()
	return node, nil
}
