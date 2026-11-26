package handlers

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/access"
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

// IssueSessionAppCode issues an app code for the caller's own account.
func (h *AuthHandler) IssueSessionAppCode(ctx context.Context, claims *auth.Claims, acting auth.ActingProfile) (auth.IssuedAppCode, error) {
	return h.appKeys.IssueSessionAppCode(ctx, claims, acting)
}

// RedeemAppCode enrolls the keys of a signed redeem with its app code.
func (h *AuthHandler) RedeemAppCode(ctx context.Context, req auth.KeyRedeemRequest) (*AppKeyAnswer, error) {
	result, err := h.appKeys.RedeemAppCode(ctx, req)
	return h.appKeyAnswer(ctx, result, err)
}

// AppKeySessionAccess answers what the caller's session may do with keys.
func (h *AuthHandler) AppKeySessionAccess(ctx context.Context, claims *auth.Claims) (auth.SessionKeyAccess, error) {
	return h.appKeys.SessionKeyAccess(ctx, claims)
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

// SessionMayReachOtherDevices reports whether the caller's session may see
// or change what the account's other devices hold.
func (h *AuthHandler) SessionMayReachOtherDevices(ctx context.Context, claims *auth.Claims) error {
	return h.appKeys.SessionMayReachOtherDevices(ctx, claims)
}

// ReplacePersonKey replaces the caller's person key.
func (h *AuthHandler) ReplacePersonKey(ctx context.Context, claims *auth.Claims, key appkeys.PublicKey, acting auth.ActingProfile) error {
	return h.appKeys.ReplacePersonKey(ctx, claims, key, acting)
}

// ListPendingAppKeys lists the caller's device keys waiting for approval.
func (h *AuthHandler) ListPendingAppKeys(ctx context.Context, claims *auth.Claims) ([]*models.AppDeviceKey, error) {
	return h.appKeys.ListPendingKeys(ctx, claims)
}

// ApprovePendingAppKey approves one of the caller's waiting device keys.
func (h *AuthHandler) ApprovePendingAppKey(ctx context.Context, claims *auth.Claims, id string, acting auth.ActingProfile) error {
	return h.appKeys.ApprovePendingKey(ctx, claims, id, acting)
}

// DenyPendingAppKey refuses one of the caller's waiting device keys.
func (h *AuthHandler) DenyPendingAppKey(ctx context.Context, claims *auth.Claims, id string) error {
	return h.appKeys.DenyPendingKey(ctx, claims, id)
}

// SetHouseholdStores wires the profile stores ActsAsVerifiedMainProfile
// reads. Without them it refuses every caller. outsidePostgres is set when
// the profile store is not the Postgres one: a key transaction can't read
// those profiles, so the acting profile isn't checked again there.
func (h *AuthHandler) SetHouseholdStores(stores userstore.UserStoreProvider, outsidePostgres bool) {
	h.householdStores = stores
	h.profilesOutsidePostgres = outsidePostgres
}

// ActsAsVerifiedMainProfile reports whether the request acts as the
// account's household main profile, verified with its PIN when it has one.
// It is the rule for every operation that creates lasting access to the
// shared account (enrolling or replacing app keys, approving a waiting
// device), and it is the household manager rule that disconnecting a sign-in
// and creating a personal API key already apply (householdManagerAs): a
// named profile must be the primary one and pass verify; a request that names
// none passes only from an administrator account that has at most one
// profile, with no PIN and no maturity or library limit: there the request
// can only mean that profile. An administrator's account with several
// profiles, or a PIN, names the main profile like anyone else's. verify
// returns access.ErrProfileUnverified for a PIN-locked main profile the
// request did not verify.
//
// It also returns the profile the request acted as, for the operation to
// check again under the account lock (auth.ActingProfile): the zero value
// when the request named none.
func (h *AuthHandler) ActsAsVerifiedMainProfile(ctx context.Context, userID int, activeProfileID string, verify func(*userstore.Profile) error) (bool, auth.ActingProfile, error) {
	none := auth.ActingProfile{}
	if h.householdStores == nil {
		return false, none, nil
	}
	store, err := h.householdStores.ForUser(ctx, userID)
	if err != nil {
		return false, none, fmt.Errorf("opening user store: %w", err)
	}
	if store == nil {
		return false, none, nil
	}
	if verify == nil {
		verify = func(*userstore.Profile) error { return access.ErrProfileUnverified }
	}
	allowed, active, err := householdManagerAs(ctx, store, activeProfileID, verify)
	if err != nil || !allowed {
		return false, none, err
	}
	if active == nil {
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			return false, none, fmt.Errorf("listing household profiles: %w", err)
		}
		return len(profiles) <= 1, none, nil
	}
	if h.profilesOutsidePostgres {
		return true, none, nil
	}
	return true, auth.ActingProfile{ID: active.ID, PINRevision: active.PINRevision}, nil
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

// SessionMayManageKeys refuses a login session that may not manage the
// account's keys or read or send its profile set. Without app key sign-in
// wired it refuses nothing: approving a device sign-in in advance, which
// also asks, checks the session again in its own transaction.
func (h *AuthHandler) SessionMayManageKeys(ctx context.Context, claims *auth.Claims) error {
	if h.appKeys == nil {
		return nil
	}
	return h.appKeys.SessionMayManageKeys(ctx, claims)
}
