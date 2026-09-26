package handlers

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

var ErrAdminNodesUnavailable = errors.New("administrator nodes are not configured")

// ReadAdminNodes overlays process-local health observations on copies, leaving
// repository and pool objects unchanged. Worker requests are not performed.
func (h *NodeHandler) ReadAdminNodes(ctx context.Context) ([]*nodepool.Node, error) {
	if h.repo == nil {
		return nil, ErrAdminNodesUnavailable
	}
	var rows []*nodepool.Node
	var err error
	if h.configuration != nil {
		rows, _, err = h.configuration.Snapshot(ctx)
	} else {
		rows, err = h.repo.List(ctx)
	}
	if err != nil {
		return nil, err
	}
	nodes := make([]*nodepool.Node, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			copy := *row
			nodes = append(nodes, &copy)
		}
	}
	h.overlayAdvertisedHashes(nodes)
	return nodes, nil
}
