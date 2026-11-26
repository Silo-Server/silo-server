package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/invitations"
)

// AppCode is an app code as a server's web page hands it to the Silo app.
type AppCode struct {
	Code          string `json:"code" doc:"The app code: base64url of 32 random bytes. Good for one redeemAppCode within expires_in seconds" example:"8bqRFq-gVv5Dxjb61BaVfnDB3-F24muoSMMiatX49VA"`
	ExpiresIn     int    `json:"expires_in" doc:"Seconds the code stays good: ten minutes, or less when the invitation expires sooner" example:"600"`
	ServerAddress string `json:"server_address" doc:"The server address the app connects to, which a page shows with the code or puts in the link: server.public_url when the request arrived on that address (so a path prefix survives), otherwise the address the request arrived on" example:"https://media.example.com"`
	LinkAllowed   bool   `json:"link_allowed" doc:"Whether a page may put this code in a link (the join link). True only for a join code whose redeem creates an account with no administrative power. When false, show the server address and the code as text to type in the app, and never build a link, a scannable link or anything a device could open from them: a link can be received by an app other than Silo, which would redeem the code first. Always false for createAppCode" example:"true"`
}

// AppCodeOutput is the response of both operations that issue an app code.
// Like every credential response it is Cache-Control: no-store.
type AppCodeOutput struct {
	Body AppCode
}

// InvitationAppCodeInput starts a join through the Silo app.
type InvitationAppCodeInput struct {
	RawBody []byte
	Body    struct {
		Token    string `json:"token" minLength:"1" maxLength:"128" doc:"The token from the invitation link's fragment"`
		Username string `json:"username,omitempty" maxLength:"254" doc:"The account name the person chose. Required when the lookup reports email_required; an emailed invitation's account takes its address instead, and this is ignored"`
		Email    string `json:"email,omitempty" maxLength:"254" doc:"An optional email address for an invitation with no address of its own; ignored for an emailed invitation"`
	}
}

// SessionAppCodeInput is the createAppCode request: an empty JSON object.
type SessionAppCodeInput struct {
	RawBody []byte
	Body    struct{}
}

// RedeemAppCodeInput is the body of redeemAppCode: the person key being
// enrolled beside a redeem envelope it signed.
type RedeemAppCodeInput struct {
	SignInDeviceHeaders
	RawBody []byte
	Body    struct {
		PersonKey string         `json:"person_key" minLength:"87" maxLength:"87" doc:"The person key being enrolled: base64url of its 65-byte uncompressed P-256 point" example:"BIXPevG2yxPrIkwoRlSuZ0c0DldsVX_3OET3S0YeP0uDN7Lj-SQMccAMdvNimX-OvE62RqjlO_Fll-F9MU_kw5Q"`
		Message   AppKeyEnvelope `json:"message" doc:"A redeem envelope signed with the private half of person_key, with device_sig; the app code is in its payload"`
	}
}

// AppKeySessionAccess is what the caller's login session may do with the
// account's app keys.
type AppKeySessionAccess struct {
	List        bool `json:"list" doc:"Whether this session may list the account's app keys and waiting devices (listAppKeys, listPendingAppKeys) and deny a waiting device" example:"true"`
	Manage      bool `json:"manage" doc:"Whether this session may enroll and replace keys, issue an app code (createAppCode) and make the account's other changes that lead to lasting access; the main-profile rule still applies to each" example:"true"`
	Approve     bool `json:"approve" doc:"Whether this session may approve a waiting device: manage, and the request acts as the household's verified main profile" example:"false"`
	SignInAgain bool `json:"sign_in_again" doc:"Whether signing in again in this browser would give a session that may: true for a session opened by device sign-in, or before this server recorded how sessions open, when the account has a local password the server accepts or a sign-in provider account. False for an administrator acting as the account, an API key, a remote-playback session and a session another app opened with a grant" example:"false"`
}

// AppKeySessionAccessOutput is the getAppKeySessionAccess response.
type AppKeySessionAccessOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         AppKeySessionAccess
}

// redeemSealedReply is redeemAppCode's sealed answer: the signed_in
// plaintext with the account's name and person key.
var redeemSealedReply = personSealedReply("username, person_key, ",
	"; username is the account's name, and person_key one of: enrolled (the request's person key is the account's person key, now or already), kept (the account kept a different person key, named by person_key_id; the request's key is not stored) or replaced (a sign-in link's code brought a different key, and the account's old person key was revoked). A client tells whether the account holds the key it sent by comparing person_key_id with the ID of that key, not by person_key. person_key says how the account came to hold its key (enrolled, kept, replaced); a client that does not know a value still has its answer from the ID")

// redeemPayloadLocation is where redeem's signed payload sits in its body.
const redeemPayloadLocation = locationBody + ".message.payload"

