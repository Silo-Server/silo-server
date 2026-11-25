package handlers

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
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

// RedeemAppKeyGrant enrolls another app's key with a grant the person key
// signed.
func (h *AuthHandler) RedeemAppKeyGrant(ctx context.Context, req auth.KeyRequest) (*AppKeyAnswer, error) {
	result, err := h.appKeys.RedeemGrant(ctx, req)
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

// SessionMayIssueCredential reports whether the caller's session may be
// handed a personal API key or a linked sign-in.
func (h *AuthHandler) SessionMayIssueCredential(ctx context.Context, claims *auth.Claims) error {
	return h.appKeys.SessionMayIssueCredential(ctx, claims)
}

// ReplacePersonKey replaces the caller's person key.
func (h *AuthHandler) ReplacePersonKey(ctx context.Context, claims *auth.Claims, key appkeys.PublicKey) error {
	return h.appKeys.ReplacePersonKey(ctx, claims, key)
}

// ListPendingAppKeys lists the caller's device keys waiting for approval.
func (h *AuthHandler) ListPendingAppKeys(ctx context.Context, claims *auth.Claims) ([]*models.AppDeviceKey, error) {
	return h.appKeys.ListPendingKeys(ctx, claims)
}

// ApprovePendingAppKey approves one of the caller's waiting device keys.
func (h *AuthHandler) ApprovePendingAppKey(ctx context.Context, claims *auth.Claims, id string) error {
	return h.appKeys.ApprovePendingKey(ctx, claims, id)
}

// DenyPendingAppKey refuses one of the caller's waiting device keys.
func (h *AuthHandler) DenyPendingAppKey(ctx context.Context, claims *auth.Claims, id string) error {
	return h.appKeys.DenyPendingKey(ctx, claims, id)
}

// SetHouseholdStores wires the profile stores ActsAsVerifiedMainProfile
// reads. Without them it refuses every caller.
func (h *AuthHandler) SetHouseholdStores(stores userstore.UserStoreProvider) {
	h.householdStores = stores
}

// ActsAsVerifiedMainProfile reports whether the request acts as the
// account's household main profile, verified with its PIN when it has one.
// It is the rule for every operation that creates lasting access to the
// shared account (enrolling or replacing app keys, approving a waiting
// device), and it is the household manager rule that disconnecting a sign-in
// and creating a personal API key already apply (canManageHouseholdAs): a
// named profile must be the primary one and pass verify; a request that names
// none passes only from an administrator account that has at most one
// profile, with no PIN and no maturity or library limit: there the request
// can only mean that profile. An administrator's account with several
// profiles, or a PIN, names the main profile like anyone else's. verify
// returns access.ErrProfileUnverified for a PIN-locked main profile the
// request did not verify.
func (h *AuthHandler) ActsAsVerifiedMainProfile(ctx context.Context, userID int, activeProfileID string, verify func(*userstore.Profile) error) (bool, error) {
	allowed, err := mayManageHouseholdOf(ctx, h.householdStores, userID, activeProfileID, verify)
	if err != nil || !allowed || activeProfileID != "" {
		return allowed, err
	}
	store, err := h.householdStores.ForUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("opening user store: %w", err)
	}
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		return false, fmt.Errorf("listing household profiles: %w", err)
	}
	return len(profiles) <= 1, nil
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
