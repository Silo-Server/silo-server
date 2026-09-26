package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/jackc/pgx/v5/pgconn"
)

type AdminNodeConfigurationService interface {
	CreateAdminNode(context.Context, nodepool.CreateNodeInput) (*nodepool.Node, error)
	UpdateAdminNode(context.Context, int, nodepool.UpdateNodeInput, func(int64) error) (*nodepool.Node, error)
	DeleteAdminNode(context.Context, int, func(int64) error) error
}
type AdminNodeCreateBody struct {
	Name             string `json:"name" minLength:"1" maxLength:"1024"`
	Type             string `json:"type" enum:"proxy,transcode"`
	URL              string `json:"url" minLength:"1" maxLength:"8192"`
	PublicURL        string `json:"public_url,omitempty" maxLength:"8192"`
	Group            string `json:"group,omitempty" maxLength:"1024"`
	MaxJobs          *int   `json:"max_jobs,omitempty" nullable:"true" maximum:"2147483647" minimum:"-2147483648"`
	MaxBandwidthKbps *int   `json:"max_bandwidth_kbps,omitempty" nullable:"true" maximum:"2147483647" minimum:"-2147483648"`
}
type AdminNodeUpdateBody struct {
	Name             *string `json:"name,omitempty" minLength:"1" maxLength:"1024"`
	URL              *string `json:"url,omitempty" minLength:"1" maxLength:"8192"`
	PublicURL        *string `json:"public_url,omitempty" nullable:"true" maxLength:"8192"`
	Enabled          *bool   `json:"enabled,omitempty"`
	Group            *string `json:"group,omitempty" maxLength:"1024"`
	MaxJobs          *int    `json:"max_jobs,omitempty" maximum:"2147483647" minimum:"-2147483648"`
	MaxBandwidthKbps *int    `json:"max_bandwidth_kbps,omitempty" maximum:"2147483647" minimum:"-2147483648"`
	HWAccelOverride  *string `json:"hw_accel_override,omitempty" nullable:"true" maxLength:"128"`
	HWDeviceOverride *string `json:"hw_device_override,omitempty" nullable:"true" maxLength:"8192"`
}

func (b *AdminNodeUpdateBody) UnmarshalJSON(data []byte) error {
	var input nodepool.UpdateNodeInput
	if err := input.UnmarshalJSON(data); err != nil {
		return err
	}
	*b = AdminNodeUpdateBody(input)
	return nil
}

