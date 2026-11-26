package handlers

import (
	"context"
	"errors"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/signinlinks"
)

// SignInLinkHandler adapts the sign-in link service for v2: the
// administrator's issue action and the public link page.
type SignInLinkHandler struct {
	service *signinlinks.Service
}

// NewSignInLinkHandler creates a SignInLinkHandler.
func NewSignInLinkHandler(service *signinlinks.Service) *SignInLinkHandler {
	return &SignInLinkHandler{service: service}
}

// SignInLinkEmailAvailable reports whether the server can email a link.
func (h *SignInLinkHandler) SignInLinkEmailAvailable(ctx context.Context) bool {
	return h.service.EmailAvailable(ctx)
}

// IssueSignInLink makes a sign-in link for an account. Who may make one is
// who may set that account's password as an administrator: only the Owner
// for another administrator, nobody but the Owner for the Owner, only the
// Owner for an account of its own whose password sign-in is off, and never
// a scoped API key for an administrator. The service applies the rules with
// the accounts locked; this renders their refusals as an account update
// does.
func (h *SignInLinkHandler) IssueSignInLink(ctx context.Context, input signinlinks.IssueInput) (*signinlinks.IssueResult, error) {
	input.ScopedKey = apimw.IsScopedAPIKey(ctx)
	result, err := h.service.Issue(ctx, input)
	if errors.Is(err, auth.ErrScopedKeyAdminCredentials) {
		return nil, apiError(http.StatusForbidden, "insufficient_scope", "A scoped API key may not make a sign-in link for an admin account")
	}
	return result, ownerError(err)
}

// RedeemSignInLink spends a link for the public page.
func (h *SignInLinkHandler) RedeemSignInLink(ctx context.Context, token string, keysOn bool) (auth.SignInLinkRedemption, error) {
	return h.service.Redeem(ctx, token, keysOn)
}

// SetPasswordWithSignInLink sets a password with a redeemed link's token.
func (h *SignInLinkHandler) SetPasswordWithSignInLink(ctx context.Context, token, newPassword string) error {
	return h.service.SetPassword(ctx, token, newPassword)
}
