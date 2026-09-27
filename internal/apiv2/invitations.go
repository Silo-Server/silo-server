package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/invitations"
	"github.com/Silo-Server/silo-server/internal/models"
)

const listAdminInvitationsOperation = "listAdminInvitations"

// InvitationService uses the same transactional lifecycle as legacy handlers.
type InvitationService interface {
	SupportsDefaultProfile() bool
	Lookup(context.Context, string) (*invitations.LookupResult, error)
	AcceptInvitation(context.Context, string, string, string, string) (handlers.InvitationAcceptanceView, error)
	GetByID(context.Context, int64) (*models.Invitation, error)
	ListPage(context.Context, *invitations.PageKey, int) ([]*models.Invitation, bool, error)
	Send(context.Context, invitations.SendInput) (*invitations.SendResult, error)
	Resend(context.Context, int64, int64) (*invitations.SendResult, error)
	Revoke(context.Context, int64) error
}

type InvitationCapabilities struct {
	Capability
	DefaultProfile bool `json:"default_profile" doc:"Whether the selected profile store can atomically provision a requested default profile."`
	Profileless    bool `json:"profileless"`
}
type InvitationCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         InvitationCapabilities
}
type InvitationTokenInput struct {
	Token string `path:"token" minLength:"1" maxLength:"128"`
}
type InvitationAcceptInput struct {
	Token     string `path:"token" minLength:"1" maxLength:"128"`
	UserAgent string `header:"User-Agent"`
	Body      struct {
		Password string `json:"password" minLength:"8" maxLength:"72"`
	}
}
type InvitationLookup struct {
	Email               string  `json:"email"`
	InviterName         string  `json:"inviter_name"`
	ServerName          string  `json:"server_name"`
	ExpiresAt           Instant `json:"expires_at"`
	ShowTour            bool    `json:"show_tour"`
	AcceptanceAvailable bool    `json:"acceptance_available"`
}
type InvitationLookupOutput struct{ Body InvitationLookup }

// InvitationAcceptance always means the account and invitation committed. A
// sign-in-required result must never cause a caller to replay acceptance.
type InvitationAcceptance struct {
	Status      string     `json:"status" enum:"accepted"`
	LoginStatus string     `json:"login_status" enum:"signed_in,sign_in_required"`
	Username    string     `json:"username"`
	Tokens      *TokenPair `json:"tokens,omitempty"`
}
type InvitationAcceptOutput struct{ Body InvitationAcceptance }
type AdminInvitation struct {
	ID             ID       `json:"id"`
	Email          string   `json:"email"`
	Role           string   `json:"role" enum:"user,admin"`
	AccessGroupID  *ID      `json:"access_group_id,omitempty"`
	LibraryIDs     []ID     `json:"library_ids" nullable:"true" doc:"Null inherits library access; an empty array is an explicit empty override."`
	CreateProfile  bool     `json:"create_profile"`
	ShowTour       bool     `json:"show_tour"`
	Note           string   `json:"note"`
	InvitedBy      ID       `json:"invited_by"`
	InvitedByName  string   `json:"invited_by_name"`
	Status         string   `json:"status" enum:"pending,accepted,revoked,expired"`
	ExpiresAt      Instant  `json:"expires_at"`
	AcceptedAt     *Instant `json:"accepted_at,omitempty"`
	AcceptedUserID *ID      `json:"accepted_user_id,omitempty"`
	CreatedAt      Instant  `json:"created_at"`
}
type AdminInvitationOutput struct{ Body AdminInvitation }
type AdminInvitationListOutput struct{ Body Collection[AdminInvitation] }
type AdminInvitationIDInput struct {
	ID ID `path:"id" pattern:"^[1-9][0-9]*$"`
}
type AdminInvitationCreateInput struct {
	RawBody []byte
	Body    struct {
		Email         string `json:"email" minLength:"1" maxLength:"254"`
		Role          string `json:"role,omitempty" enum:"user,admin" default:"user"`
		AccessGroupID *ID    `json:"access_group_id,omitempty" pattern:"^[1-9][0-9]*$"`
		LibraryIDs    []ID   `json:"library_ids,omitempty" doc:"Omit for inherited access; send [] for an explicit empty override."`
		CreateProfile *bool  `json:"create_profile,omitempty" default:"true"`
		ShowTour      *bool  `json:"show_tour,omitempty" default:"true"`
		Note          string `json:"note,omitempty" maxLength:"4096"`
	}
}
type InvitationDelivery struct {
	Invitation     AdminInvitation `json:"invitation"`
	ClaimURL       string          `json:"claim_url" doc:"One-time disclosure; never retained for replay. Contains the bearer claim token."`
	DeliveryStatus string          `json:"delivery_status" enum:"sent,not_configured,failed_or_unknown"`
}
type InvitationDeliveryOutput struct {
	Location string `header:"Location"`
	Body     InvitationDelivery
}