type AdminNodeCreateInput struct{ Body AdminNodeCreateBody }
type AdminNodeUpdateInput struct {
	ID          string `path:"id" pattern:"^[1-9][0-9]*$" maxLength:"10"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminNodeUpdateBody
}
type AdminNodeDeleteInput struct {
	ID          string `path:"id" pattern:"^[1-9][0-9]*$" maxLength:"10"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminNodeConfigurationOutput struct {
	ETag string `header:"ETag"`
	Body AdminNode
}

func adminNodeConfigurationTag(ctx context.Context, id int, revision int64) EntityTag {
	return collectionEditorTag(ctx, "admin-node", strconv.Itoa(id), revision)
}
func adminNodeConfigurationGuard(ctx context.Context, id int, match, none string) func(int64) error {
	return func(revision int64) error {
		tag := adminNodeConfigurationTag(ctx, id, revision)
		if p := EvaluateGuardedPreconditions(match, none, tag); p != nil {
			if p.Status == http.StatusPreconditionFailed {
				return StaleVersionProblem(tag)
			}
			return p
		}
		return nil
	}
}
func adminNodeConfigurationProblem(err error) error {
	if p, ok := errors.AsType[*Problem](err); ok {
		return p
	}
	if errors.Is(err, nodepool.ErrNodeConfigurationConflict) {
		return NewProblem(TypeConflict, "Node URL already has a different configuration.")
	}
	if p, ok := errors.AsType[*pgconn.PgError](err); ok && p.Code == "23505" && p.ConstraintName == "stream_nodes_url_key" {
		return NewProblem(TypeConflict, "Node URL is already configured.")
	}
	return adminNodeCommandProblem(err)
}
func adminNodeConfigurationOutput(ctx context.Context, node *nodepool.Node, err error) (*AdminNodeConfigurationOutput, error) {
	if err != nil {
		return nil, adminNodeConfigurationProblem(err)
	}
	if node == nil || node.AdminRevision <= 0 {
		return nil, NewProblem(TypeInternalError, "Node write acknowledgement is unavailable; inspect current state.")
	}
	tag := adminNodeConfigurationTag(ctx, node.ID, node.AdminRevision).String()
	body := adminNodeOf(node)
	body.ConfigETag = tag
	return &AdminNodeConfigurationOutput{ETag: tag, Body: body}, nil
}
func registerAdminNodeConfiguration(reg *Registry) {
	create := Operation{Operation: humaOp("POST", Prefix+"/admin/nodes", "createAdminNode", "admin-nodes", "Persist node configuration and durable pool invalidation. An identical natural URL configuration resolves a repeated create; conflicting configuration returns 409. No worker probe or replica completion acknowledgement."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyUniqueConstraint}
	create.DefaultStatus = http.StatusCreated
	create.Errors = append(create.Errors, http.StatusConflict)
	Register(reg, create, func(ctx context.Context, in *AdminNodeCreateInput) (*AdminNodeConfigurationOutput, error) {
		if reg.deps.AdminNodeConfiguration == nil {
			return nil, unavailable("administrator nodes")
		}
		b := in.Body
		node, err := reg.deps.AdminNodeConfiguration.CreateAdminNode(ctx, nodepool.CreateNodeInput{Name: b.Name, Type: b.Type, URL: b.URL, PublicURL: b.PublicURL, Group: b.Group, MaxJobs: b.MaxJobs, MaxBandwidthKbps: b.MaxBandwidthKbps})
		return adminNodeConfigurationOutput(ctx, node, err)
	})
	update := Operation{Operation: humaOp("PUT", Prefix+"/admin/nodes/{id}", "updateAdminNode", "admin-nodes", "Update stored node configuration under the original If-Match revision. Configuration changes advance ETag and persist pool invalidation; a no-change PUT retains ETag. Disabling alone removes new placement and routine health sampling after reconciliation, preserves the last health sample, leaves existing streams serving and does not contact the worker. The response acknowledges stored configuration, not worker reload, replica completion or session teardown."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNaturalIdempotent}
	update.Errors = append(update.Errors, http.StatusConflict)
	Register(reg, update, func(ctx context.Context, in *AdminNodeUpdateInput) (*AdminNodeConfigurationOutput, error) {
		id, err := adminNodeCommandID(&AdminNodeCommandInput{ID: in.ID})
		if err != nil {
			return nil, err
		}
		if reg.deps.AdminNodeConfiguration == nil {
			return nil, unavailable("administrator nodes")
		}
		input := nodepool.UpdateNodeInput(in.Body)
		if err := input.Validate(); err != nil {
			return nil, NewProblem(TypeValidationFailed, "Invalid node configuration.")
		}
		node, err := reg.deps.AdminNodeConfiguration.UpdateAdminNode(ctx, id, input, adminNodeConfigurationGuard(ctx, id, in.IfMatch, in.IfNoneMatch))
		return adminNodeConfigurationOutput(ctx, node, err)
	})
	remove := Operation{Operation: humaOp("DELETE", Prefix+"/admin/nodes/{id}", "deleteAdminNode", "admin-nodes", "Delete the original If-Match configuration and atomically persist pool invalidation for replica reconciliation. No worker teardown, session completion or all-replica acknowledgement. A subsequent 404 is not proof of this caller's outcome."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyDurableDispatch}
	Register(reg, remove, func(ctx context.Context, in *AdminNodeDeleteInput) (*struct{}, error) {
		id, err := adminNodeCommandID(&AdminNodeCommandInput{ID: in.ID})
		if err != nil {
			return nil, err
		}
		if reg.deps.AdminNodeConfiguration == nil {
			return nil, unavailable("administrator nodes")
		}
		if err := reg.deps.AdminNodeConfiguration.DeleteAdminNode(ctx, id, adminNodeConfigurationGuard(ctx, id, in.IfMatch, in.IfNoneMatch)); err != nil {
			return nil, adminNodeConfigurationProblem(err)
		}
		return &struct{}{}, nil
	})
}
