package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	providerModeOAuth = "oauth"
	fieldInviteCode   = "invite_code"
)

// The seams v1 and v2 share for providers, token refresh, session listing,
// first-run setup, and invited signup. Each returns an *APIError on failure
// so both transports render the same decision.

// ListProviders lists the login providers a client may offer, omitting OAuth
// providers when the OAuth routes are not served. v1 GET /auth/providers and
// v2 listAuthProviders both call it.
func (h *AuthHandler) ListProviders() []auth.LoginProviderInfo {
	providers := h.service.ListProviders()
	out := make([]auth.LoginProviderInfo, 0, len(providers))
	for _, provider := range providers {
		if provider.Mode == providerModeOAuth && !h.oauthRoutesAvailable {
			continue
		}
		out = append(out, provider)
	}
	return out
}

// RefreshedTokensView is a refreshed credential: the token pair without the
// account, which the client already holds.
type RefreshedTokensView struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// Refresh exchanges a refresh token for a new pair. v1 POST /auth/refresh and
// v2 refreshSession both call it. A revoked session is 401 session_revoked;
// any other failure is 401 invalid_token, as on v1.
func (h *AuthHandler) Refresh(ctx context.Context, refreshToken string) (RefreshedTokensView, error) {
	if refreshToken == "" {
		return RefreshedTokensView{}, &APIError{Status: http.StatusBadRequest, Code: policyErrorBadRequest, Message: "Refresh token is required", Field: "refresh_token"}
	}
	pair, err := h.service.Refresh(ctx, refreshToken)
	if err != nil {
		if errors.Is(err, auth.ErrSessionRevoked) {
			return RefreshedTokensView{}, apiError(http.StatusUnauthorized, "session_revoked", "Session has been revoked")
		}
		return RefreshedTokensView{}, apiError(http.StatusUnauthorized, "invalid_token", "Invalid or expired refresh token")
	}
	return RefreshedTokensView{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, ExpiresIn: pair.ExpiresIn}, nil
}

// ListSessions lists the caller's login sessions. v1 GET /auth/sessions and
// v2 listSessions both call it.
func (h *AuthHandler) ListSessions(ctx context.Context, userID int) ([]*models.AuthSession, error) {
	sessions, err := h.service.GetSessions(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
	}
	return sessions, nil
}

