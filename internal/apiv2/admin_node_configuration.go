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
	URL              string `json:"url" format:"uri-reference" minLength:"1" maxLength:"8192"`
	PublicURL        string `json:"public_url,omitempty" format:"uri-reference" maxLength:"8192"`
	Group            string `json:"group,omitempty" maxLength:"1024"`
	MaxJobs          *int   `json:"max_jobs,omitempty" nullable:"true" maximum:"2147483647" minimum:"-2147483648"`
	MaxBandwidthKbps *int   `json:"max_bandwidth_kbps,omitempty" nullable:"true" maximum:"2147483647" minimum:"-2147483648"`
	SourceAccess     string `json:"source_access,omitempty" enum:"local,core" doc:"How a transcode node reads source media: local through its own library mount at the API servers' paths, or core over HTTP from the API servers, so the node needs no library mount. Absent means local. A proxy accepts only local. Requires the source_access node capability."`
}
type AdminNodeUpdateBody struct {
	Name             *string `json:"name,omitempty" minLength:"1" maxLength:"1024"`
	URL              *string `json:"url,omitempty" format:"uri-reference" minLength:"1" maxLength:"8192"`
	PublicURL        *string `json:"public_url,omitempty" format:"uri-reference" nullable:"true" maxLength:"8192"`
	Enabled          *bool   `json:"enabled,omitempty"`
	Group            *string `json:"group,omitempty" maxLength:"1024"`
	MaxJobs          *int    `json:"max_jobs,omitempty" maximum:"2147483647" minimum:"-2147483648"`
	MaxBandwidthKbps *int    `json:"max_bandwidth_kbps,omitempty" maximum:"2147483647" minimum:"-2147483648"`
	HWAccelOverride  *string `json:"hw_accel_override,omitempty" nullable:"true" maxLength:"128"`
	HWDeviceOverride *string `json:"hw_device_override,omitempty" nullable:"true" maxLength:"8192"`
	// The node fixes its artifact directory at startup, so a change applies
	// when it restarts.
	DownloadArtifactDirOverride      *string `json:"download_artifact_dir_override,omitempty" nullable:"true" maxLength:"4096" doc:"Absolute directory for this transcode node's prepared download files. Null or empty restores the inherited directory: download.artifact_dir, or when that is blank, download-artifacts inside the node's transcode directory. Applies when the node restarts."`
	DownloadArtifactMaxBytesOverride *int64  `json:"download_artifact_max_bytes_override,omitempty" nullable:"true" minimum:"-1" doc:"Storage budget in bytes for this node's prepared download files; 0 means no budget. Null or -1 restores download.artifact_max_bytes."`
	// Jobs started after a change use the new mode; running jobs keep theirs.
	SourceAccess *string `json:"source_access,omitempty" enum:"local,core" doc:"How this transcode node reads source media: local or core (see the create body). Applies to jobs the node starts after the change; running jobs keep their mode. A proxy accepts only local. Requires the source_access node capability."`
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
	if locked, ok := errors.AsType[*nodepool.EnvManagedFieldsError](err); ok {
		p := NewProblem(TypeValidationFailed, validationSummary)
		for _, field := range locked.Fields {
			p.Errors = append(p.Errors, ProblemError{Location: locationBody + "." + field, Code: codeManagedByEnvironment, Detail: "Set by the node's environment. Change it there and restart the node."})
		}
		p.promoteSoleFieldDetail()
		return p
	}
	if errors.Is(err, nodepool.ErrNodeConfigurationConflict) {
		return NewProblem(TypeConflict, "Node URL already has a different configuration.")
	}
	if errors.Is(err, nodepool.ErrInvalidNodeInput) {
		return NewProblem(TypeValidationFailed, "Invalid node configuration.")
	}
	if errors.Is(err, nodepool.ErrTranscodeNodeURLTaken) {
		return NewProblem(TypeConflict, "Another transcode node already has this URL.")
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
		node, err := reg.deps.AdminNodeConfiguration.CreateAdminNode(ctx, nodepool.CreateNodeInput{Name: b.Name, Type: b.Type, URL: b.URL, PublicURL: b.PublicURL, Group: b.Group, MaxJobs: b.MaxJobs, MaxBandwidthKbps: b.MaxBandwidthKbps, SourceAccess: b.SourceAccess})
		return adminNodeConfigurationOutput(ctx, node, err)
	})
	update := Operation{Operation: humaOp("PUT", Prefix+"/admin/nodes/{id}", "updateAdminNode", "admin-nodes", "Update stored node configuration under the original If-Match revision. A field named in the node's env_managed is set by the node's environment: a change to it is 422 validation_failed with field code managed_by_environment at body.<field>, and restating its current value is accepted. Configuration changes advance ETag and persist pool invalidation; a no-change PUT retains ETag. Disabling alone removes new placement and routine health sampling after reconciliation, preserves the last health sample, leaves existing streams serving and does not contact the worker. The response acknowledges stored configuration, not worker reload, replica completion or session teardown."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNaturalIdempotent}
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