// appCodeLifeNote is the life of a code; the invitation's version adds its
// bound.
const (
	appCodeLifeNote           = " The code lives ten minutes and works once. The response is no-store."
	invitationAppCodeLifeNote = " The code lives ten minutes, never past the invitation's expiry, and works once. The response is no-store."
)

// serverAddressNote says what server_address is for.
const serverAddressNote = " server_address is where the app should connect, not proof of which server answers there: the redeem's signed server_id and the origin rule are."

func registerAppCodes(reg *Registry) {
	invite := humaOp(http.MethodPost, Prefix+"/invitations/app-code", "createInvitationAppCode", "invitations",
		"Start joining through the Silo app with an invitation link.")
	invite.Description = "The invite page calls this when the person chooses to join with the Silo app. It checks the invitation as acceptInvitationLink does, reserves the account name and answers an app code for the app to redeem (redeemAppCode); the invitation stays open, and nothing is created until the app redeems the code. A second code from the same invitation replaces the first, which stops working. An emailed invitation's account takes its address as name and email; for an invitation with no address (email_required in the lookup) username is required and email optional. An unknown, used, revoked or expired invitation is 404 not_found. A name or email another account holds, or another unspent code reserves, is 409 conflict with errors[0] {code: duplicate, location: body.username or body.email}, naming the field that clashed. A missing username is 422 at body.username, and so is a username containing @ that isn't the email the same request gives (it could be someone else's address); an invalid email is 422 at body.email. An invitation that creates a profile on a server whose profile store can't create it in the same transaction is 501 capability_unsupported, as accept answers (acceptance_available and app_join are false in the lookup). With local password sign-in off on this server the answer is 409 provider_sign_in_required: an invitation never creates an account whose only way in is its app keys there; the person signs in with the server's provider on its site instead and uses createAppCode. 409 capability_disabled while auth.app_key_sign_in is off. Spends the per-address invitation rate-limit budget, which the lookup shares: 20 a minute with a burst of 10 by default, and each code and each renewal spends one." + invitationAppCodeLifeNote + serverAddressNote
	invite.Errors = []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	Register(reg, Operation{Operation: invite, RetrySafety: RetrySafetyNonRetryable, Class: ClassPublic, ServiceBacked: true,
		RateLimitBucket: "invitation", MaxBodyBytes: 4 << 10}, reg.createInvitationAppCode)

	session := humaOp(http.MethodPost, Prefix+"/auth/app-codes", "createAppCode", "app-keys",
		"Issue an app code that lets the Silo app enroll its keys on the signed-in account.")
	session.Description = "Send an empty JSON object. The Open in Silo action on a server's site: the app redeems the code (redeemAppCode) to enroll a person key and its device key on this account, which take this session's provider identity. A code whose session has ended by then no longer works. Only the account's own login session issues one: an API key, an administrator acting as the account, a remote-playback session, and a session opened by device sign-in or by another app's key are 403 permission_denied. A session opened before this server recorded how sessions open is refused the same way; signing in again clears that (getAppKeySessionAccess says whether it would). Works with local password sign-in off from a session the server's sign-in provider opened; that is how a person gets the app on such a server. The redeem doesn't check the main profile's PIN again: the code was issued to the verified main profile and ends with the session that issued it. 409 capability_disabled while auth.app_key_sign_in is off." + appCodeLifeNote + serverAddressNote + mainProfileRule
	session.Errors = []int{http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests}
	sessionOp := mainProfileOnly(Operation{Operation: session, RetrySafety: RetrySafetyNonRetryable, ServiceBacked: true, Audited: true,
		RateLimitBucket: bucketKeySignIn, MaxBodyBytes: 1 << 10})
	Register(reg, withOrigins(sessionOp, grantedRefusedSignIn), reg.createAppCode)

	redeem := humaOp(http.MethodPost, Prefix+"/auth/app-codes/redeem", "redeemAppCode", "app-keys",
		"Enroll a person key and a device key with an app code and open the device's login session.")
	redeem.Description = "message is a redeem envelope signed with the private half of person_key (its kid must be person_key's ID), with device_sig; the app code is in its payload. A join code creates the invited account, with no password and the invitation's access, enrolls both keys and marks the invitation accepted, in one transaction. A session code enrolls the device key on the account that issued it, with that session's provider identity: person_key is enrolled when the account has none. When the account already has a different person key, the redeem keeps it: the device key is enrolled under the account's person key, nothing is revoked, devices waiting for approval stay waiting, and person_key is not stored (it only proves the device signed the envelope). A person who has lost every other device then replaces the account's person key from the new session with replacePersonKey as the main profile. A sign-in link's code (redeemSignInLink) enrolls a new or replacing person key and its device unbound to any provider (the account's own person key sent again keeps its provider binding, and its device takes it), and its person_key replaces a different one the account holds (person_key replaced), which is revoked with the devices it introduced that wait for approval; devices already enrolled keep their keys. Keys from a code start active, also on an administrator's account from a session or join code; a sign-in link's code is refused for an administrator's account. 404 app_code_invalid: the code is unknown, used or expired, its invitation was revoked, replaced or accepted since, the session that issued it ended, or it is a sign-in link's code and the account is an administrator's. 409 conflict has two causes, told apart by errors[0].code: duplicate (at body.message.payload) means the reserved name or email was taken since, and the person gets a new code from the page with another name; key_enrolled means a public key is already enrolled on this server, at body.person_key for the person key or body.message.payload for the device key, and the app makes that key again and redeems the same code on a new challenge. Every refusal after the code is looked up rolls the code back, so it stays good for its ten minutes; the challenge is spent once the signature verifies. 409 provider_sign_in_required: a join code while local password sign-in is off. A malformed payload field is 422 at body.message.payload. Spends the per-address key_sign_in rate-limit budget." + appKeySignedBodyNote + redeemSealedReply + providerAnswersDescription
	redeem.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	redeemOp := Operation{Operation: redeem, RetrySafety: RetrySafetyNonRetryable, Class: ClassPublic, ServiceBacked: true, Audited: true,
		RateLimitBucket: bucketKeySignIn, MaxBodyBytes: 16 << 10}
	Register(reg, redeemOp, reg.redeemAppCode)

	sessionAccess := humaOp(http.MethodGet, Prefix+"/auth/keys/session", "getAppKeySessionAccess", "app-keys",
		"Describe what the caller's login session may do with the account's app keys.")
	sessionAccess.Description = "So a page can tell, before it asks, whether this session may list or manage keys, and whether signing in again would help. Answers while auth.app_key_sign_in is off; read getAppKeyCapability for the switch. approve follows the X-Profile-Id the request declares."
	Register(reg, Operation{Operation: sessionAccess, Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true, Origins: grantedOwnSession}, reg.getAppKeySessionAccess)

	declareRetryAfter(reg, "Seconds to wait before asking again.", sessionOp)
	declareRetryAfter(reg, "Seconds to wait before asking again, on a fresh challenge.", redeemOp)
}

