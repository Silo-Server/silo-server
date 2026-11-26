package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// PendingAppKeyList is the account's device keys waiting for approval.
type PendingAppKeyList struct {
	Items []AppDeviceKey `json:"items" doc:"Device keys waiting for approval, oldest first, each with status pending; a key past its 24 hours is left out"`
}

// PendingAppKeyListOutput is the listPendingAppKeys response.
type PendingAppKeyListOutput struct {
	Body PendingAppKeyList
}

// PendingAppKeyDecisionInput names one waiting device key; the body is an
// empty JSON object.
type PendingAppKeyDecisionInput struct {
	ID      string `path:"id" format:"uuid" doc:"The waiting device key's id from listPendingAppKeys" example:"0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11"`
	RawBody []byte
	Body    struct{}
}

// mainProfileRule is how an operation that creates lasting access to the
// shared account is limited to the household's main profile
// (requireVerifiedMainProfile).
const mainProfileRule = " It creates lasting access to the shared account, so only the household's main profile may call it: X-Profile-Id must name the account's primary profile, with X-Profile-Token when that profile is PIN-protected (without it the request is 403 profile_verification_required); naming any other profile of the account is 403 main_profile_required, and an X-Profile-Id that names no profile of the account is 404 not_found. A request without X-Profile-Id is accepted only from an administrator account that has at most one profile, neither PIN-protected nor access-restricted; an administrator account with several profiles, or a PIN, names its main profile like any other account. Otherwise it is 403 main_profile_required. On this operation 403 permission_denied means the session itself may not do this, whatever profile it names. The operation checks the profile again once it holds the account: if the profile's PIN changed, it stopped being the main profile, or it was being written at that moment, the answer is 403 profile_verification_required with nothing changed, and the request is made again: an operation whose body carries a signed envelope (enrollAppKeys) needs a new challenge and a newly signed envelope, since the first challenge is spent; every other operation is sent again as it was."

// pendingSessionRule names the sessions the pending key operations refuse.
const pendingSessionRule = "Only the account's own login session: an API key, an administrator acting as the account, and a session opened by device sign-in or by another app's key are 403 permission_denied. A session opened before this server recorded how sessions open is refused the same way; signing in again clears that."

// mainProfileOnly declares an operation that creates lasting access to the
// shared account: profile-scoped, so the viewer gate verifies a PIN-locked
// profile's X-Profile-Token, with the header optional, so the handler decides
// a request that names no profile. Its handler calls
// requireVerifiedMainProfile.
func mainProfileOnly(op Operation) Operation {
	op.Class = ClassProfileScoped
	op.ProfileOptional = true
	return op
}

// mainProfileChecker answers the household main-profile rule
// (handlers.AuthHandler.ActsAsVerifiedMainProfile); the app key service and
// the device login service both carry it.
type mainProfileChecker interface {
	// SessionMayManageKeys refuses a session that may not do this whatever
	// profile it names.
	SessionMayManageKeys(context.Context, *auth.Claims) error
	ActsAsVerifiedMainProfile(ctx context.Context, userID int, activeProfileID string, verify func(*userstore.Profile) error) (bool, auth.ActingProfile, error)
}

// requireVerifiedMainProfile refuses a request that does not act as the
// account's household main profile, verified with its PIN when it has one.
// Every operation that creates lasting access to the account (enrolling or
// replacing app keys, approving a waiting device, approving a device sign-in
// in advance) calls it after its own session checks, and is registered with
// mainProfileOnly.
func (reg *Registry) requireVerifiedMainProfile(ctx context.Context, svc mainProfileChecker) (auth.ActingProfile, error) {
	none := auth.ActingProfile{}
	claims := claimsFrom(ctx)
	if !claims.IsOwnLoginSession() {
		return none, appKeyProblem(auth.NotOwnSessionError(claims))
	}
	// The session before the profile: one that may not do this is told so,
	// not sent to switch profile and prove a PIN for nothing.
	if err := svc.SessionMayManageKeys(ctx, claims); err != nil {
		return none, appKeyProblem(err)
	}
	acted, acting, err := svc.ActsAsVerifiedMainProfile(ctx, claims.UserID, profileFrom(ctx), verifyHouseholdProfile(ctx))
	if errors.Is(err, access.ErrProfileUnverified) {
		return none, NewProblem(TypeProfileVerificationRequired, "This change requires verifying the main profile's PIN.")
	}
	if err != nil {
		return none, serviceProblem(err)
	}
	if !acted {
		return none, mainProfileRequiredProblem()
	}
	return acting, nil
}

// mainProfileRequiredProblem answers a request that does not act as the
// household's main profile: it names another profile, or names none where
// one is needed. It has its own problem type so an app can tell "switch to
// the main profile" from permission_denied, which on these operations means
// the session itself may not do this and signing in again is the remedy.
func mainProfileRequiredProblem() *Problem {
	return NewProblem(TypeMainProfileRequired, "This change requires the household's main profile.")
}

