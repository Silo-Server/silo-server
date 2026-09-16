package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

const nodeReprobeFailed = "error"

type AdminNodeCheckView struct {
	Healthy          bool
	ActiveJobs       int
	EgressKbps       int
	CapabilitiesHash string
	HealthPersisted  bool
}

func (h *NodeHandler) CheckAdminNode(ctx context.Context, id int) (AdminNodeCheckView, error) {
	if h == nil || h.repo == nil {
		return AdminNodeCheckView{}, ErrAdminNodesUnavailable
	}
	node, err := h.repo.GetByID(ctx, id)
	if err != nil {
		return AdminNodeCheckView{}, err
	}
	return h.checkNodeView(ctx, node), nil
}
func (h *NodeHandler) checkNodeView(ctx context.Context, node *nodepool.Node) AdminNodeCheckView {
	healthy, jobs, egress, hash, stats, networkAccess := nodepool.CheckNode(ctx, node)
	err := h.repo.UpdateHealth(ctx, node.ID, node.URL, healthy, jobs, egress, stats, networkAccess)
	if err != nil {
		slog.ErrorContext(ctx, "persisting health check result", "component", "api", "node_id", node.ID, "error", err)
	}
	h.applyHealthToPools(node, healthy, jobs, egress, hash, stats, networkAccess)
	return AdminNodeCheckView{Healthy: healthy, ActiveJobs: jobs, EgressKbps: egress, CapabilitiesHash: hash, HealthPersisted: err == nil}
}
func (h *NodeHandler) ReprobeAdminNode(w http.ResponseWriter, r *http.Request, id int) (ReprobeNodeResult, error) {
	if h == nil || h.repo == nil {
		return ReprobeNodeResult{}, ErrAdminNodesUnavailable
	}
	node, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		return ReprobeNodeResult{}, err
	}
	h.extendReprobeWriteDeadline(w, r, node, h.nodeReprobeTimeout(node))
	return h.reprobeNodeView(r.Context(), node), nil
}
func (h *NodeHandler) reprobeNodeView(ctx context.Context, node *nodepool.Node) ReprobeNodeResult {
	result := ReprobeNodeResult{NodeID: node.ID, NodeName: node.Name, Status: "ok"}
	reprobed, err := h.reprobeNode(ctx, node)
	if err != nil {
		slog.WarnContext(ctx, "node capability re-probe failed", "component", "api",
			"node_id", node.ID, "name", node.Name, "error", err)
		result.Status = nodeReprobeFailed
		result.Error = err.Error()
		return result
	}
	result.Resolved = reprobed.Resolved
	result.CapabilityHash = reprobed.CapabilityHash

	// The node has already recomputed at this point, so a refresh failure is
	// reported alongside a successful re-probe rather than turning it into one:
	// the next sweep will store the report, and saying the re-probe failed
	// would invite an operator to run it again for nothing.
	if h.capabilities == nil {
		return result
	}
	if err := h.capabilities.RefreshNodeCapabilities(ctx, node); err != nil {
		slog.WarnContext(ctx, "storing re-probed node capabilities failed", "component", "api",
			"node_id", node.ID, "name", node.Name, "error", err)
	} else {
		result.CapabilitiesRefreshed = true
	}
	return result
}
