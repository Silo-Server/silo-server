package nodecontrol

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// RecipeRevoker invalidates every recipe issued for a transcode node: by its
// stable row id, and by the URL a card without the id names it with.
// *noderecipe.Store implements it.
type RecipeRevoker interface {
	RevokeNodeID(ctx context.Context, nodeID int) error
	RevokeNode(ctx context.Context, nodeURL string) error
}

// HandleRevokeRecipes serves POST /api/internal/nodes/self/revoke-recipes:
// the calling node, tearing its sessions down for a force reload, withdraws
// every recipe bound to it, so nothing the reload ends can be rebuilt or
// restarted from one. The core revokes by the node's authenticated id and
// then by its registered URL, never by anything in the request. A card bound
// by URL alone to a URL the row no longer has is not revoked.
//
// The node asks as part of its own teardown, under its own reload gate, so a
// force reload the node never ran revokes nothing.
func (s *Server) HandleRevokeRecipes(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.opts.Revocations == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx := r.Context()
	if err := s.opts.Revocations.RevokeNodeID(ctx, id); err != nil {
		slog.WarnContext(ctx, "revoke a node's recipes by id", "component", "nodecontrol", "node_id", id, "error", err)
		http.Error(w, "recipe revocation unavailable", http.StatusServiceUnavailable)
		return
	}
	node, err := s.identity(ctx, id)
	if errors.Is(err, nodepool.ErrNodeNotFound) {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.WarnContext(ctx, "read the revoking node", "component", "nodecontrol", "node_id", id, "error", err)
		http.Error(w, "recipe revocation unavailable", http.StatusServiceUnavailable)
		return
	}
	if node.URL != "" {
		if err := s.opts.Revocations.RevokeNode(ctx, node.URL); err != nil {
			slog.WarnContext(ctx, "revoke a node's recipes by URL", "component", "nodecontrol", "node_id", id, "error", err)
			http.Error(w, "recipe revocation unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
