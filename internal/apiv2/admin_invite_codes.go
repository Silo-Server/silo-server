package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type AdminInviteCodeService interface {
	ListPage(context.Context, int, int) ([]*models.InviteCode, bool, error)
	CreateNamed(context.Context, models.CreateInviteCodeInput) (*models.InviteCode, error)
	Update(context.Context, int, models.UpdateInviteCodeInput) error
	TopUp(context.Context, int, int) (*models.InviteCode, error)
	Delete(context.Context, int) error
}
type AdminInviteCode struct {
	ID        ID      `json:"id"`
	Code      string  `json:"code"`
	Label     string  `json:"label"`
	MaxUses   int     `json:"max_uses"`
	UseCount  int     `json:"use_count"`
	CreatedBy ID      `json:"created_by"`
	Enabled   bool    `json:"enabled"`
	CreatedAt Instant `json:"created_at"`
	UpdatedAt Instant `json:"updated_at"`
}
type AdminInviteCodeOutput struct{ Body AdminInviteCode }
type AdminInviteCodeListOutput struct{ Body Collection[AdminInviteCode] }
type AdminInviteCodeIDInput struct {
	ID ID `path:"id" pattern:"^[1-9][0-9]*$"`
}
type AdminInviteCodeCreateInput struct {
	RawBody []byte
	Body    struct {
		Code    string `json:"code" minLength:"1" maxLength:"128"`
		Label   string `json:"label,omitempty" maxLength:"512"`
		MaxUses int    `json:"max_uses" minimum:"1" maximum:"2147483647"`
	}
}
type AdminInviteCodeUpdateInput struct {
	ID      ID `path:"id" pattern:"^[1-9][0-9]*$"`
	RawBody []byte
	Body    struct {
		Label   *string `json:"label,omitempty" nullable:"false" maxLength:"512"`
		MaxUses *int    `json:"max_uses,omitempty" nullable:"false" minimum:"1" maximum:"2147483647"`
		Enabled *bool   `json:"enabled,omitempty" nullable:"false"`
	}
}
type AdminInviteCodeTopUpInput struct {
	ID   ID `path:"id" pattern:"^[1-9][0-9]*$"`
	Body struct {
		AdditionalUses int `json:"additional_uses" minimum:"1" maximum:"2147483647"`
	}
}
type AdminInviteCodeCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminInviteCodeCapabilitiesOutputBody
}

type AdminInviteCodeCapabilitiesOutputBody struct {
	Capability
	Available          bool `json:"available"`
	ClientSelectedCode bool `json:"client_selected_code"`
}

