package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/danielgtaylor/huma/v2"
)

const dashboardLayoutAbsentRevision = "absent"

type AdminDashboardLayoutService interface {
	ReadAdminDashboardLayout(context.Context, int) (handlers.AdminDashboardLayoutView, error)
}

// DashboardLayoutDocument is the client-owned layout object; widgets and spans
// are sanitized by the consuming client, not interpreted by this reader.
type DashboardLayoutDocument map[string]any

func (DashboardLayoutDocument) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: true, Extensions: map[string]any{extExtensionBag: "admin-dashboard-layout"}}
}

type AdminDashboardLayout struct {
	Layout    *DashboardLayoutDocument `json:"layout"`
	UpdatedAt *Instant                 `json:"updated_at"`
}
type AdminDashboardLayoutInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminDashboardLayoutOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminDashboardLayout
}

func registerAdminDashboardLayout(reg *Registry) {
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/dashboard/layout", "getAdminDashboardLayout", "admin-observability", "Read this administrator account's stored layout. Null means retain the local/default arrangement. The validator includes the stored generation, including resets, and is required by layout saves."), Class: ClassActingAdmin, ServiceBacked: true, Conditional: true}
	Register(reg, op, func(ctx context.Context, in *AdminDashboardLayoutInput) (*AdminDashboardLayoutOutput, error) {
		if reg.deps.AdminDashboardLayout == nil {
			return nil, unavailable("dashboard layout")
		}
		view, err := reg.deps.AdminDashboardLayout.ReadAdminDashboardLayout(ctx, claimsFrom(ctx).UserID)
		if err != nil {
			return nil, serviceProblem(err)
		}
		body, tag, err := dashboardLayoutRepresentation(ctx, view)
		if err != nil {
			return nil, err
		}
		out := &AdminDashboardLayoutOutput{ETag: tag.String(), Body: body}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
}

func dashboardLayoutRepresentation(ctx context.Context, view handlers.AdminDashboardLayoutView) (AdminDashboardLayout, EntityTag, error) {
	var body AdminDashboardLayout
	revision := dashboardLayoutAbsentRevision
	if view.UpdatedAt != nil {
		var layout DashboardLayoutDocument
		dec := json.NewDecoder(bytes.NewReader(view.Layout))
		dec.UseNumber()
		if err := dec.Decode(&layout); err != nil || layout == nil {
			return AdminDashboardLayout{}, EntityTag{}, serviceProblem(errors.New("invalid stored dashboard layout"))
		}
		canonical, err := json.Marshal(layout)
		if err != nil {
			return AdminDashboardLayout{}, EntityTag{}, serviceProblem(err)
		}
		body.Layout = &layout
		body.UpdatedAt = new(NewInstant(*view.UpdatedAt))
		revision = string(canonical) + "/" + view.UpdatedAt.UTC().Format(time.RFC3339Nano)
	} else if len(view.Layout) != 0 {
		return AdminDashboardLayout{}, EntityTag{}, serviceProblem(errors.New("stored dashboard layout lacks timestamp"))
	}
	tag := RenderETag("admin-dashboard-layout:"+strconv.Itoa(claimsFrom(ctx).UserID)+":"+profileFrom(ctx)+":"+viewerScopeDigest(ctx), view.Revision+"/"+revision, 1)
	return body, tag, nil
}
