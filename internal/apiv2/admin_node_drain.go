package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// AdminNodeDrainService records and lifts administrator drain requests.
type AdminNodeDrainService interface {
	SetAdminNodeDrain(ctx context.Context, id int, requested bool) (*nodepool.Node, error)
}

type AdminNodeDrain struct {
	NodeID         ID     `json:"node_id"`
	DrainRequested bool   `json:"drain_requested" doc:"An administrator asked the node to drain. Placement skips it at once on this server and within seconds on the others."`
	DrainState     string `json:"drain_state" enum:"none,draining,drained" doc:"What the node reported on its last health check. It reaches draining once the node has read the request, and drained when it has no work left."`
	ActiveJobs     int    `json:"active_jobs" doc:"Active jobs at the node's last health check."`
}
type AdminNodeDrainOutput struct{ Body AdminNodeDrain }

// AdminNodeCapabilities describes node administration support in this build.
type AdminNodeCapabilities struct {
	Capability
	Drain bool `json:"drain" doc:"Transcode nodes can drain: the drain and resume operations, and drain_requested and drain_state on node rows."`
}
type AdminNodeCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminNodeCapabilities
}

func (c AdminNodeCapabilities) capabilityState() string { return c.State }

func adminNodeDrainOf(n *nodepool.Node) AdminNodeDrain {
	return AdminNodeDrain{NodeID: IDFromInt(int64(n.ID)), DrainRequested: n.DrainRequested, DrainState: adminNodeDrainState(n), ActiveJobs: n.ActiveJobs}
}

// adminNodeDrainState is the node's reported drain state. "none" for a proxy,
// which does not drain, and for a row read before the column existed.
func adminNodeDrainState(n *nodepool.Node) string {
	if n.DrainState == "" {
		return string(nodepool.DrainStateNone)
	}
	return string(n.DrainState)
}

func registerAdminNodeDrain(reg *Registry) {
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/nodes/capabilities", "getAdminNodeCapabilities", "admin-nodes", "Discover node administration features supported by this build."), Class: ClassActingAdmin}, func(context.Context, *CapabilityInput) (*AdminNodeCapabilitiesOutput, error) {
		state := StateAvailable
		if reg.deps.AdminNodeDrain == nil {
			state = StateNotConfigured
		}
		return &AdminNodeCapabilitiesOutput{Body: AdminNodeCapabilities{Capability: Capability{State: state}, Drain: true}}, nil
	})

	op := func(suffix, id, summary string) Operation {
		operation := Operation{Operation: humaOp("POST", Prefix+"/admin/nodes/{id}/"+suffix, id, "admin-nodes", summary), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
		// A proxy node cannot drain.
		operation.Errors = []int{http.StatusConflict}
		return operation
	}
	set := func(requested bool) func(context.Context, *AdminNodeCommandInput) (*AdminNodeDrainOutput, error) {
		return func(ctx context.Context, in *AdminNodeCommandInput) (*AdminNodeDrainOutput, error) {
			id, err := adminNodeCommandID(in)
			if err != nil {
				return nil, err
			}
			if reg.deps.AdminNodeDrain == nil {
				return nil, unavailable("administrator nodes")
			}
			node, err := reg.deps.AdminNodeDrain.SetAdminNodeDrain(ctx, id, requested)
			if errors.Is(err, handlers.ErrNodeNotDrainable) {
				return nil, NewProblem(TypeConflict, "Only transcode nodes can drain.")
			}
			if err != nil {
				return nil, adminNodeCommandProblem(err)
			}
			return &AdminNodeDrainOutput{Body: adminNodeDrainOf(node)}, nil
		}
	}
	Register(reg, op("drain", "drainAdminNode", "Stop placing new work on a transcode node while its sessions finish. The node stays pooled and health-checked; drain_state reports drained when it is safe to stop. Repeating converges on the same request."), set(true))
	Register(reg, op("resume", "resumeAdminNode", "Lift a drain request so the transcode node takes new work again. A node draining because it received SIGTERM keeps draining. Repeating converges on the same state."), set(false))
}
