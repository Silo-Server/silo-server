package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// AppKeyEnvelope is a signed message (docs/auth-api.md, "App key sign-in").
type AppKeyEnvelope struct {
	KeyID     string  `json:"kid" minLength:"1" maxLength:"64" doc:"Key ID of the key that made sig: base64url of the first 16 bytes of SHA-256 over its 65-byte public key" example:"PgujdSfBbNNyXngLWCsOgw"`
	Payload   string  `json:"payload" minLength:"1" maxLength:"8192" doc:"base64url (no padding) of the UTF-8 JSON payload; the signatures cover silo-sync-v1.<purpose>.<payload> exactly as sent" example:"eyJwdXJwb3NlIjoic2lnbi1pbiJ9"`
	Sig       string  `json:"sig" minLength:"1" maxLength:"128" doc:"base64url of the 64-byte r || s ECDSA P-256 signature over SHA-256 of the signing input" example:"nnyAt7pV-XsUSmpX0IAk1d0QmUrO4z9CZ5Oeoy9QlVSyCq2EED4Ns_Aakhd5DgOmSqWWrT5xs8an1Ye0a4smgg"`
	DeviceSig *string `json:"device_sig,omitempty" minLength:"1" maxLength:"128" doc:"The device key's own signature over the same bytes. Required on introduce and enroll, refused on sign-in" example:"fMNUAuxbYE4rOU1dn5PqgBmxSoSu8GmnvtS_M3syK8N-uOt2jcfwhFX5z3BbCfmGy8V3ToHIzxZ8ySiNfdXqOw"`
}

// AppKeyEnvelopeInput is the body of introduce and sign-in: the envelope.
type AppKeyEnvelopeInput struct {
	SignInDeviceHeaders
	RawBody []byte
	Body    AppKeyEnvelope
}

// EnrollAppKeysInput is the body of enroll: the person key being enrolled
// beside an enroll envelope it signed.
type EnrollAppKeysInput struct {
	SignInDeviceHeaders
	RawBody []byte
	Body    struct {
		PersonKey string         `json:"person_key" minLength:"87" maxLength:"87" doc:"The person key being enrolled: base64url of its 65-byte uncompressed P-256 point" example:"BIXPevG2yxPrIkwoRlSuZ0c0DldsVX_3OET3S0YeP0uDN7Lj-SQMccAMdvNimX-OvE62RqjlO_Fll-F9MU_kw5Q"`
		Message   AppKeyEnvelope `json:"message" doc:"An enroll envelope signed with the private half of person_key"`
	}
}

// AppKeyChallengeInput is the challenge request: an empty JSON object.
type AppKeyChallengeInput struct {
	RawBody []byte
	Body    struct{}
}

// AppKeyChallenge is a challenge to sign.
type AppKeyChallenge struct {
	Challenge  string `json:"challenge" doc:"base64url of 32 random bytes. Good for one signed request within expires_in seconds" example:"8bqRFq-gVv5Dxjb61BaVfnDB3-F24muoSMMiatX49VA"`
	ExpiresIn  int    `json:"expires_in" doc:"Seconds the challenge stays good" example:"120"`
	ServerID   string `json:"server_id" doc:"This server's identity, which the signed payload names" example:"3f2a9d5e-6b1c-4c7e-9a0d-2f4b8c1e7a35"`
	ServerTime int64  `json:"server_time" doc:"The server's clock in seconds since the Unix epoch. A device stamps the payload's iat and exp from it" example:"1795578600"`
}

// AppKeyChallengeOutput is the createAppKeyChallenge response.
type AppKeyChallengeOutput struct {
	Body AppKeyChallenge
}

// SealedReply is an answer only the holder of the request's reply key can
// read.
type SealedReply struct {
	EphemeralKey string `json:"epk" doc:"The server's one-time P-256 public key, base64url of the 65-byte point" example:"BFAoQ6TpfXoAHwrvxBK7VXdPfM6Gwt7mqjIgxbn3PwlULI5vaNn1PcXf-cAKZEbeofM3XQ-a3E2RHfUs7rR74Kk"`
	Nonce        string `json:"nonce" doc:"base64url of the 12-byte AES-256-GCM nonce" example:"uTfZ9MxcZfYkDOcc"`
	Ciphertext   string `json:"ciphertext" doc:"base64url of the sealed answer with its 16-byte tag appended" example:"HmXEoRjnf6js2Ld9mI4A7szSS2-nsCuQzNNZO1CwSospE23QUKm4"`
}