// mainProfileChangedProblem answers auth.ErrActingProfileChanged: the main
// profile's PIN or standing changed between the check above and the
// operation's own check under the account lock.
func mainProfileChangedProblem() *Problem {
	return NewProblem(TypeProfileVerificationRequired, "The main profile changed while this request ran. Verify it again and repeat the request.")
}

func registerAppKeyPending(reg *Registry) {
	list := humaOp(http.MethodGet, Prefix+"/auth/keys/pending", "listPendingAppKeys", "app-keys",
		"List the signed-in account's devices waiting for approval.")
	list.Description = pendingSessionRule + " Only an administrator's account has waiting devices: a device a person key introduces to it waits for approval for 24 hours. Each item is a device object as listAppKeys returns it. Answers while auth.app_key_sign_in is off."
	list.Errors = []int{http.StatusForbidden}
	Register(reg, Operation{Operation: list, Class: ClassAuthenticated, ServiceBacked: true, Origins: grantedRefusedSignIn}, reg.listPendingAppKeys)

	approve := humaOp(http.MethodPost, Prefix+"/auth/keys/pending/{id}/approve", "approvePendingAppKey", "app-keys",
		"Approve a device waiting to sign in to the signed-in administrator account.")
	approve.Description = pendingSessionRule + " Send an empty JSON object. The key becomes active: the device's next signInWithAppKey, on a new challenge, opens a session if it passes the usual sign-in checks. The account must still be an enabled administrator (403 permission_denied otherwise). Approving an active key changes nothing; a key that is unknown, denied, revoked or past its 24 hours is 404 not_found. 409 capability_disabled while auth.app_key_sign_in is off." + mainProfileRule
	approve.DefaultStatus = http.StatusNoContent
	approve.Errors = []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict}
	Register(reg, withOrigins(mainProfileOnly(Operation{Operation: approve, RetrySafety: RetrySafetyNaturalIdempotent, ServiceBacked: true, Audited: true}), grantedRefusedSignIn), reg.approvePendingAppKey)

	deny := humaOp(http.MethodPost, Prefix+"/auth/keys/pending/{id}/deny", "denyPendingAppKey", "app-keys",
		"Refuse a device waiting to sign in to the signed-in account.")
	deny.Description = pendingSessionRule + " Send an empty JSON object. The device's next signInWithAppKey that reaches the key answers 403 key_denied (409 capability_disabled comes first while auth.app_key_sign_in is off), and neither listAppKeys nor listPendingAppKeys shows the key. Denying a denied key changes nothing; an active, revoked or unknown key is 404 not_found. Answers while auth.app_key_sign_in is off. Refused in demo mode for a caller who is not an administrator."
	deny.DefaultStatus = http.StatusNoContent
	deny.Errors = []int{http.StatusForbidden, http.StatusNotFound}
	Register(reg, Operation{Operation: deny, RetrySafety: RetrySafetyNaturalIdempotent, Class: ClassAuthenticated, ServiceBacked: true, Audited: true, DemoRestricted: true, Origins: grantedRefusedSignIn}, reg.denyPendingAppKey)
}

func (reg *Registry) listPendingAppKeys(ctx context.Context, _ *struct{}) (*PendingAppKeyListOutput, error) {
	svc, err := reg.appKeysWired()
	if err != nil {
		return nil, err
	}
	if !claimsFrom(ctx).IsOwnLoginSession() {
		return nil, appKeyProblem(auth.NotOwnSessionError(claimsFrom(ctx)))
	}
	keys, err := svc.ListPendingAppKeys(ctx, claimsFrom(ctx))
	if err != nil {
		return nil, appKeyProblem(err)
	}
	hide := reg.hideEnrolledAddresses(ctx)
	out := PendingAppKeyList{Items: make([]AppDeviceKey, 0, len(keys))}
	for _, key := range keys {
		// An introduced key never comes from a grant, so allowed_by stays null.
		device := appDeviceKeyOf(key, "")
		if hide {
			device.EnrolledIPAddress = ""
		}
		out.Items = append(out.Items, device)
	}
	return &PendingAppKeyListOutput{Body: out}, nil
}

func (reg *Registry) approvePendingAppKey(ctx context.Context, in *PendingAppKeyDecisionInput) (*struct{}, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, err := reg.appKeysOn(ctx)
	if err != nil {
		return nil, err
	}
	acting, err := reg.requireVerifiedMainProfile(ctx, svc)
	if err != nil {
		return nil, err
	}
	if err := svc.ApprovePendingAppKey(ctx, claimsFrom(ctx), in.ID, acting); err != nil {
		return nil, appKeyProblem(err)
	}
	return nil, nil
}

func (reg *Registry) denyPendingAppKey(ctx context.Context, in *PendingAppKeyDecisionInput) (*struct{}, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, err := reg.appKeysWired()
	if err != nil {
		return nil, err
	}
	if !claimsFrom(ctx).IsOwnLoginSession() {
		return nil, appKeyProblem(auth.NotOwnSessionError(claimsFrom(ctx)))
	}
	if err := svc.DenyPendingAppKey(ctx, claimsFrom(ctx), in.ID); err != nil {
		return nil, appKeyProblem(err)
	}
	return nil, nil
}