func (reg *Registry) createInvitationAppCode(ctx context.Context, in *InvitationAppCodeInput) (*AppCodeOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if _, err := reg.appKeysOn(ctx); err != nil {
		return nil, err
	}
	svc, p := reg.invitationService()
	if p != nil {
		return nil, p
	}
	issued, err := svc.IssueAppCode(ctx, invitations.AppCodeRequest{Token: in.Body.Token, Username: in.Body.Username, Email: in.Body.Email})
	if err != nil {
		return nil, appCodeIssueProblem(err)
	}
	return reg.appCodeAnswer(ctx, issued)
}

func (reg *Registry) createAppCode(ctx context.Context, in *SessionAppCodeInput) (*AppCodeOutput, error) {
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
	issued, err := svc.IssueSessionAppCode(ctx, claimsFrom(ctx), acting)
	if err != nil {
		return nil, appKeyProblem(err)
	}
	return reg.appCodeAnswer(ctx, issued)
}

func (reg *Registry) redeemAppCode(ctx context.Context, in *RedeemAppCodeInput) (*SealedReplyOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, err := reg.appKeysOn(ctx)
	if err != nil {
		return nil, err
	}
	personKey, env, err := appkeys.ParseKeyBody(in.RawBody, appkeys.PurposeRedeem)
	if err != nil {
		return nil, appKeyProblemAt(err, redeemPayloadLocation)
	}
	req, err := reg.keyRequest(ctx, env)
	if err != nil {
		return nil, err
	}
	answer, err := svc.RedeemAppCode(withClientDevice(ctx), auth.KeyRedeemRequest{KeyRequest: req, PersonKey: personKey})
	if errors.Is(err, auth.ErrAppKeysOff) {
		// The switch went off after appKeysOn admitted the request: the
		// same refusal.
		return nil, CapabilityProblem(StateDisabled, appKeyDomain)
	}
	if p := appCodeConflict(err); p != nil {
		return nil, p
	}
	if err != nil {
		return nil, appKeyProblemAt(err, redeemPayloadLocation)
	}
	return reg.sealAppKeyAnswer(answer)
}