// SealedReplyOutput is the response of every route that returns session
// tokens to a signed request. Like every credential response it is
// Cache-Control: no-store, the listener default.
type SealedReplyOutput struct {
	Body SealedReply
}

// appKeyReply is the plaintext a sealed reply carries.
type appKeyReply struct {
	Status    string     `json:"status"`
	DeviceID  string     `json:"device_id"`
	AccountID ID         `json:"account_id"`
	Tokens    *TokenPair `json:"tokens,omitempty"`
	// PersonKeyID is the account's live person key's ID as a JSON string,
	// or JSON null when it has none, on a signed_in answer to one of the
	// person's own device keys; left out of approval_required and of a
	// granted app's answers (personKeyIDMember).
	PersonKeyID json.RawMessage `json:"person_key_id,omitempty"`
	// Username and PersonKey are the account's name and what the redeem did
	// with the request's person key (enrolled or kept), on an app code's
	// redeem only.
	Username  string `json:"username,omitempty"`
	PersonKey string `json:"person_key,omitempty"`
	// LocalRoute is the pinned local route, on signed_in only, when the
	// listener has a TLS identity (docs/architecture/app-keys.md, "The pinned
	// route").
	LocalRoute *ServerLocalRoute `json:"local_route,omitempty"`
}

// providerAnswersDescription names the answers a key enrolled through the
// server's sign-in provider can get, and what each asks of the client.
const providerAnswersDescription = " A key enrolled through the server's sign-in provider can also get 409 provider_sign_in_required (the person signs in with the provider once, then the key works again), 503 provider_unavailable (try again later) or 403 not_permitted (the provider refused the person)."

// sealedRefusals lists the unsealed refusals of every signed route.
const sealedRefusals = "Refusals are not sealed: 401 signature_invalid (bad signature, unknown or revoked key, wrong purpose or server_id, outside iat and exp), 401 challenge_invalid (unknown, used or expired challenge), 403 origin_mismatch (a signed origin this server knows is wrong for the address the request arrived on), 422 validation_failed (the envelope or a payload field is malformed), 409 capability_disabled while auth.app_key_sign_in is off, and 429 rate_limited."

// sealedReplyIntro and sealedReplyRouteNote are the shared pieces of a
// sealed reply's description.
const (
	sealedReplyIntro     = " The response is a sealed reply: the answer encrypted to the payload's reply_key, salted with its challenge, so only the device that signed can read it."
	sealedReplyRouteNote = "where tokens is the login token pair and local_route the pinned local route as getServerConnections gives it (left out when the listener has no TLS identity)"
)

// personKeyIDNote states the person_key_id member of a signed_in reply to
// one of the person's own device keys.
const personKeyIDNote = "; person_key_id is the key ID (kid) of the account's live person key as the session opens, or null when the account has none: none was enrolled, it was revoked, or it is bound to a sign-in provider identity that is gone from the account (the key list leaves such a key out too). A device that knows a newer person key than the one named, or gets null, learns that this server holds an older person key or none. The member is absent only from a server that does not report it, and from the answers that never carry it: approval_required, and every answer to another app's key enrolled through a grant"

// sealedReplySignedIn describes a signed_in sealed reply whose plaintext
// carries extra members (each followed by ", ") before local_route, which
// notes, starting with "; ", explain. It states the whole shape once.
func sealedReplySignedIn(extra, notes string) string {
	return sealedReplyIntro + " Its plaintext is {status: signed_in, device_id, account_id, tokens, " + extra + "local_route}, " +
		sealedReplyRouteNote + notes + ". " + sealedRefusals
}