func inviteCodeOf(row *models.InviteCode) AdminInviteCode {
	return AdminInviteCode{ID: ID(strconv.Itoa(row.ID)), Code: row.Code, Label: row.Label, MaxUses: row.MaxUses, UseCount: row.UseCount, CreatedBy: ID(strconv.Itoa(row.CreatedBy)), Enabled: row.Enabled, CreatedAt: NewInstant(row.CreatedAt), UpdatedAt: NewInstant(row.UpdatedAt)}
}
func inviteCodeProblem(err error) *Problem {
	switch {
	case errors.Is(err, auth.ErrInviteCodeNotFound):
		return NewProblem(TypeNotFound, "Invite code not found.")
	case errors.Is(err, auth.ErrInviteCodeInvalid):
		return NewProblem(TypeValidationFailed, "Invalid invite code configuration.")
	case errors.Is(err, auth.ErrInviteCodeConflict):
		return NewProblem(TypeConflict, "This code already exists with different configuration; reload the list.")
	default:
		return serviceProblem(err)
	}
}
func inviteCodeID(id ID) (int, *Problem) {
	n, p := id.positive("path.id")
	if p != nil {
		return 0, NewProblem(TypeValidationFailed, "Invalid invite code ID.")
	}
	return n, nil
}
func registerAdminInviteCodes(reg *Registry) {
	op := func(method, path, id string, safety RetrySafety) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/invite-codes"+path, id, "admin", "Manage signup invite codes."), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, RetrySafety: safety}
		if method == http.MethodPost && path == "" {
			o.DefaultStatus = http.StatusCreated
		}
		if method == http.MethodPut || method == http.MethodDelete {
			o.DefaultStatus = http.StatusNoContent
		}
		return o
	}
	Register(reg, op("GET", "/capabilities", "getAdminInviteCodeCapabilities", ""), func(_ context.Context, _ *CapabilityInput) (*AdminInviteCodeCapabilitiesOutput, error) {
		out := new(AdminInviteCodeCapabilitiesOutput)
		out.Body.Available = reg.deps.AdminInviteCodes != nil
		out.Body.ClientSelectedCode = true
		return out, nil
	})
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, op("GET", "", "listAdminInviteCodes", ""), func(ctx context.Context, in *CursorListInput) (*AdminInviteCodeListOutput, error) {
		if reg.deps.AdminInviteCodes == nil {
			return nil, unavailable("invite codes")
		}
		scope := CursorScope{OperationID: "listAdminInviteCodes", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: "limit=" + strconv.Itoa(in.Limit), Sort: "id:desc", Tiebreaker: "id:desc"}
		before := 0
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &before); p != nil {
				return nil, p
			}
			if before <= 0 {
				return nil, NewProblem(TypeInvalidCursor, "Invalid invite code cursor.")
			}
		}
		rows, more, err := reg.deps.AdminInviteCodes.ListPage(ctx, before, in.Limit)
		if err != nil {
			return nil, inviteCodeProblem(err)
		}
		items := make([]AdminInviteCode, 0, len(rows))
		for _, row := range rows {
			items = append(items, inviteCodeOf(row))
		}
		next := ""
		if more {
			if len(rows) == 0 {
				return nil, NewProblem(TypeInternalError, "Unable to page invite codes.")
			}
			next, err = cursors.Encode(scope, rows[len(rows)-1].ID)
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminInviteCodeListOutput{Body: Paginated(items, next)}, nil
	})
	Register(reg, op("POST", "", "createAdminInviteCode", RetrySafetyUniqueConstraint), func(ctx context.Context, in *AdminInviteCodeCreateInput) (*AdminInviteCodeOutput, error) {
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		if reg.deps.AdminInviteCodes == nil {
			return nil, unavailable("invite codes")
		}
		row, err := reg.deps.AdminInviteCodes.CreateNamed(ctx, models.CreateInviteCodeInput{Code: in.Body.Code, Label: in.Body.Label, MaxUses: in.Body.MaxUses, CreatedBy: claimsFrom(ctx).UserID})
		if err != nil {
			return nil, inviteCodeProblem(err)
		}
		return &AdminInviteCodeOutput{Body: inviteCodeOf(row)}, nil
	})
	Register(reg, op("PUT", "/{id}", "updateAdminInviteCode", RetrySafetyNaturalIdempotent), func(ctx context.Context, in *AdminInviteCodeUpdateInput) (*struct{}, error) {
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		if reg.deps.AdminInviteCodes == nil {
			return nil, unavailable("invite codes")
		}
		id, p := inviteCodeID(in.ID)
		if p != nil {
			return nil, p
		}
		if err := reg.deps.AdminInviteCodes.Update(ctx, id, models.UpdateInviteCodeInput{Label: in.Body.Label, MaxUses: in.Body.MaxUses, Enabled: in.Body.Enabled}); err != nil {
			return nil, inviteCodeProblem(err)
		}
		return nil, nil
	})
	Register(reg, op("POST", "/{id}/top-up", "topUpAdminInviteCode", RetrySafetyNonRetryable), func(ctx context.Context, in *AdminInviteCodeTopUpInput) (*AdminInviteCodeOutput, error) {
		if reg.deps.AdminInviteCodes == nil {
			return nil, unavailable("invite codes")
		}
		id, p := inviteCodeID(in.ID)
		if p != nil {
			return nil, p
		}
		row, err := reg.deps.AdminInviteCodes.TopUp(ctx, id, in.Body.AdditionalUses)
		if err != nil {
			return nil, inviteCodeProblem(err)
		}
		return &AdminInviteCodeOutput{Body: inviteCodeOf(row)}, nil
	})
	Register(reg, op("DELETE", "/{id}", "deleteAdminInviteCode", RetrySafetyNaturalIdempotent), func(ctx context.Context, in *AdminInviteCodeIDInput) (*struct{}, error) {
		if reg.deps.AdminInviteCodes == nil {
			return nil, unavailable("invite codes")
		}
		id, p := inviteCodeID(in.ID)
		if p != nil {
			return nil, p
		}
		if err := reg.deps.AdminInviteCodes.Delete(ctx, id); err != nil {
			return nil, inviteCodeProblem(err)
		}
		return nil, nil
	})
}

func (c AdminInviteCodeCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