func (reg *Registry) getAppKeySessionAccess(ctx context.Context, _ *struct{}) (*AppKeySessionAccessOutput, error) {
	svc, err := reg.appKeysWired()
	if err != nil {
		return nil, err
	}
	claims := claimsFrom(ctx)
	got, err := svc.AppKeySessionAccess(ctx, claims)
	if err != nil {
		return nil, appKeyProblem(err)
	}
	out := AppKeySessionAccess{List: got.List, Manage: got.Manage, SignInAgain: got.SignInAgain}
	if got.Manage {
		acted, _, err := svc.ActsAsVerifiedMainProfile(ctx, claims.UserID, profileFrom(ctx), verifyHouseholdProfile(ctx))
		if err != nil && !errors.Is(err, access.ErrProfileUnverified) {
			return nil, serviceProblem(err)
		}
		out.Approve = acted && err == nil
	}
	return &AppKeySessionAccessOutput{CacheControl: cachePrivateNoCache, Body: out}, nil
}

// appCodeAnswer is the answer of both issuing operations.
func (reg *Registry) appCodeAnswer(ctx context.Context, issued auth.IssuedAppCode) (*AppCodeOutput, error) {
	address, ok := reg.appCodeServerAddress(ctx)
	if !ok {
		return nil, NewProblem(TypeInternalError, "This server could not work out the address it was reached on.")
	}
	return &AppCodeOutput{Body: AppCode{Code: issued.Code, ExpiresIn: issued.ExpiresIn(time.Now()), ServerAddress: address, LinkAllowed: issued.LinkAllowed}}, nil
}

// appCodeServerAddress is the server address a page puts in the link that
// carries an app code: server.public_url in its canonical server address
// form when the request arrived on that address's origin, so a path prefix a
// reverse proxy adds survives, and the request's own origin otherwise. ok is
// false when neither canonicalizes.
func (reg *Registry) appCodeServerAddress(ctx context.Context) (string, bool) {
	r := requestFrom(ctx)
	if r == nil {
		return "", false
	}
	arrived, err := appkeys.CanonicalOrigin(clientip.RequestOrigin(r))
	if err != nil {
		return "", false
	}
	if connections := reg.deps.ServerConnections; connections.PublicURL != nil {
		configured := strings.TrimSpace(connections.PublicURL())
		if origin, err := appkeys.CanonicalOrigin(configured); err == nil && origin == arrived {
			if address, err := appkeys.CanonicalServerAddress(strings.TrimRight(configured, "/")); err == nil {
				return address, true
			}
		}
	}
	return arrived, true
}

// appCodeIssueProblem maps a refusal of createInvitationAppCode.
func appCodeIssueProblem(err error) error {
	switch {
	case errors.Is(err, invitations.ErrUsernameRequired):
		return validationProblem(locationBody+".username", codeRequired, "Choose a username.")
	case errors.Is(err, invitations.ErrInvalidEmail):
		return validationProblem(locationBody+".email", codeInvalid, "Enter a valid email address, like name@example.com.")
	case errors.Is(err, invitations.ErrUsernameIsAddress):
		return validationProblem(locationBody+".username", codeInvalid, "A username with @ must be the email address you give.")
	case errors.Is(err, auth.ErrAppCodeNameTaken):
		return appCodeConflict(err)
	case errors.Is(err, auth.ErrJoinNotAvailable):
		return NewProblem(TypeProviderSignInRequired, joinNotAvailableDetail)
	}
	return invitationProblem(err, true)
}

// appCodeConflict answers the two 409 conflict causes of the app code
// routes so an app tells them apart by errors[].code, never by the detail:
// duplicate is a taken account name or email (the person goes back to the
// page for another), at body.username or body.email when the code is
// requested and at body.message.payload when it is redeemed; key_enrolled is
// a public key this server holds already (the app makes that key again and
// redeems the same code: the refusal rolled the code back), at
// body.person_key or, for the device key, body.message.payload. It answers
// nil for every other error.
func appCodeConflict(err error) *Problem {
	if taken, ok := errors.AsType[*auth.NameTakenError](err); ok {
		location := redeemPayloadLocation
		if taken.Field != "" {
			location = locationBody + "." + taken.Field
		}
		return NewProblem(TypeConflict, "An account already uses this username or email.").
			WithErrors(ProblemError{Location: location, Code: codeDuplicate, Detail: "Choose another username or email."})
	}
	if enrolled, ok := errors.AsType[*auth.KeyEnrolledError](err); ok {
		location := redeemPayloadLocation
		if enrolled.Member == auth.KeyMemberPerson {
			location = locationBody + ".person_key"
		}
		return NewProblem(TypeConflict, "That public key is already enrolled on this server.").
			WithErrors(ProblemError{Location: location, Code: codeKeyEnrolled, Detail: "Make that key again and redeem the same code on a new challenge."})
	}
	return nil
}

const joinNotAvailableDetail = "This server adds people through its sign-in provider. Sign in with it on this server's site, then open Silo from your account settings."