// personSealedReply describes a sealed reply to one of the person's own
// device keys: signed_in carries person_key_id, then extra members, which
// notes explain.
func personSealedReply(extra, notes string) string {
	return sealedReplySignedIn("person_key_id, "+extra, personKeyIDNote+notes)
}

// approvalReplyNote is the approval_required plaintext.
const approvalReplyNote = "; for an administrator's account that needs approval it is {status: approval_required, device_id, account_id}, with no tokens and no person_key_id"

// grantedAppReplyNote is what a reply to another app's key leaves out.
const grantedAppReplyNote = "; a reply to another app's key, one enrolled through a grant, has no person_key_id member, tokens.user.email is the empty string and tokens has no account_created_at: the app is not one of the person's devices, so it does not learn which person key the account holds (a key ID that changes would tell it the person replaced their key), nor the person's email"

// introduceSealedReply is introduceAppKey's sealed answer.
var introduceSealedReply = personSealedReply("", approvalReplyNote)

// signInSealedReply is signInWithAppKey's sealed answer, also to a granted
// app's key.
var signInSealedReply = personSealedReply("", approvalReplyNote+grantedAppReplyNote)

// grantSealedReply is redeemAppKeyGrant's sealed answer, to another app's
// key.
var grantSealedReply = sealedReplySignedIn("", grantedAppReplyNote)

// enrollSealedReply is enrollAppKeys' sealed answer.
var enrollSealedReply = personSealedReply("", "")

// enrollPayloadLocation is where enroll's signed payload sits in its body.
const enrollPayloadLocation = locationBody + ".message.payload"

// appKeyBodyLimits states the routes' body caps, which are lower than the
// shared structured-body cap.
const (
	appKeyChallengeBodyNote = " A body over 1 KiB is 413 payload_too_large."
	appKeySignedBodyNote    = " A body over 16 KiB is 413 payload_too_large."
)

// pendingRetryNote is how a device waiting for approval asks again.
const pendingRetryNote = " A device waiting for approval asks again each time with a fresh challenge and a newly signed envelope: resending the same envelope after a 429 answers 401 challenge_invalid, because its challenge was spent."