// RevokeSession revokes one of the caller's sessions. v1 DELETE
// /auth/sessions/{id} and v2 deleteSession both call it.
func (h *AuthHandler) RevokeSession(ctx context.Context, sessionID string, userID int) error {
	if sessionID == "" {
		return apiError(http.StatusBadRequest, "bad_request", "Session ID is required")
	}
	if err := h.service.RevokeSession(ctx, sessionID, userID); err != nil {
		if auth.IsSessionNotFound(err) {
			return apiError(http.StatusNotFound, "not_found", "Session not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
	}
	return nil
}

// RegistrationInput is a first-run setup or invited signup as the transport
// received it. InviteCode is only read by Signup.
type RegistrationInput struct {
	Username             string
	Email                string
	Password             string
	InviteCode           string
	CreateDefaultProfile bool
	DefaultProfileName   string
	DeviceName           string
	IP                   string
}

// SetupInitialUser creates the first administrator and opens its session.
// v1 POST /auth/setup and v2 setupServer both call it.
func (h *AuthHandler) SetupInitialUser(ctx context.Context, in RegistrationInput) (TokenPairView, error) {
	in.Username = auth.NormalizeUsername(in.Username)
	in.Email = auth.NormalizeEmail(in.Email)
	if in.Username == "" || in.Email == "" || in.Password == "" {
		return TokenPairView{}, apiError(http.StatusBadRequest, "bad_request", "Username, email, and password are required")
	}
	pair, user, err := h.service.SetupInitialUser(ctx, in.Username, in.Email, in.Password, in.CreateDefaultProfile, in.DefaultProfileName, in.DeviceName, in.IP)
	if err != nil {
		if errors.Is(err, auth.ErrSetupAlreadyComplete) {
			return TokenPairView{}, apiError(http.StatusUnauthorized, "setup_complete", "Initial setup has already been completed")
		}
		return TokenPairView{}, apiError(http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
	}
	return h.tokenPairView(ctx, pair, user), nil
}

// SignupEnabled reports whether public invited signup is on. v1 GET
// /auth/signup and v2 getSignupStatus both call it.
func (h *AuthHandler) SignupEnabled(ctx context.Context) (bool, error) {
	enabled, err := h.service.IsSignupEnabled(ctx)
	if err != nil {
		return false, apiError(http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
	}
	return enabled, nil
}

// Signup creates an account from an invite code and opens its session. v1
// POST /auth/signup and v2 signup both call it. Invite-code failures carry
// Field so v2 can render them at the member.
func (h *AuthHandler) Signup(ctx context.Context, in RegistrationInput) (TokenPairView, error) {
	in.Username = auth.NormalizeUsername(in.Username)
	in.Email = auth.NormalizeEmail(in.Email)
	if in.Username == "" || in.Email == "" || in.Password == "" || in.InviteCode == "" {
		return TokenPairView{}, apiError(http.StatusBadRequest, "bad_request", "Username, email, password, and invite code are required")
	}
	pair, user, err := h.service.Signup(ctx, in.Username, in.Email, in.Password, in.InviteCode, in.CreateDefaultProfile, in.DefaultProfileName, in.DeviceName, in.IP)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrSignupDisabled):
			return TokenPairView{}, apiError(http.StatusForbidden, "signup_disabled", "Public signups are not currently enabled")
		case errors.Is(err, auth.ErrInviteCodeNotFound):
			return TokenPairView{}, &APIError{Status: http.StatusBadRequest, Code: "invalid_code", Message: "Invalid invite code", Field: fieldInviteCode}
		case errors.Is(err, auth.ErrInviteCodeExhausted):
			return TokenPairView{}, &APIError{Status: http.StatusBadRequest, Code: "code_exhausted", Message: "This invite code has reached its maximum uses", Field: fieldInviteCode}
		case errors.Is(err, auth.ErrInviteCodeDisabled):
			return TokenPairView{}, &APIError{Status: http.StatusBadRequest, Code: "code_disabled", Message: "This invite code is no longer active", Field: fieldInviteCode}
		case auth.IsDuplicate(err):
			return TokenPairView{}, apiError(http.StatusBadRequest, "duplicate", "Username or email already taken")
		}
		return TokenPairView{}, apiError(http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
	}
	return h.tokenPairView(ctx, pair, user), nil
}

func (h *AuthHandler) tokenPairView(ctx context.Context, pair *auth.TokenPair, user *models.User) TokenPairView {
	return TokenPairView{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		User:         buildUserResponse(user, effectiveDownloadAllowed(ctx, user, h.accessGroups), nil, nil),
	}
}

// PluginLaunchTTL is the lifetime of the plugin access cookie.
const PluginLaunchTTL = 5 * time.Minute

// PluginLaunchToken mints the short-lived plugin access token the launch
// cookie carries. v1 POST /auth/plugin-launch and v2 launchPlugin both call
// it; the caller sets the cookie on its own path.
func (h *AuthHandler) PluginLaunchToken(claims *auth.Claims, profileID string) (string, error) {
	if claims == nil || claims.SessionID == "" {
		return "", apiError(http.StatusUnauthorized, "unauthorized", "Invalid or missing authentication token")
	}
	token, err := h.jwt.GeneratePluginAccessToken(claims.UserID, claims.Role, claims.SessionID, strings.TrimSpace(profileID), PluginLaunchTTL)
	if err != nil {
		return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to prepare plugin access")
	}
	return token, nil
}

// PluginAccessCookieName is the cookie the plugin launch issues.
const PluginAccessCookieName = auth.PluginAccessCookieName
