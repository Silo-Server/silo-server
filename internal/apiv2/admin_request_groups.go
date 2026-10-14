package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

const (
	opGetAdminRequestGroupLimit    = "getAdminRequestGroupLimit"
	opUpdateAdminRequestGroupLimit = "updateAdminRequestGroupLimit"
)

// adminRequestGroups is the access-group slice of the request service.
type adminRequestGroups interface {
	GetGroupLimit(context.Context, mediarequests.Viewer, int64) (*mediarequests.GroupLimit, error)
	UpsertGroupLimitConditional(context.Context, mediarequests.Viewer, mediarequests.GroupLimit, int64) (*mediarequests.GroupLimit, error)
}

// AdminRequestGroupLimit is an access group's request approval and quota. Its
// members' own limits win over it, and it wins over the server-wide settings.
type AdminRequestGroupLimit struct {
	GroupID      ID     `json:"group_id" example:"2"`
	LimitMode    string `json:"limit_mode" enum:"inherit,custom,unlimited" doc:"inherit uses the server-wide limit; custom uses max_requests per window_days" example:"custom"`
	MaxRequests  *int   `json:"max_requests" nullable:"true" minimum:"0" example:"10"`
	WindowDays   *int   `json:"window_days" nullable:"true" minimum:"1" example:"7"`
	ApprovalMode string `json:"approval_mode" enum:"inherit,manual,auto" doc:"inherit uses the server-wide approval setting" example:"manual"`
}

type AdminRequestGroupLimitBody struct {
	LimitMode    string `json:"limit_mode" enum:"inherit,custom,unlimited"`
	MaxRequests  *int   `json:"max_requests" nullable:"true" minimum:"0"`
	WindowDays   *int   `json:"window_days" nullable:"true" minimum:"1"`
	ApprovalMode string `json:"approval_mode" enum:"inherit,manual,auto"`
}

type AdminRequestGroupInput struct {
	GroupID ID `path:"group_id" pattern:"^[1-9][0-9]*$" doc:"The access group" example:"2"`
}

type AdminRequestGroupLimitInput struct {
	AdminRequestGroupInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminRequestGroupLimitBody
}

type AdminRequestGroupLimitOutput struct {
	ETag string `header:"ETag"`
	Body AdminRequestGroupLimit
}

func registerAdminRequestGroups(reg *Registry) {
	op := func(method, id, summary string, guard bool) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/request-groups/{group_id}/limit", id, "admin", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, Guarded: guard}
		o.Errors = []int{http.StatusNotFound}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodGet, opGetAdminRequestGroupLimit, "Get an access group's request approval and limit.", false), reg.getAdminRequestGroupLimit)
	Register(reg, op(http.MethodPut, opUpdateAdminRequestGroupLimit, "Replace an access group's request approval and limit.", true), reg.updateAdminRequestGroupLimit)
}

func (reg *Registry) adminRequestGroupService() (adminRequestGroups, *Problem) {
	s, ok := reg.deps.AdminRequests.(adminRequestGroups)
	if !ok {
		return nil, unavailable("request access by group")
	}
	return s, nil
}

func adminGroupLimitOf(r *mediarequests.GroupLimit) AdminRequestGroupLimit {
	return AdminRequestGroupLimit{IDFromInt(r.GroupID), string(r.LimitMode), r.MaxRequests, r.WindowDays, string(r.ApprovalMode)}
}

func adminRequestGroupID(in AdminRequestGroupInput) (int64, *Problem) {
	id, err := strconv.ParseInt(string(in.GroupID), 10, 64)
	if err != nil || id <= 0 {
		return 0, NewProblem(TypeValidationFailed, "Invalid access group ID.")
	}
	return id, nil
}

func (reg *Registry) getAdminRequestGroupLimit(ctx context.Context, in *AdminRequestGroupInput) (*AdminRequestGroupLimitOutput, error) {
	s, p := reg.adminRequestGroupService()
	if p != nil {
		return nil, p
	}
	id, p := adminRequestGroupID(*in)
	if p != nil {
		return nil, p
	}
	r, err := s.GetGroupLimit(ctx, adminRequestViewer(ctx), id)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestGroupLimitOutput{ETag: adminRequestTag(ctx, "group-limit", string(in.GroupID), r.Revision).String(), Body: adminGroupLimitOf(r)}, nil
}

func (reg *Registry) updateAdminRequestGroupLimit(ctx context.Context, in *AdminRequestGroupLimitInput) (*AdminRequestGroupLimitOutput, error) {
	s, p := reg.adminRequestGroupService()
	if p != nil {
		return nil, p
	}
	id, p := adminRequestGroupID(in.AdminRequestGroupInput)
	if p != nil {
		return nil, p
	}
	v := adminRequestViewer(ctx)
	r, err := s.GetGroupLimit(ctx, v, id)
	if err != nil {
		return nil, requestProblem(err)
	}
	rev, p := adminRequestGuard(AdminRequestPreconditions{in.IfMatch, in.IfNoneMatch}, adminRequestTag(ctx, "group-limit", string(in.GroupID), r.Revision), r.Revision)
	if p != nil {
		return nil, p
	}
	b := in.Body
	r, err = s.UpsertGroupLimitConditional(ctx, v, mediarequests.GroupLimit{GroupID: id, LimitMode: mediarequests.LimitMode(b.LimitMode), MaxRequests: b.MaxRequests, WindowDays: b.WindowDays, ApprovalMode: mediarequests.ApprovalMode(b.ApprovalMode)}, rev)
	if errors.Is(err, mediarequests.ErrStaleRevision) {
		current, e := reg.getAdminRequestGroupLimit(ctx, &in.AdminRequestGroupInput)
		if e != nil {
			return nil, e
		}
		return nil, NewProblem(TypePreconditionFailed, "The access group's request limit changed; reload before saving.").WithHeader("ETag", current.ETag)
	}
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestGroupLimitOutput{ETag: adminRequestTag(ctx, "group-limit", string(in.GroupID), r.Revision).String(), Body: adminGroupLimitOf(r)}, nil
}
