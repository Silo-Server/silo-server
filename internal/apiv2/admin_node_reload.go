package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminNodeReloadService interface {
	ForceReloadAdminNodes(context.Context) ([]handlers.ForceReloadResult, error)
	ForceReloadAdminNode(context.Context, int) ([]handlers.ForceReloadResult, error)
}
type AdminNodeReloadResult struct {
	NodeID   ID     `json:"node_id"`
	NodeName string `json:"node_name"`
	Status   string `json:"status" enum:"ok,error"`
	Error    string `json:"error,omitempty"`
}
type AdminNodeReloadOutput struct {
	Body struct {
		Results []AdminNodeReloadResult `json:"results"`
	}
}

func adminNodeReloadOutput(rows []handlers.ForceReloadResult, err error) (*AdminNodeReloadOutput, error) {
	if err != nil {
		return nil, adminNodeCommandProblem(err)
	}
	out := new(AdminNodeReloadOutput)
	out.Body.Results = make([]AdminNodeReloadResult, 0, len(rows))
	for _, row := range rows {
		result := AdminNodeReloadResult{NodeID: IDFromInt(int64(row.NodeID)), NodeName: row.NodeName, Status: row.Status}
		if row.Status != "ok" {
			result.Error = "Node reload was refused or could not be confirmed. Inspect node state before another explicit command."
		}
		out.Body.Results = append(out.Body.Results, result)
	}
	return out, nil
}
func registerAdminNodeReload(reg *Registry) {
	op := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp("POST", Prefix+path, id, "admin-nodes", summary), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	}
	Register(reg, op("/admin/nodes/force-reload", "forceReloadAdminNodes", "Synchronously request force reload from every currently enumerated enabled node in parallel, with ten-second per-node timeout and no redirects or replay. May tear down sessions. Results are individual acknowledgements, not an atomic or durable completion receipt."), func(ctx context.Context, _ *struct{}) (*AdminNodeReloadOutput, error) {
		if reg.deps.AdminNodeReload == nil {
			return nil, unavailable("administrator nodes")
		}
		rows, err := reg.deps.AdminNodeReload.ForceReloadAdminNodes(ctx)
		return adminNodeReloadOutput(rows, err)
	})
	Register(reg, op("/admin/nodes/{id}/force-reload", "forceReloadAdminNode", "Synchronously request force reload from one stored node, including a disabled node. May tear down sessions. Ten-second timeout, no redirects or automatic replay; per-node status is acknowledgement or uncertainty, not a durable receipt."), func(ctx context.Context, in *AdminNodeCommandInput) (*AdminNodeReloadOutput, error) {
		id, err := adminNodeCommandID(in)
		if err != nil {
			return nil, err
		}
		if reg.deps.AdminNodeReload == nil {
			return nil, unavailable("administrator nodes")
		}
		rows, err := reg.deps.AdminNodeReload.ForceReloadAdminNode(ctx, id)
		return adminNodeReloadOutput(rows, err)
	})
}