func registerAppKeySignIn(reg *Registry) {
	challenge := humaOp(http.MethodPost, Prefix+"/auth/keys/challenge", "createAppKeyChallenge", "app-keys",
		"Issue a challenge for one signed app key request.")
	challenge.Description = "Send an empty JSON object. The challenge is good for two minutes and one signed request, on any API process. Spends the per-address key_challenge rate-limit budget. 409 capability_disabled while auth.app_key_sign_in is off." + appKeyChallengeBodyNote
	challenge.Errors = []int{http.StatusConflict, http.StatusTooManyRequests}
	challengeOp := Operation{Operation: challenge, RetrySafety: RetrySafetyNonRetryable,
		Class: ClassPublic, ServiceBacked: true, RateLimitBucket: bucketKeyChallenge, MaxBodyBytes: 1 << 10}
	Register(reg, challengeOp, reg.createAppKeyChallenge)

	introduce := humaOp(http.MethodPost, Prefix+"/auth/keys/introduce", "introduceAppKey", "app-keys",
		"Enroll this device's key with the account's person key and open a login session.")
	introduce.Description = "The body is an introduce envelope signed with the account's person key, with device_sig from the device key in the payload. The device key is enrolled and opens the session. For an administrator's account it is stored waiting for approval, and the device repeats signInWithAppKey until it is approved. An account holds at most 10 waiting keys inside their 24 hours; an introduce that would add an eleventh is 409 conflict, after its challenge is spent. A waiting key past its 24 hours does not count, and a retention task later revokes it. A public key already enrolled on this server is 409 conflict. Spends the per-address key_sign_in rate-limit budget." + appKeySignedBodyNote + introduceSealedReply + providerAnswersDescription
	introduce.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	introduceOp := Operation{Operation: introduce, RetrySafety: RetrySafetyNonRetryable,
		Class: ClassPublic, ServiceBacked: true, Audited: true, RateLimitBucket: bucketKeySignIn, MaxBodyBytes: 16 << 10}
	Register(reg, introduceOp, reg.introduceAppKey)

	signIn := humaOp(http.MethodPost, Prefix+"/auth/keys/sign-in", "signInWithAppKey", "app-keys",
		"Open a login session with an enrolled device key.")
	signIn.Description = "The body is a sign-in envelope signed with the device key, with no device_sig. A key waiting for an administrator's approval answers approval_required, sealed, at most once every five seconds (sooner is 429 rate_limited with Retry-After) and for 24 hours; after that, or once denied, it is 403 key_denied." + pendingRetryNote + " A key whose provider binding changed during the request also answers 429 with a one-second Retry-After: wait, get a fresh challenge and sign a new envelope. That attempt goes through every sign-in check again, so it can still be refused (for example 409 provider_sign_in_required or 503). Spends the per-address key_sign_in rate-limit budget." + appKeySignedBodyNote + signInSealedReply + providerAnswersDescription
	signIn.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	signInOp := Operation{Operation: signIn, RetrySafety: RetrySafetyNonRetryable,
		Class: ClassPublic, ServiceBacked: true, RateLimitBucket: bucketKeySignIn, MaxBodyBytes: 16 << 10}
	Register(reg, signInOp, reg.signInWithAppKey)

	enroll := humaOp(http.MethodPost, Prefix+"/auth/keys/enroll", "enrollAppKeys", "app-keys",
		"Enroll a person key and a device key on the signed-in account and open a session bound to the device key.")
	enroll.Description = "Only the account's own login session enrolls: an API key, an administrator acting as the account, and a session opened by device sign-in or by another app's key are 403 permission_denied. A session opened before this server recorded how sessions open is refused the same way; signing in again clears that. message is an enroll envelope signed with the private half of person_key (its kid must be person_key's ID), with device_sig. When the account has no person key, person_key is registered and takes the provider identity of the session that enrolls it. When person_key is the account's person key already, it is reused as it is, binding unchanged. A different person key on the account is 409 conflict, and so is a device key already enrolled on this server. The new device key takes the session's provider identity. The client drops the session it called with. Keys enrolled here start active, also on an administrator's account, so enroll never answers approval_required. A malformed payload field is 422 at body.message.payload." + mainProfileRule + appKeySignedBodyNote + enrollSealedReply + providerAnswersDescription
	enroll.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	enrollOp := mainProfileOnly(Operation{Operation: enroll, RetrySafety: RetrySafetyNonRetryable,
		ServiceBacked: true, Audited: true, RateLimitBucket: bucketKeySignIn, MaxBodyBytes: 16 << 10})
	Register(reg, withOrigins(enrollOp, grantedRefusedSignIn), reg.enrollAppKeys)

	redeem := humaOp(http.MethodPost, Prefix+"/auth/grants/redeem", "redeemAppKeyGrant", "app-keys",
		"Enroll another app's key with a grant the person signed and open the app's login session.")
	redeem.Description = "The body is a grant-redeem envelope signed with the app's own key, the app_key the grant inside it names, with no device_sig. The grant is a grant envelope signed by the account's person key away from this server: good for at most five minutes, for this server_id, and once. origin must be one of the grant's origins. The app's key is enrolled as kind app, enrolled_by grant, named device_name or else the grant's app_name, and takes the provider identity of the person key that signed the grant. A session it opens cannot enroll keys, replace the person key, approve a device sign-in or revoke any key but its own. 403 app_grant_invalid is about the grant itself: it was already used, is for another server or address, does not verify by the account's live person key, or names an administrator's or disabled account. 403 app_grant_expired: the grant's own exp has passed. 401 signature_invalid: the redeemer does not hold the grant's app_key (the outer signature does not verify under it, or the envelope's kid is not that key's ID), or the grant-redeem envelope is outside its own iat and exp. A public key already enrolled on this server is 409 conflict. Spends the per-address key_sign_in rate-limit budget." + appKeySignedBodyNote + grantSealedReply + providerAnswersDescription
	redeem.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	redeemOp := Operation{Operation: redeem, RetrySafety: RetrySafetyNonRetryable,
		Class: ClassPublic, ServiceBacked: true, Audited: true, RateLimitBucket: bucketKeySignIn, MaxBodyBytes: 16 << 10}
	Register(reg, redeemOp, reg.redeemAppKeyGrant)

	declareRetryAfter(reg, "Seconds to wait before repeating the empty POST.", challengeOp)
	declareRetryAfter(reg, "Seconds to wait before asking again, on a fresh challenge.", introduceOp, signInOp, enrollOp, redeemOp)
}

