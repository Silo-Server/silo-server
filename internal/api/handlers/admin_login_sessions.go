package handlers

import (
	"context"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// adminLoginSessionStore is the part of auth.SessionRepository the admin
// login-session operations use.
type adminLoginSessionStore interface {
	ListByUserPage(ctx context.Context, userID int, after *auth.SessionKey, limit int) ([]*models.AuthSession, error)
	RevokeAsAdmin(ctx context.Context, actorID, userID int, sessionID *string) (int, error)
}

func (h *AdminHandler) AdminLoginSessionsAvailable() bool {
	return h.loginSessions != nil && h.userRepo != nil
}

func (h *AdminHandler) ListAdminLoginSessions(ctx context.Context, userID int, after *auth.SessionKey, limit int) ([]*models.AuthSession, bool, error) {
	if !h.AdminLoginSessionsAvailable() {
		return nil, false, apiError(501, "capability_unsupported", "Login-session administration is unavailable")
	}
	if _, err := h.userRepo.GetByID(ctx, userID); err != nil {
		return nil, false, err
	}
	sessions, err := h.loginSessions.ListByUserPage(ctx, userID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	if len(sessions) > limit {
		return sessions[:limit], true, nil
	}
	return sessions, false, nil
}

func (h *AdminHandler) RevokeAdminLoginSessions(ctx context.Context, userID int, sessionID *string) (int, error) {
	if !h.AdminLoginSessionsAvailable() {
		return 0, apiError(501, "capability_unsupported", "Login-session administration is unavailable")
	}
	claims := apimw.GetClaims(ctx)
	if claims == nil || claims.Role != models.RoleAdmin {
		return 0, apiError(http.StatusForbidden, "forbidden", "Admin access required")
	}
	n, err := h.loginSessions.RevokeAsAdmin(ctx, claims.UserID, userID, sessionID)
	if auth.IsSessionNotFound(err) {
		return 0, apiError(http.StatusNotFound, "not_found", "Session not found")
	}
	if err != nil {
		return 0, ownerError(err)
	}
	// Signing out everywhere also ends the account's Jellyfin-compatible
	// sessions on every node, as a password reset or disable does.
	if sessionID == nil && h.OnUserSessionsRevoked != nil {
		h.OnUserSessionsRevoked(ctx, userID)
	}
	return n, nil
}
