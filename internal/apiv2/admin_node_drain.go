package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/workerdrain"
)

type AdminNodeDrainService interface {
	BeginAdminNodeDrain(context.Context, int, func(int64) error) (handlers.AdminNodeDrainView, error)
	ReadAdminNodeDrain(context.Context, int) (handlers.AdminNodeDrainView, error)
	CancelAdminNodeDrain(context.Context, int, func(int64) error) (*nodepool.Node, error)
}

type AdminNodeDrainCapabilities struct {
	Capability
	WorkerDrain      bool `json:"worker_drain"`
	DisabledCreation bool `json:"disabled_creation"`
}
type AdminNodeDrainCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminNodeDrainCapabilities
}
type AdminNodeDrainInput struct {
	ID          string `path:"id" pattern:"^[1-9][0-9]*$" maxLength:"10"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminNodeDrain struct {
	NodeID             ID      `json:"node_id"`
	ConfigETag         string  `json:"config_etag"`
	FenceID            string  `json:"fence_id"`
	WorkerInstanceID   string  `json:"worker_instance_id"`
	NativeServerID     string  `json:"native_server_id"`
	Fenced             bool    `json:"fenced"`
	Drained            bool    `json:"drained"`
	ActiveJobs         int     `json:"active_jobs"`
	ActiveRequests     int     `json:"active_requests"`
	ActiveReservations int     `json:"active_reservations" doc:"Conservative existing media permits retained to expiry or a positive session deny; absence from Redis is never retirement proof."`
	ObservedAt         Instant `json:"observed_at"`
}
type AdminNodeDrainOutput struct {
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminNodeDrain
}

func adminNodeDrainOutput(ctx context.Context, view handlers.AdminNodeDrainView, err error) (*AdminNodeDrainOutput, error) {
	if err != nil {
		if errors.Is(err, workerdrain.ErrUnavailable) || errors.Is(err, nodepool.ErrNodeDrainAuthorityUnavailable) {
			return nil, unavailable("worker drain confirmation; inspect current configuration before another explicit command")
		}
		if errors.Is(err, nodepool.ErrNodeDrainConflict) {
			return nil, NewProblem(TypeConflict, "Node configuration changed during worker confirmation. Read current configuration.")
		}
		return nil, adminNodeConfigurationProblem(err)
	}
	if view.Node == nil || view.Node.AdminRevision <= 0 {
		return nil, NewProblem(TypeInternalError, "Worker drain acknowledgement unavailable.")
	}
	tag := adminNodeConfigurationTag(ctx, view.Node.ID, view.Node.AdminRevision).String()
	s := view.Status
	return &AdminNodeDrainOutput{ETag: tag, CacheControl: playbackCacheControl, Body: AdminNodeDrain{NodeID: IDFromInt(int64(view.Node.ID)), ConfigETag: tag, FenceID: s.FenceID, WorkerInstanceID: s.WorkerInstanceID, NativeServerID: s.NativeServerID, Fenced: s.Fenced, Drained: s.Drained, ActiveJobs: s.ActiveJobs, ActiveRequests: s.ActiveRequests, ActiveReservations: s.ActiveReservations, ObservedAt: NewInstant(s.ObservedAt)}}, nil
}

func registerAdminNodeDrain(reg *Registry) {
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/nodes/capabilities", "getAdminNodeCapabilities", "admin-nodes", "Discover native worker retirement fencing and disabled commissioning support; each configured worker must also confirm its private drain protocol."), Class: ClassActingAdmin, ServiceBacked: true}, func(_ context.Context, _ *CapabilityInput) (*AdminNodeDrainCapabilitiesOutput, error) {
		return &AdminNodeDrainCapabilitiesOutput{Body: AdminNodeDrainCapabilities{Capability: Capability{State: configuredCapabilityState(reg.deps.AdminNodeDrain != nil || reg.deps.AdminNodeConfiguration != nil)}, WorkerDrain: reg.deps.AdminNodeDrain != nil, DisabledCreation: reg.deps.AdminNodeConfiguration != nil}}, nil
	})
	read := Operation{Operation: humaOp("GET", Prefix+"/admin/nodes/{id}/drain", "getAdminNodeDrain", "admin-nodes", "Freshly confirm worker admission fencing and execution, egress and media-permit reservations. Zero seals admission, including late dispatch and reconstruction. A changed configuration or unavailable worker cannot yield a drained receipt."), Class: ClassActingAdmin, ServiceBacked: true}
	read.Errors = append(read.Errors, http.StatusConflict)
	Register(reg, read, func(ctx context.Context, in *AdminNodeCommandInput) (*AdminNodeDrainOutput, error) {
		id, err := adminNodeCommandID(in)
		if err != nil {
			return nil, err
		}
		if reg.deps.AdminNodeDrain == nil {
			return nil, unavailable("worker drain")
		}
		view, err := reg.deps.AdminNodeDrain.ReadAdminNodeDrain(ctx, id)
		return adminNodeDrainOutput(ctx, view, err)
	})
	begin := Operation{Operation: humaOp("PUT", Prefix+"/admin/nodes/{id}/drain", "beginAdminNodeDrain", "admin-nodes", "Disable placement and durably fence new worker admission under the original If-Match configuration; existing admitted work continues. Then confirm the private worker. An unavailable confirmation can follow a committed fence; inspect configuration and GET drain before any explicit retry. Stored disable alone is insufficient."), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: true, Guarded: true, RetrySafety: RetrySafetyNaturalIdempotent}
	begin.Errors = append(begin.Errors, http.StatusConflict)
	Register(reg, begin, func(ctx context.Context, in *AdminNodeDrainInput) (*AdminNodeDrainOutput, error) {
		id, err := adminNodeCommandID(&AdminNodeCommandInput{ID: in.ID})
		if err != nil {
			return nil, err
		}
		if reg.deps.AdminNodeDrain == nil {
			return nil, unavailable("worker drain")
		}
		view, err := reg.deps.AdminNodeDrain.BeginAdminNodeDrain(ctx, id, adminNodeConfigurationGuard(ctx, id, in.IfMatch, in.IfNoneMatch))
		return adminNodeDrainOutput(ctx, view, err)
	})
	cancel := Operation{Operation: humaOp("DELETE", Prefix+"/admin/nodes/{id}/drain", "cancelAdminNodeDrain", "admin-nodes", "Explicitly cancel the durable worker admission fence under the original If-Match validator, invalidating earlier drained receipts. Placement stays disabled until a separate guarded configuration update. Workers synchronize cancellation on the next admission."), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: true, Guarded: true, RetrySafety: RetrySafetyNaturalIdempotent}
	Register(reg, cancel, func(ctx context.Context, in *AdminNodeDrainInput) (*struct{}, error) {
		id, err := adminNodeCommandID(&AdminNodeCommandInput{ID: in.ID})
		if err != nil {
			return nil, err
		}
		if reg.deps.AdminNodeDrain == nil {
			return nil, unavailable("worker drain")
		}
		_, err = reg.deps.AdminNodeDrain.CancelAdminNodeDrain(ctx, id, adminNodeConfigurationGuard(ctx, id, in.IfMatch, in.IfNoneMatch))
		if err != nil {
			return nil, adminNodeConfigurationProblem(err)
		}
		return &struct{}{}, nil
	})
}