// declareRetryAfter documents the Retry-After header the app key routes send
// with a 429 and a 503, as other operations that send it declare it.
func declareRetryAfter(reg *Registry, description string, ops ...Operation) {
	for _, op := range ops {
		registered := registeredOperation(reg.api.OpenAPI(), op)
		if registered == nil {
			continue
		}
		for _, status := range []string{"429", "503"} {
			response := registered.Responses[status]
			if response == nil {
				continue
			}
			if response.Headers == nil {
				response.Headers = map[string]*huma.Header{}
			}
			response.Headers["Retry-After"] = &huma.Header{Schema: &huma.Schema{Type: huma.TypeString},
				Description: description}
		}
	}
}

func (reg *Registry) createAppKeyChallenge(ctx context.Context, in *AppKeyChallengeInput) (*AppKeyChallengeOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, err := reg.appKeysOn(ctx)
	if err != nil {
		return nil, err
	}
	serverID, err := reg.serverID(ctx)
	if err != nil {
		return nil, err
	}
	challenge, serverTime, err := svc.IssueKeyChallenge(ctx)
	if err != nil {
		return nil, appKeyProblem(err)
	}
	return &AppKeyChallengeOutput{Body: AppKeyChallenge{
		Challenge: challenge, ExpiresIn: int(appkeys.ChallengeLifetime.Seconds()), ServerID: serverID, ServerTime: serverTime,
	}}, nil
}

// keyRequest builds the service request of a signed envelope.
func (reg *Registry) keyRequest(ctx context.Context, env appkeys.Envelope) (auth.KeyRequest, error) {
	serverID, err := reg.serverID(ctx)
	if err != nil {
		return auth.KeyRequest{}, err
	}
	known := reg.appKeyKnownOrigins(ctx)
	return auth.KeyRequest{
		Envelope: env, ServerID: serverID, IP: clientip.FromContext(ctx),
		CheckOrigin: func(origin string) error { return judgeSignedOrigin(known, origin) },
	}, nil
}

func (reg *Registry) introduceAppKey(ctx context.Context, in *AppKeyEnvelopeInput) (*SealedReplyOutput, error) {
	return reg.signedKeyRequest(ctx, in, appkeys.PurposeIntroduce, func(svc AppKeyService, ctx context.Context, req auth.KeyRequest) (*handlers.AppKeyAnswer, error) {
		return svc.IntroduceAppKey(ctx, req)
	})
}

func (reg *Registry) signInWithAppKey(ctx context.Context, in *AppKeyEnvelopeInput) (*SealedReplyOutput, error) {
	return reg.signedKeyRequest(ctx, in, appkeys.PurposeSignIn, func(svc AppKeyService, ctx context.Context, req auth.KeyRequest) (*handlers.AppKeyAnswer, error) {
		return svc.SignInWithAppKey(ctx, req)
	})
}

func (reg *Registry) redeemAppKeyGrant(ctx context.Context, in *AppKeyEnvelopeInput) (*SealedReplyOutput, error) {
	return reg.signedKeyRequest(ctx, in, appkeys.PurposeGrantRedeem, func(svc AppKeyService, ctx context.Context, req auth.KeyRequest) (*handlers.AppKeyAnswer, error) {
		return svc.RedeemAppKeyGrant(ctx, req)
	})
}

