package handlers

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/telemetry"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// forceReloadRequest shares URL/auth construction with the frozen bridge.
func (h *NodeHandler) forceReloadRequest(ctx context.Context, node *nodepool.Node) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nodepool.NodeEndpoint(node.URL, "/admin/force-reload"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.jwtSecret)
	return req, nil
}
func (h *NodeHandler) ForceReloadAdminNodes(ctx context.Context) ([]ForceReloadResult, error) {
	if h == nil || h.repo == nil {
		return nil, ErrAdminNodesUnavailable
	}
	all, err := h.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	nodes := make([]*nodepool.Node, 0, len(all))
	for _, node := range all {
		if node != nil && node.Enabled {
			copy := *node
			nodes = append(nodes, &copy)
		}
	}
	results := make([]ForceReloadResult, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Go(func() { results[i] = h.forceReloadAdminNode(ctx, node) })
	}
	wg.Wait()
	return results, nil
}
func (h *NodeHandler) ForceReloadAdminNode(ctx context.Context, id int) ([]ForceReloadResult, error) {
	if h == nil || h.repo == nil {
		return nil, ErrAdminNodesUnavailable
	}
	node, err := h.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	copy := *node
	return []ForceReloadResult{h.forceReloadAdminNode(ctx, &copy)}, nil
}
func (h *NodeHandler) forceReloadAdminNode(ctx context.Context, node *nodepool.Node) ForceReloadResult {
	result := ForceReloadResult{NodeID: node.ID, NodeName: node.Name, Status: nodeReprobeFailed}
	req, err := h.forceReloadRequest(ctx, node)
	if err != nil {
		return result
	}
	// A redirect is a second dispatch, never part of this nonretryable command.
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := telemetry.DoTrustedNode(client, req, "reload")
	if err != nil {
		return result
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		result.Status = "ok"
	}
	return result
}
