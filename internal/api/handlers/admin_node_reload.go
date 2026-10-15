package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/telemetry"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// NodeRecipeAuthority revokes every recipe issued for a transcode node, by
// its stable row id and by the URL older recipes name it with.
// *noderecipe.Store implements it.
type NodeRecipeAuthority interface {
	RevokeNodeID(ctx context.Context, nodeID int) error
	RevokeNode(ctx context.Context, nodeURL string) error
}

// SetRecipeAuthority wires the recipe revocation a transcode node's force
// reload performs first. Nil revokes nothing.
func (h *NodeHandler) SetRecipeAuthority(authority NodeRecipeAuthority) {
	if h == nil {
		return
	}
	h.recipeAuthority = authority
}

// revokeNodeRecipes withdraws every recipe a transcode node could resolve
// before a force reload tears its sessions down, so nothing the reload ends
// can be rebuilt or restarted from one. The row id covers recipes bound to it
// and the current URL the ones that predate the binding; an operator's retry
// after a failure revokes both again. Proxies hold no node recipes.
func (h *NodeHandler) revokeNodeRecipes(ctx context.Context, node *nodepool.Node) error {
	if h.recipeAuthority == nil || node.Type != nodepool.NodeTypeTranscode {
		return nil
	}
	var errs []error
	if node.ID > 0 {
		if err := h.recipeAuthority.RevokeNodeID(ctx, node.ID); err != nil {
			errs = append(errs, fmt.Errorf("revoke node id %d: %w", node.ID, err))
		}
	}
	if err := h.recipeAuthority.RevokeNode(ctx, node.URL); err != nil {
		errs = append(errs, fmt.Errorf("revoke node URL: %w", err))
	}
	return errors.Join(errs...)
}

// forceReloadRequest shares URL/auth construction with the frozen bridge. A
// transcode node's recipes are revoked first; when that fails the node is not
// called, so the operator's retry finds its sessions as they were.
func (h *NodeHandler) forceReloadRequest(ctx context.Context, node *nodepool.Node) (*http.Request, error) {
	if err := h.revokeNodeRecipes(ctx, node); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nodepool.NodeEndpoint(node.URL, "/admin/force-reload"), nil)
	if err != nil {
		return nil, err
	}
	if err := nodeauth.SignWith(req, node.SignTarget(), node.URL, h.nodeKeys); err != nil {
		return nil, err
	}
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
