package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/danielgtaylor/huma/v2"
)

type AdminDashboardLayoutSaveService interface {
	SaveAdminDashboardLayout(context.Context, int, json.RawMessage, func(handlers.AdminDashboardLayoutView) error) (handlers.AdminDashboardLayoutView, error)
}
type DashboardLayoutWriteDocument json.RawMessage

func (v *DashboardLayoutWriteDocument) UnmarshalJSON(b []byte) error {
	*v = append((*v)[:0], b...)
	return nil
}
func (DashboardLayoutWriteDocument) Schema(reg huma.Registry) *huma.Schema {
	return (DashboardLayoutDocument{}).Schema(reg)
}

type AdminDashboardLayoutSaveBody struct {
	Layout DashboardLayoutWriteDocument `json:"layout"`
}
type AdminDashboardLayoutSaveInput struct {
	Body        AdminDashboardLayoutSaveBody
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminDashboardLayoutSaveOutput struct {
	ETag string `header:"ETag"`
}

func registerAdminDashboardLayoutSave(reg *Registry) {
	op := Operation{Operation: humaOp("PUT", Prefix+"/admin/dashboard/layout", "saveAdminDashboardLayout", "admin-observability", "Store this administrator account's client-owned layout object. Requires the original read validator; successful ETag acknowledges this committed write. No automatic replay."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNonRetryable}
	op.MaxBodyBytes = 16 << 10
	Register(reg, op, func(ctx context.Context, in *AdminDashboardLayoutSaveInput) (*AdminDashboardLayoutSaveOutput, error) {
		if reg.deps.AdminDashboardLayoutSaves == nil {
			return nil, unavailable("dashboard layout")
		}
		layout := bytes.TrimSpace(in.Body.Layout)
		if len(layout) == 0 || layout[0] != '{' || !json.Valid(layout) {
			return nil, NewProblem(TypeValidationFailed, "A layout object is required.")
		}
		committed, err := reg.deps.AdminDashboardLayoutSaves.SaveAdminDashboardLayout(ctx, claimsFrom(ctx).UserID, layout, func(current handlers.AdminDashboardLayoutView) error {
			_, tag, err := dashboardLayoutRepresentation(ctx, current)
			if err != nil {
				return err
			}
			if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
				if p.Status == 412 {
					return StaleVersionProblem(tag)
				}
				return p
			}
			return nil
		})
		if err != nil {
			if problem, ok := errors.AsType[*Problem](err); ok {
				return nil, problem
			}
			return nil, serviceProblem(err)
		}
		_, tag, err := dashboardLayoutRepresentation(ctx, committed)
		if err != nil {
			return nil, err
		}
		return &AdminDashboardLayoutSaveOutput{ETag: tag.String()}, nil
	})
}
