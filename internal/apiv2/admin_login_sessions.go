package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

const opListAdminUserLoginSessions = "listAdminUserLoginSessions"

type AdminLoginSessionService interface {
	AdminLoginSessionsAvailable() bool
	ListAdminLoginSessions(context.Context, int, *auth.SessionKey, int) ([]*models.AuthSession, bool, error)
	RevokeAdminLoginSessions(context.Context, int, *string) (int, error)
}

type LoginSessionCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         LoginSessionCapabilities
}
type LoginSessionCapabilities struct {
	Capability
	Available       bool `json:"available"`
	LastSeen        bool `json:"last_seen" doc:"Real authenticated request activity is recorded, at most once per minute per session"`
	AdminManagement bool `json:"admin_management" doc:"Admin login-session operations are served; normal administrator authorization and owner protection still apply"`
}

func registerLoginSessionCapabilities(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/auth/sessions/capabilities", "getLoginSessionCapabilities", "auth", "Discover login-session listing, activity tracking and revocation support."), Class: ClassAuthenticated, ServiceBacked: true}, func(context.Context, *CapabilityInput) (*LoginSessionCapabilitiesOutput, error) {
		available := reg.deps.Sessions != nil
		state := StateUnsupported
		if available {
			state = StateAvailable
		}
		return &LoginSessionCapabilitiesOutput{Body: LoginSessionCapabilities{Capability: Capability{State: state}, Available: available, LastSeen: available, AdminManagement: reg.deps.AdminLoginSessions != nil && reg.deps.AdminLoginSessions.AdminLoginSessionsAvailable()}}, nil
	})
}

type AdminLoginSessionListInput struct {
	UserID ID `path:"user_id"`
	LoginSessionListInput
}
type AdminLoginSessionInput struct {
	UserID ID `path:"user_id"`
}
type AdminLoginSessionDeleteInput struct {
	UserID    ID `path:"user_id"`
	SessionID ID `path:"session_id" pattern:"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" example:"6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c"`
}
type AdminLoginSessionsRevokedOutput struct{ Body AdminLoginSessionsRevoked }
type AdminLoginSessionsRevoked struct {
	Revoked int `json:"revoked" minimum:"0" doc:"Number of live login sessions revoked for the target account"`
}

func adminLoginSessionOperation(method, path, id, summary string) Operation {
	op := adminAccountOperation(method, path, id, false)
	op.Summary = summary
	op.Errors = []int{http.StatusNotFound, http.StatusForbidden}
	if method == http.MethodDelete {
		op.RetrySafety = RetrySafetyNaturalIdempotent
	}
	return op
}

func (reg *Registry) adminLoginSessions() (AdminLoginSessionService, *Problem) {
	svc := reg.deps.AdminLoginSessions
	if svc == nil || !svc.AdminLoginSessionsAvailable() {
		return nil, unavailable("login-session administration")
	}
	return svc, nil
}

func registerAdminLoginSessions(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	root := "/{user_id}/login-sessions"
	Register(reg, adminLoginSessionOperation(http.MethodGet, root, opListAdminUserLoginSessions, "List a user's live login sessions; this does not list playback sessions."), func(ctx context.Context, in *AdminLoginSessionListInput) (*LoginSessionCollectionOutput, error) {
		svc, p := reg.adminLoginSessions()
		if p != nil {
			return nil, p
		}
		id, p := adminAccountID(in.UserID)
		if p != nil {
			return nil, p
		}
		claims := claimsFrom(ctx)
		scope := CursorScope{OperationID: opListAdminUserLoginSessions, Security: strconv.Itoa(claims.UserID), Filter: strconv.Itoa(id), Sort: loginSessionCursorSort, Tiebreaker: "id"}
		after, p := decodeLoginSessionCursor(cursors, scope, in.Cursor)
		if p != nil {
			return nil, p
		}
		sessions, more, err := svc.ListAdminLoginSessions(ctx, id, after, in.Limit)
		if err != nil {
			return nil, adminAccountError(err)
		}
		return reg.loginSessionPage(ctx, cursors, scope, sessions, more, id)
	})
	one := adminLoginSessionOperation(http.MethodDelete, root+"/{session_id}", "deleteAdminUserLoginSession", "Revoke one login session owned by this user; other sessions and the password are unchanged.")
	one.DefaultStatus = http.StatusNoContent
	Register(reg, one, func(ctx context.Context, in *AdminLoginSessionDeleteInput) (*struct{}, error) {
		svc, p := reg.adminLoginSessions()
		if p != nil {
			return nil, p
		}
		id, p := adminAccountID(in.UserID)
		if p != nil {
			return nil, p
		}
		sessionID := string(in.SessionID)
		if _, err := svc.RevokeAdminLoginSessions(ctx, id, &sessionID); err != nil {
			return nil, adminAccountError(err)
		}
		return nil, nil
	})
	Register(reg, adminLoginSessionOperation(http.MethodDelete, root, "deleteAdminUserLoginSessions", "Revoke the account's live login sessions, including sessions it opened through View as user, and withdraw uncollected device sign-in approvals. The password, registered devices and other accounts' own sessions are unchanged."), func(ctx context.Context, in *AdminLoginSessionInput) (*AdminLoginSessionsRevokedOutput, error) {
		svc, p := reg.adminLoginSessions()
		if p != nil {
			return nil, p
		}
		id, p := adminAccountID(in.UserID)
		if p != nil {
			return nil, p
		}
		n, err := svc.RevokeAdminLoginSessions(ctx, id, nil)
		if err != nil {
			return nil, adminAccountError(err)
		}
		return &AdminLoginSessionsRevokedOutput{Body: AdminLoginSessionsRevoked{Revoked: n}}, nil
	})
}