// signedKeyRequest runs a public signed route: the switch, the request, the
// service call and the sealed answer. The envelope is read from the raw body
// by appkeys.ParseEnvelope, the parser the protocol's vectors test: the
// framework's decoding matches field names regardless of case, and an
// envelope has exactly its fields.
func (reg *Registry) signedKeyRequest(ctx context.Context, in *AppKeyEnvelopeInput, purpose appkeys.Purpose,
	call func(AppKeyService, context.Context, auth.KeyRequest) (*handlers.AppKeyAnswer, error),
) (*SealedReplyOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, err := reg.appKeysOn(ctx)
	if err != nil {
		return nil, err
	}
	env, err := appkeys.ParseEnvelope(in.RawBody, purpose)
	if err != nil {
		return nil, appKeyProblem(err)
	}
	req, err := reg.keyRequest(ctx, env)
	if err != nil {
		return nil, err
	}
	answer, err := call(svc, withClientDevice(ctx), req)
	if err != nil {
		return nil, appKeyProblem(err)
	}
	return reg.sealAppKeyAnswer(answer)
}

func (reg *Registry) enrollAppKeys(ctx context.Context, in *EnrollAppKeysInput) (*SealedReplyOutput, error) {
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
	claims := claimsFrom(ctx)
	personKey, env, err := appkeys.ParseKeyBody(in.RawBody, appkeys.PurposeEnroll)
	if err != nil {
		return nil, appKeyProblemAt(err, enrollPayloadLocation)
	}
	req, err := reg.keyRequest(ctx, env)
	if err != nil {
		return nil, err
	}
	answer, err := svc.EnrollAppKeys(withClientDevice(ctx), auth.KeyEnrollRequest{KeyRequest: req, PersonKey: personKey, Claims: claims, Acting: acting})
	if err != nil {
		return nil, appKeyProblemAt(err, enrollPayloadLocation)
	}
	return reg.sealAppKeyAnswer(answer)
}

// localRoute is the pinned local route as getServerConnections reports it,
// or nil when the listener has no TLS identity.
func (reg *Registry) localRoute() *ServerLocalRoute {
	connections := reg.deps.ServerConnections
	if connections.LocalTLSPin == "" {
		return nil
	}
	route := &ServerLocalRoute{SPKISHA256: connections.LocalTLSPin}
	if connections.LocalURL != nil {
		if origin, ok := netaccess.NormalizeOrigin(connections.LocalURL()); ok {
			route.URL = origin
		}
	}
	return route
}

// sealAppKeyAnswer seals the answer's plaintext to the request's reply key,
// salted with its challenge. Every route that answers a signed request with
// session tokens builds its plaintext here, so a signed_in answer always
// carries the pinned local route.
func (reg *Registry) sealAppKeyAnswer(answer *handlers.AppKeyAnswer) (*SealedReplyOutput, error) {
	result := answer.Result
	reply := appKeyReply{Status: result.Status, DeviceID: result.DeviceID}
	if result.Status == appkeys.ReplySignedIn {
		reply.LocalRoute = reg.localRoute()
		reply.PersonKeyID = personKeyIDMember(result)
	}
	if result.User != nil {
		reply.AccountID = IDFromInt(int64(result.User.ID))
		if result.PersonKeyStatus != "" {
			reply.Username, reply.PersonKey = result.User.Username, result.PersonKeyStatus
		}
	}
	if answer.Tokens != nil {
		tokens := tokenPairFromView(*answer.Tokens)
		var kind auth.SessionKind
		if result.GrantedApp {
			kind = auth.SessionKindGrantedApp
			// Like its refreshes (handlers.RefreshedTokensView).
			tokens.AccountCreatedAt = nil
		}
		tokens.User = reg.accountForSession(answer.Tokens.User, kind)
		reply.Tokens = &tokens
	}
	plaintext, _ := json.Marshal(reply)
	sealed, err := appkeys.Seal(result.ReplyKey, result.Challenge, plaintext)
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	return &SealedReplyOutput{Body: SealedReply{
		EphemeralKey: sealed.EphemeralKey, Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext,
	}}, nil
}

