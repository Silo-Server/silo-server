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

// SignInLinkCapabilities reports whether the server can make a link now,
// and email one.
func (h *SignInLinkHandler) SignInLinkCapabilities(ctx context.Context) (signinlinks.Capabilities, error) {
	return h.service.Capabilities(ctx)
}

// IssueSignInLink makes a sign-in link for an account. A scoped API key
// never makes one: no scope names the route, so the scope gate refuses it
// before this runs. Otherwise the rule is the one for setting that
// account's password as an administrator: only the Owner for another
// administrator, nobody but the Owner for the Owner, and only the Owner for
// an account of its own whose password sign-in is off. The service applies
// the rules with the accounts locked; this renders their refusals as an
// account update does. The scoped-key branch below is a guard should a scope
// ever name the route.
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
