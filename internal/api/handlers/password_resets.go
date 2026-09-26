package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/passwordreset"
)

// PasswordResetHandler adapts the reset link service for v2: the
// administrator's issue action and the public reset screen.
type PasswordResetHandler struct {
	service      *passwordreset.Service
	users        passwordResetTargets
	accessGroups access.GroupPolicyProvider
}

type passwordResetTargets interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// NewPasswordResetHandler creates a PasswordResetHandler.
func NewPasswordResetHandler(service *passwordreset.Service, users passwordResetTargets) *PasswordResetHandler {
	return &PasswordResetHandler{service: service, users: users}
}

// SetAccessGroupProvider wires the policy source for the effective download
// gate reported on the sign-in a completed reset returns.
func (h *PasswordResetHandler) SetAccessGroupProvider(provider access.GroupPolicyProvider) {
	h.accessGroups = provider
}

// PasswordResetCapabilities reports which deliveries the server can make.
func (h *PasswordResetHandler) PasswordResetCapabilities(ctx context.Context) passwordreset.Capabilities {
	return h.service.Capabilities(ctx)
}

// IssuePasswordReset issues a reset link for an account. Only the server Owner
// may reset the Owner's password, and a scoped API key may not reset an admin
// account: like setting its password, either would let the link's holder sign
// in with that account's full authority.
func (h *PasswordResetHandler) IssuePasswordReset(ctx context.Context, input passwordreset.IssueInput) (*passwordreset.IssueResult, error) {
	target, err := h.users.GetByID(ctx, input.UserID)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckOwnerTarget(actorUserID(ctx), target); err != nil {
		return nil, ownerError(err)
	}
	if actorIsScopedAPIKey(ctx) && target.Role == roleAdmin {
		return nil, apiError(http.StatusForbidden, "insufficient_scope", "A scoped API key may not reset an admin account's password")
	}
	return h.service.Issue(ctx, input)
}

// PasswordResetSelfService reports whether self-service reset is turned on
// and whether the server can deliver it.
func (h *PasswordResetHandler) PasswordResetSelfService(ctx context.Context) (enabled, configured bool, err error) {
	return h.service.SelfService(ctx)
}

// RequestPasswordReset starts a reset the account holder asked for on the
// sign-in page.
func (h *PasswordResetHandler) RequestPasswordReset(ctx context.Context, login string) error {
	return h.service.Request(ctx, login)
}

// LookupPasswordReset resolves a link for the public reset screen.
func (h *PasswordResetHandler) LookupPasswordReset(ctx context.Context, token string) (*passwordreset.LookupResult, error) {
	return h.service.Lookup(ctx, token)
}

// PasswordResetCompletionView is a completed reset: Tokens is nil when the
// sign-in that follows the committed reset failed.
type PasswordResetCompletionView struct {
	Username string
	Tokens   *TokenPairView
}

// CompletePasswordReset spends a link, sets the new password, and signs in.
func (h *PasswordResetHandler) CompletePasswordReset(ctx context.Context, token, password, device, ip string) (PasswordResetCompletionView, error) {
	pair, user, err := h.service.Complete(ctx, token, password, device, ip)
	if user == nil {
		return PasswordResetCompletionView{}, err
	}
	view := PasswordResetCompletionView{Username: user.Username}
	if err == nil {
		view.Tokens = &TokenPairView{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, ExpiresIn: pair.ExpiresIn, User: buildUserResponse(user, effectiveDownloadAllowed(ctx, user, h.accessGroups), nil, nil)}
	}
	return view, err
}
