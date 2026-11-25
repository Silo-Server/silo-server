package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// AppKeyAnswer is an app key sign-in's answer before it is sealed: the
// service's result and, once a session opened, its tokens as the login
// response carries them.
type AppKeyAnswer struct {
	Result *auth.KeySignInResult
	Tokens *TokenPairView
}

// SetAppKeySignIn wires app key sign-in. Without it the app key operations
// report not_configured.
func (h *AuthHandler) SetAppKeySignIn(keys *auth.AppKeySignIn) { h.appKeys = keys }

// AppKeySignInConfigured reports whether app key sign-in is wired.
func (h *AuthHandler) AppKeySignInConfigured() bool { return h != nil && h.appKeys != nil }

// AppKeySignInEnabled reads the auth.app_key_sign_in switch.
func (h *AuthHandler) AppKeySignInEnabled(ctx context.Context) (bool, error) {
	return h.appKeys.AppKeySignInEnabled(ctx)
}

// IssueKeyChallenge stores a new challenge; see auth.AppKeySignIn.
func (h *AuthHandler) IssueKeyChallenge(ctx context.Context) (string, int64, error) {
	return h.appKeys.IssueChallenge(ctx)
}

// IntroduceAppKey enrolls a device key introduced by the person key.
func (h *AuthHandler) IntroduceAppKey(ctx context.Context, req auth.KeyRequest) (*AppKeyAnswer, error) {
	result, err := h.appKeys.Introduce(ctx, req)
	return h.appKeyAnswer(ctx, result, err)
}

// SignInWithAppKey opens a session with an enrolled device key.
func (h *AuthHandler) SignInWithAppKey(ctx context.Context, req auth.KeyRequest) (*AppKeyAnswer, error) {
	result, err := h.appKeys.SignIn(ctx, req)
	return h.appKeyAnswer(ctx, result, err)
}

// EnrollAppKeys enrolls a person key and a device key from a login session.
func (h *AuthHandler) EnrollAppKeys(ctx context.Context, req auth.KeyEnrollRequest) (*AppKeyAnswer, error) {
	result, err := h.appKeys.Enroll(ctx, req)
	return h.appKeyAnswer(ctx, result, err)
}

// ListOwnAppKeys lists the caller's own keys.
func (h *AuthHandler) ListOwnAppKeys(ctx context.Context, claims *auth.Claims) (auth.OwnAppKeys, error) {
	return h.appKeys.ListOwnKeys(ctx, claims)
}

// RevokeOwnDeviceKey revokes one of the caller's device keys.
func (h *AuthHandler) RevokeOwnDeviceKey(ctx context.Context, claims *auth.Claims, id string) error {
	return h.appKeys.RevokeOwnDeviceKey(ctx, claims, id)
}

// ReplacePersonKey replaces the caller's person key.
func (h *AuthHandler) ReplacePersonKey(ctx context.Context, claims *auth.Claims, key appkeys.PublicKey) error {
	return h.appKeys.ReplacePersonKey(ctx, claims, key)
}

// appKeyAnswer adds the token view a signed-in result carries. Errors pass
// through unchanged: the v2 operations map the auth sentinels themselves.
func (h *AuthHandler) appKeyAnswer(ctx context.Context, result *auth.KeySignInResult, err error) (*AppKeyAnswer, error) {
	if err != nil {
		return nil, err
	}
	answer := &AppKeyAnswer{Result: result}
	if result.Pair != nil && result.User != nil {
		answer.Tokens = &TokenPairView{
			AccessToken:  result.Pair.AccessToken,
			RefreshToken: result.Pair.RefreshToken,
			ExpiresIn:    result.Pair.ExpiresIn,
			User:         buildUserResponse(result.User, effectiveAccountPolicy(ctx, result.User, h.accessGroups), nil, nil),
		}
	}
	return answer, nil
}

// VerifyProfileSet verifies a signed profile set; see auth.AppKeySignIn.
func (h *AuthHandler) VerifyProfileSet(ctx context.Context, req auth.KeyRequest) (*auth.ProfileSetAuthority, error) {
	return h.appKeys.VerifyProfileSet(ctx, req)
}

// ProfileSetSessionAllowed refuses a login session that may not read or
// send the account's profile set.
func (h *AuthHandler) ProfileSetSessionAllowed(ctx context.Context, claims *auth.Claims) error {
	return h.appKeys.ProfileSetSessionAllowed(ctx, claims)
}