// personKeyIDMember is the person_key_id member of a signed_in answer: the
// account's live person key's ID, null when it has none, or nothing when the
// answer does not say (a granted app's key: the person's key ID changing
// would tell that app the person replaced their key).
func personKeyIDMember(result *auth.KeySignInResult) json.RawMessage {
	if !result.PersonKeyReported {
		return nil
	}
	if result.PersonKeyID == "" {
		return json.RawMessage("null")
	}
	member, _ := json.Marshal(result.PersonKeyID)
	return member
}

// appKeyKnownOrigins is the origins a signed origin may be on this request,
// or nil when the server can't judge the address the request arrived on.
// Through a network access provider (its ingress token, not the Host
// header) that is the provider's connected origins; on the configured
// public address it is that address's origin. A LAN address or a reverse
// proxy can't be judged, which is why the reply is sealed. Every origin is
// compared in its canonical form, and one that won't canonicalize can't be
// judged.
func (reg *Registry) appKeyKnownOrigins(ctx context.Context) []string {
	connections := reg.deps.ServerConnections
	if path := netaccess.PathFromContext(ctx); !path.IsDefault() {
		// The installation the request came through, not the first
		// installation of that provider: two installations of one provider
		// serve different origins.
		status, ok := connections.Providers.Get(path.InstallationID)
		if !ok || status.Provider != path.Provider || !status.Connected() {
			return nil
		}
		var known []string
		for _, raw := range append([]string{status.Origin}, listenerOrigins(status)...) {
			if origin, err := appkeys.CanonicalOrigin(strings.TrimSpace(raw)); err == nil {
				known = append(known, origin)
			}
		}
		return known
	}
	r := requestFrom(ctx)
	if r == nil || connections.PublicURL == nil {
		return nil
	}
	public, err := appkeys.CanonicalOrigin(strings.TrimSpace(connections.PublicURL()))
	if err != nil {
		return nil
	}
	arrived, err := appkeys.CanonicalOrigin(clientip.RequestOrigin(r))
	if err != nil || arrived != public {
		return nil
	}
	return []string{public}
}

func listenerOrigins(status netaccess.Status) []string {
	out := make([]string, 0, len(status.Listeners))
	for _, listener := range status.Listeners {
		out = append(out, listener.Origin)
	}
	return out
}

// judgeSignedOrigin refuses a signed origin that isn't one of known. With
// nothing known the server can't judge, and accepts.
func judgeSignedOrigin(known []string, signed string) error {
	if len(known) == 0 || slices.Contains(known, signed) {
		return nil
	}
	return auth.ErrKeyOriginMismatch
}

// appKeyProblem maps an app key refusal to its problem. A refusal at or
// before the signature is signature_invalid; a malformed envelope or payload
// field, once the signature verifies, is validation_failed.
func appKeyProblem(err error) error {
	return appKeyProblemAt(err, locationBody+".payload")
}

// appKeyProblemAt is appKeyProblem with the location of the route's signed
// payload, for a payload field it refuses.
func appKeyProblemAt(err error, payloadLocation string) error {
	if errors.Is(err, auth.ErrSessionCheckUnavailable) {
		// As at refresh: the store couldn't be asked, and the key is not
		// judged.
		return NewProblem(TypeDependencyUnavailable, "The sign-in could not be checked right now. Retry after the Retry-After delay.").
			WithRetryAfter(apimw.CredentialCheckRetryAfterSeconds)
	}
	if errors.Is(err, auth.ErrProviderCheckIncomplete) {
		return NewProblem(TypeDependencyUnavailable, "The sign-in provider's answer could not be applied; try again later.")
	}
	if errors.Is(err, auth.ErrActingProfileChanged) {
		return mainProfileChangedProblem()
	}
	var retry *auth.KeyRetryError
	if errors.As(err, &retry) {
		detail := "This device is waiting for approval; ask again later."
		if retry.Changed {
			detail = "This device's key changed while signing in; ask again."
		}
		return NewProblem(TypeRateLimited, detail).WithRetryAfter(int(retry.RetryAfter.Seconds()))
	}
	if code := appkeys.CodeOf(err); code != "" {
		// Named app_grant_* so they can't be mistaken for the OAuth problem
		// invalid_grant.
		switch code {
		case appkeys.CodeGrantInvalid, appkeys.CodeOriginNotGranted:
			return NewProblem(TypeAppGrantInvalid, grantInvalidDetail)
		case appkeys.CodeGrantExpired:
			return NewProblem(TypeAppGrantExpired, "The grant has expired; ask the person to allow the app again.")
		}
		switch code.Stage() {
		case appkeys.StageShape:
			location := locationBody
			var refusal *appkeys.Error
			if errors.As(err, &refusal) && refusal.Field != "" {
				location += "." + refusal.Field
			}
			return validationProblem(location, codeInvalid, "The signed message is not in its shape for this route.")
		case appkeys.StageFields:
			return validationProblem(payloadLocation, codeInvalid, "A field of the signed payload is missing or malformed.")
		}
		return NewProblem(TypeSignatureInvalid, "The signed message did not verify.")
	}
	for _, m := range appKeyProblems {
		if errors.Is(err, m.err) {
			return NewProblem(m.kind, m.detail)
		}
	}
	return serviceProblem(err)
}