func invitationOf(inv *models.Invitation) AdminInvitation {
	out := AdminInvitation{ID: IDFromInt(inv.ID), Email: inv.Email, Role: roleOf(inv.Role), CreateProfile: inv.CreateProfile, ShowTour: inv.ShowTour, Note: inv.Note, InvitedBy: IDFromInt(inv.InvitedBy), InvitedByName: inv.InvitedByName, Status: inv.Status(time.Now()), ExpiresAt: NewInstant(inv.ExpiresAt), CreatedAt: NewInstant(inv.CreatedAt)}
	if inv.LibraryIDs != nil {
		out.LibraryIDs = make([]ID, 0, len(inv.LibraryIDs))
		for _, id := range inv.LibraryIDs {
			out.LibraryIDs = append(out.LibraryIDs, IDFromInt(int64(id)))
		}
	}
	if inv.AccessGroupID != nil {
		out.AccessGroupID = new(IDFromInt(*inv.AccessGroupID))
	}
	if inv.AcceptedUserID != nil {
		out.AcceptedUserID = new(IDFromInt(*inv.AcceptedUserID))
	}
	if inv.AcceptedAt != nil {
		out.AcceptedAt = new(NewInstant(*inv.AcceptedAt))
	}
	return out
}
func invitationProblem(err error, public bool) *Problem {
	switch {
	case errors.Is(err, invitations.ErrNotFound):
		return NewProblem(TypeNotFound, "Invitation not found or no longer available.")
	case errors.Is(err, invitations.ErrNotClaimable):
		if public {
			return NewProblem(TypeNotFound, "Invitation not found or no longer available.")
		}
		return NewProblem(TypeConflict, "The invitation changed; reload before continuing.")
	case errors.Is(err, invitations.ErrEmailTaken):
		return NewProblem(TypeConflict, "An account already uses this email or username.")
	case errors.Is(err, invitations.ErrInvalidEmail), errors.Is(err, invitations.ErrAdminGrouped):
		return NewProblem(TypeValidationFailed, "Invalid invitation configuration.")
	case errors.Is(err, invitations.ErrRoleNotAllowed):
		return NewProblem(TypePermissionDenied, "The requested role is not allowed.")
	case errors.Is(err, auth.ErrTransactionalProfileUnavailable):
		return NewProblem(TypeCapabilityUnsupported, "The selected store cannot atomically create the required profile.")
	case errors.Is(err, invitations.ErrNoLinkBase):
		return NewProblem(TypeCapabilityNotConfigured, "Invitation links are not configured.")
	default:
		return NewProblem(TypeInternalError, "Unable to process invitation.")
	}
}
func (reg *Registry) invitationService() (InvitationService, *Problem) {
	if reg.deps.Invitations == nil {
		return nil, CapabilityProblem(StateNotConfigured, "invitations")
	}
	return reg.deps.Invitations, nil
}
func invitationID(id ID) (int64, *Problem) {
	n, p := id.positive64("path.id")
	if p != nil {
		return 0, NewProblem(TypeValidationFailed, "Invalid invitation ID.")
	}
	return n, nil
}
func invitationDelivery(result *invitations.SendResult, err error) (*InvitationDeliveryOutput, error) {
	if result == nil {
		return nil, invitationProblem(err, false)
	}
	status := StateNotConfigured
	if err != nil {
		status = "failed_or_unknown"
	} else if result.EmailSent {
		status = "sent"
	}
	return &InvitationDeliveryOutput{Location: Prefix + "/admin/invitations/" + strconv.FormatInt(result.Invitation.ID, 10), Body: InvitationDelivery{Invitation: invitationOf(result.Invitation), ClaimURL: result.ClaimURL, DeliveryStatus: status}}, nil
}
func registerInvitations(reg *Registry) {
	op := func(method, path, id string, public bool) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "invitations", "Manage emailed invitations."), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method)}
		o.Errors = []int{http.StatusConflict, http.StatusTooManyRequests}
		if public {
			o.Class = ClassPublic
			o.DemoRestricted = false
			o.RateLimitBucket = "invitation"
		}
		if method == http.MethodPost {
			o.Errors = append(o.Errors, http.StatusNotImplemented)
			o.RetrySafety = RetrySafetyNonRetryable
		}
		if method == http.MethodDelete {
			o.RetrySafety = RetrySafetyNaturalIdempotent
		}
		return o
	}
	capabilities := func(_ context.Context, _ *CapabilityInput) (*InvitationCapabilitiesOutput, error) {
		state := StateNotConfigured
		profile, profileless := false, false
		if reg.deps.Invitations != nil {
			state = StateAvailable
			profile = reg.deps.Invitations.SupportsDefaultProfile()
			profileless = true
		}
		return &InvitationCapabilitiesOutput{Body: InvitationCapabilities{Capability: Capability{State: state}, DefaultProfile: profile, Profileless: profileless}}, nil
	}
	Register(reg, op(http.MethodGet, "/invitations/capabilities", "getInvitationCapabilities", true), capabilities)
	Register(reg, op(http.MethodGet, "/admin/invitations/capabilities", "getAdminInvitationCapabilities", false), capabilities)
	Register(reg, op(http.MethodGet, "/invitations/{token}", "lookupInvitation", true), func(ctx context.Context, in *InvitationTokenInput) (*InvitationLookupOutput, error) {
		svc, p := reg.invitationService()
		if p != nil {
			return nil, p
		}
		view, err := svc.Lookup(ctx, in.Token)
		if err != nil {
			return nil, invitationProblem(err, true)
		}
		return &InvitationLookupOutput{Body: InvitationLookup{Email: view.Email, InviterName: view.InviterName, ServerName: view.ServerName, ExpiresAt: NewInstant(view.ExpiresAt), ShowTour: view.ShowTour, AcceptanceAvailable: !view.CreateProfile || svc.SupportsDefaultProfile()}}, nil
	})
	accept := op(http.MethodPost, "/invitations/{token}/accept", "acceptInvitation", true)
	accept.DefaultStatus = http.StatusCreated
	Register(reg, accept, func(ctx context.Context, in *InvitationAcceptInput) (*InvitationAcceptOutput, error) {
		if len([]byte(in.Body.Password)) > 72 {
			return nil, NewProblem(TypeValidationFailed, "Password must not exceed 72 bytes.")
		}
		svc, p := reg.invitationService()
		if p != nil {
			return nil, p
		}
		view, err := svc.AcceptInvitation(ctx, in.Token, in.Body.Password, in.UserAgent, clientip.FromContext(ctx))
		if err != nil && !errors.Is(err, invitations.ErrSessionStart) {
			return nil, invitationProblem(err, true)
		}
		out := InvitationAcceptance{Status: models.InvitationStatusAccepted, LoginStatus: "sign_in_required", Username: view.Username}
		if view.Tokens != nil && err == nil {
			out.LoginStatus = "signed_in"
			out.Tokens = new(tokenPairFromView(*view.Tokens))
		}
		return &InvitationAcceptOutput{Body: out}, nil
	})
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, op(http.MethodGet, "/admin/invitations", listAdminInvitationsOperation, false), func(ctx context.Context, in *CursorListInput) (*AdminInvitationListOutput, error) {
		svc, p := reg.invitationService()
		if p != nil {
			return nil, p
		}
		scope := CursorScope{OperationID: listAdminInvitationsOperation, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: "v1/limit=" + strconv.Itoa(in.Limit), Sort: "created_at:desc", Tiebreaker: "id:desc"}
		var after *invitations.PageKey
		if in.Cursor != "" {
			after = new(invitations.PageKey)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
			if after.ID <= 0 || after.CreatedAt.IsZero() {
				return nil, NewProblem(TypeInvalidCursor, "Invalid invitation cursor.")
			}
		}
		rows, more, err := svc.ListPage(ctx, after, in.Limit)
		if err != nil {
			return nil, invitationProblem(err, false)
		}
		items := make([]AdminInvitation, 0, len(rows))
		for _, row := range rows {
			items = append(items, invitationOf(row))
		}
		next := ""
		if more {
			if len(rows) == 0 {
				return nil, NewProblem(TypeInternalError, "Unable to page invitations.")
			}
			last := rows[len(rows)-1]
			next, err = cursors.Encode(scope, invitations.PageKey{CreatedAt: last.CreatedAt, ID: last.ID})
			if err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
		}
		return &AdminInvitationListOutput{Body: Paginated(items, next)}, nil
	})
	Register(reg, op(http.MethodGet, "/admin/invitations/{id}", "getAdminInvitation", false), func(ctx context.Context, in *AdminInvitationIDInput) (*AdminInvitationOutput, error) {
		svc, p := reg.invitationService()
		if p != nil {
			return nil, p
		}
		id, p := invitationID(in.ID)
		if p != nil {
			return nil, p
		}
		row, err := svc.GetByID(ctx, id)
		if err != nil {
			return nil, invitationProblem(err, false)
		}
		return &AdminInvitationOutput{Body: invitationOf(row)}, nil
	})
	create := op(http.MethodPost, "/admin/invitations", "createAdminInvitation", false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, func(ctx context.Context, in *AdminInvitationCreateInput) (*InvitationDeliveryOutput, error) {
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		svc, p := reg.invitationService()
		if p != nil {
			return nil, p
		}
		createProfile := in.Body.CreateProfile == nil || *in.Body.CreateProfile
		showTour := in.Body.ShowTour == nil || *in.Body.ShowTour
		if createProfile && !svc.SupportsDefaultProfile() {
			return nil, invitationProblem(auth.ErrTransactionalProfileUnavailable, false)
		}
		if p := invalidEmailProblem(in.Body.Email); p != nil {
			return nil, p
		}
		input := invitations.SendInput{Email: in.Body.Email, Role: in.Body.Role, CreateProfile: createProfile, ShowTour: showTour, Note: in.Body.Note, InvitedBy: int64(claimsFrom(ctx).UserID)}
		if in.Body.AccessGroupID != nil {
			id, p := invitationID(*in.Body.AccessGroupID)
			if p != nil {
				return nil, p
			}
			input.AccessGroupID = &id
		}
		if in.Body.LibraryIDs != nil {
			input.LibraryIDs = make([]int, 0, len(in.Body.LibraryIDs))
			for _, id := range in.Body.LibraryIDs {
				n, p := id.positive("body.library_ids")
				if p != nil {
					return nil, NewProblem(TypeValidationFailed, "Invalid library ID.")
				}
				input.LibraryIDs = append(input.LibraryIDs, n)
			}
		}
		return invitationDelivery(svc.Send(ctx, input))
	})
	resend := op(http.MethodPost, "/admin/invitations/{id}/resend", "resendAdminInvitation", false)
	resend.DefaultStatus = http.StatusCreated
	Register(reg, resend, func(ctx context.Context, in *AdminInvitationIDInput) (*InvitationDeliveryOutput, error) {
		svc, p := reg.invitationService()
		if p != nil {
			return nil, p
		}
		id, p := invitationID(in.ID)
		if p != nil {
			return nil, p
		}
		prior, err := svc.GetByID(ctx, id)
		if err != nil {
			return nil, invitationProblem(err, false)
		}
		if prior.CreateProfile && !svc.SupportsDefaultProfile() {
			return nil, invitationProblem(auth.ErrTransactionalProfileUnavailable, false)
		}
		return invitationDelivery(svc.Resend(ctx, id, int64(claimsFrom(ctx).UserID)))
	})
	revoke := op(http.MethodDelete, "/admin/invitations/{id}", "revokeAdminInvitation", false)
	revoke.DefaultStatus = http.StatusNoContent
	Register(reg, revoke, func(ctx context.Context, in *AdminInvitationIDInput) (*struct{}, error) {
		svc, p := reg.invitationService()
		if p != nil {
			return nil, p
		}
		id, p := invitationID(in.ID)
		if p != nil {
			return nil, p
		}
		if err := svc.Revoke(ctx, id); err != nil {
			return nil, invitationProblem(err, false)
		}
		return &struct{}{}, nil
	})
}