const grantInvalidDetail = "The grant is used, or not valid for this server, app, address or account."

var appKeyProblems = []struct {
	err    error
	kind   ProblemType
	detail string
}{
	{auth.ErrKeySignatureInvalid, TypeSignatureInvalid, "The signed message did not verify."},
	{auth.ErrGrantInvalid, TypeAppGrantInvalid, grantInvalidDetail},
	{auth.ErrKeyChallengeInvalid, TypeChallengeInvalid, "The challenge is unknown, used or expired; ask for a new one."},
	{auth.ErrKeyOriginMismatch, TypeOriginMismatch, "The signed origin is not the address this request arrived on."},
	{auth.ErrAppKeyDenied, TypeKeyDenied, "This device was not approved."},
	{auth.ErrAppKeyAlreadyEnrolled, TypeConflict, "That public key is already enrolled on this server."},
	{auth.ErrPersonKeyConflict, TypeConflict, "The account already has a different person key."},
	{auth.ErrPersonKeyExists, TypeConflict, "The account already has a different person key."},
	{auth.ErrPendingKeyLimit, TypeConflict, "This account already has the most devices waiting for approval; approve or deny one first."},
	{auth.ErrApproverNotAdmin, TypePermissionDenied, "Only an administrator account approves a waiting device."},
	{auth.ErrAppKeyNotFound, TypeNotFound, "No such device key on this account."},
	// The administrator's row and the key's own come first: errors.Is
	// matches each to ErrSessionNotAllowed too.
	{auth.ErrActingAsAccount, TypePermissionDenied, handlers.ActingAsAccountDetail},
	{auth.ErrKeyDecidesItself, TypePermissionDenied, "A key can't approve or refuse itself; use another signed-in device."},
	{auth.ErrSessionNotAllowed, TypePermissionDenied, handlers.SessionNotAllowedDetail},
	{auth.ErrSessionRevoked, TypeSessionExpired, "The login session is no longer valid."},
	{auth.ErrUserDisabled, TypePermissionDenied, "The account is disabled."},
	{auth.ErrLocalLoginDisabled, TypeLocalLoginDisabled, "This server signs this account in only through its sign-in provider."},
	{auth.ErrProviderSignInRequired, TypeProviderSignInRequired, "Sign in with this server's sign-in provider once; after that this device signs in again."},
	{auth.ErrNotPermitted, TypeNotPermitted, "The sign-in provider does not permit this account to sign in."},
	{auth.ErrProviderUnavailable, TypeProviderUnavailable, "The sign-in provider is unavailable; try again later."},
	{auth.ErrAppCodeInvalid, TypeAppCodeInvalid, "The app code is unknown, used or expired; get a new one from the server's page."},
	{auth.ErrAppCodeNameTaken, TypeConflict, "An account already uses this username or email; join again with another."},
	{auth.ErrJoinNotAvailable, TypeProviderSignInRequired, joinNotAvailableDetail},
	{auth.ErrTransactionalProfileUnavailable, TypeCapabilityUnsupported, "The selected store cannot atomically create the required profile."},
}
