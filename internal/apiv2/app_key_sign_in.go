package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
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

func (e AppKeyEnvelope) envelope() appkeys.Envelope {
	return appkeys.Envelope{KeyID: e.KeyID, Payload: e.Payload, Sig: e.Sig, DeviceSig: e.DeviceSig}
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
	// PersonKeyID is the enrolled person key's ID, on enroll only.
	PersonKeyID string `json:"person_key_id,omitempty"`
	// LocalRoute is the pinned local route, on signed_in only, when the
	// listener has a TLS identity (docs/architecture/app-keys.md, "The pinned
	// route").
	LocalRoute *ServerLocalRoute `json:"local_route,omitempty"`
}

const sealedReplyDescription = " The response is a sealed reply: the answer encrypted to the payload's reply_key, salted with its challenge, so only the device that signed can read it. Its plaintext is {status, device_id, account_id, tokens, local_route}, where tokens is the login token pair and local_route the pinned local route as getServerConnections gives it (left out when the listener has no TLS identity); for an administrator's account that needs approval it is {status: approval_required, device_id, account_id} with no tokens. Refusals are not sealed: 401 signature_invalid (bad signature, unknown or revoked key, wrong purpose or server_id, outside iat and exp), 401 challenge_invalid (unknown, used or expired challenge), 403 origin_mismatch (a signed origin this server knows is wrong for the address the request arrived on), 422 validation_failed (the envelope or a payload field is malformed), 409 capability_disabled while auth.app_key_sign_in is off, and 429 rate_limited."

func registerAppKeySignIn(reg *Registry) {
	challenge := humaOp(http.MethodPost, Prefix+"/auth/keys/challenge", "createAppKeyChallenge", "app-keys",
		"Issue a challenge for one signed app key request.")
	challenge.Description = "Send an empty JSON object. The challenge is good for two minutes and one signed request, on any API process. Spends the per-address key_challenge rate-limit budget. 409 capability_disabled while auth.app_key_sign_in is off."
	challenge.Errors = []int{http.StatusConflict, http.StatusTooManyRequests}
	Register(reg, Operation{Operation: challenge, RetrySafety: RetrySafetyNonRetryable,
		Class: ClassPublic, ServiceBacked: true, RateLimitBucket: bucketKeyChallenge, MaxBodyBytes: 1 << 10}, reg.createAppKeyChallenge)

	introduce := humaOp(http.MethodPost, Prefix+"/auth/keys/introduce", "introduceAppKey", "app-keys",
		"Enroll this device's key with the account's person key and open a login session.")
	introduce.Description = "The body is an introduce envelope signed with the account's person key, with device_sig from the device key in the payload. The device key is enrolled and opens the session. For an administrator's account it is stored waiting for approval, and the device repeats signInWithAppKey until it is approved. A public key already enrolled on this server is 409 conflict. Spends the per-address key_sign_in rate-limit budget." + sealedReplyDescription
	introduce.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	Register(reg, Operation{Operation: introduce, RetrySafety: RetrySafetyNonRetryable,
		Class: ClassPublic, ServiceBacked: true, RateLimitBucket: bucketKeySignIn, MaxBodyBytes: 16 << 10}, reg.introduceAppKey)

	signIn := humaOp(http.MethodPost, Prefix+"/auth/keys/sign-in", "signInWithAppKey", "app-keys",
		"Open a login session with an enrolled device key.")
	signIn.Description = "The body is a sign-in envelope signed with the device key, with no device_sig. A key waiting for an administrator's approval answers approval_required, sealed, at most once every five seconds (sooner is 429 rate_limited with Retry-After) and for 24 hours; after that, or once denied, it is 403 key_denied. Spends the per-address key_sign_in rate-limit budget." + sealedReplyDescription
	signIn.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	Register(reg, Operation{Operation: signIn, RetrySafety: RetrySafetyNonRetryable,
		Class: ClassPublic, ServiceBacked: true, RateLimitBucket: bucketKeySignIn, MaxBodyBytes: 16 << 10}, reg.signInWithAppKey)

	enroll := humaOp(http.MethodPost, Prefix+"/auth/keys/enroll", "enrollAppKeys", "app-keys",
		"Enroll a person key and a device key on the signed-in account and open a session bound to the device key.")
	enroll.Description = "Only the account's own login session enrolls: an API key, an administrator acting as the account, and a session opened by device sign-in or by another app's key are 403 permission_denied. message is an enroll envelope signed with the private half of person_key (its kid must be person_key's ID), with device_sig. The person key is registered when the account has none; a different one already on the account is 409 conflict, and so is a public key already enrolled on this server. Both keys take the provider identity of the session that enrolls them. The sealed plaintext adds person_key_id; the client drops the session it called with." + sealedReplyDescription
	enroll.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
	Register(reg, Operation{Operation: enroll, RetrySafety: RetrySafetyNonRetryable,
		Class: ClassAuthenticated, ServiceBacked: true, RateLimitBucket: bucketKeySignIn, MaxBodyBytes: 16 << 10}, reg.enrollAppKeys)
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
func (reg *Registry) keyRequest(ctx context.Context, env AppKeyEnvelope) (auth.KeyRequest, error) {
	serverID, err := reg.serverID(ctx)
	if err != nil {
		return auth.KeyRequest{}, err
	}
	known := reg.appKeyKnownOrigins(ctx)
	return auth.KeyRequest{
		Envelope: env.envelope(), ServerID: serverID, IP: clientip.FromContext(ctx),
		CheckOrigin: func(origin string) error { return judgeSignedOrigin(known, origin) },
	}, nil
}

func (reg *Registry) introduceAppKey(ctx context.Context, in *AppKeyEnvelopeInput) (*SealedReplyOutput, error) {
	return reg.signedKeyRequest(ctx, in, func(svc AppKeyService, ctx context.Context, req auth.KeyRequest) (*handlers.AppKeyAnswer, error) {
		return svc.IntroduceAppKey(ctx, req)
	})
}

func (reg *Registry) signInWithAppKey(ctx context.Context, in *AppKeyEnvelopeInput) (*SealedReplyOutput, error) {
	return reg.signedKeyRequest(ctx, in, func(svc AppKeyService, ctx context.Context, req auth.KeyRequest) (*handlers.AppKeyAnswer, error) {
		return svc.SignInWithAppKey(ctx, req)
	})
}

// signedKeyRequest runs a public signed route: the switch, the request, the
// service call and the sealed answer.
func (reg *Registry) signedKeyRequest(ctx context.Context, in *AppKeyEnvelopeInput,
	call func(AppKeyService, context.Context, auth.KeyRequest) (*handlers.AppKeyAnswer, error),
) (*SealedReplyOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, err := reg.appKeysOn(ctx)
	if err != nil {
		return nil, err
	}
	req, err := reg.keyRequest(ctx, in.Body)
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
	claims := claimsFrom(ctx)
	if !claims.IsOwnLoginSession() {
		return nil, appKeyProblem(auth.ErrSessionNotAllowed)
	}
	personKey, err := appkeys.ParsePublicKey(in.Body.PersonKey)
	if err != nil {
		return nil, validationProblem(locationBody+".person_key", codeInvalid, "person_key is not a P-256 public key.")
	}
	req, err := reg.keyRequest(ctx, in.Body.Message)
	if err != nil {
		return nil, err
	}
	answer, err := svc.EnrollAppKeys(withClientDevice(ctx), auth.KeyEnrollRequest{KeyRequest: req, PersonKey: personKey, Claims: claims})
	if err != nil {
		return nil, appKeyProblem(err)
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
	reply := appKeyReply{Status: result.Status, DeviceID: result.DeviceID, PersonKeyID: result.PersonKeyID}
	if result.Status == appkeys.ReplySignedIn {
		reply.LocalRoute = reg.localRoute()
	}
	if result.User != nil {
		reply.AccountID = IDFromInt(int64(result.User.ID))
	}
	if answer.Tokens != nil {
		tokens := tokenPairFromView(*answer.Tokens)
		reply.Tokens = &tokens
	}
	plaintext, err := json.Marshal(reply)
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	sealed, err := appkeys.Seal(result.ReplyKey, result.Challenge, plaintext)
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	return &SealedReplyOutput{Body: SealedReply{
		EphemeralKey: sealed.EphemeralKey, Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext,
	}}, nil
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
		status, ok := connections.Providers.ByProvider(path.Provider)
		if !ok || !status.Connected() {
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
	var retry *auth.KeyRetryError
	if errors.As(err, &retry) {
		return NewProblem(TypeRateLimited, "This device is waiting for approval; ask again later.").
			WithRetryAfter(int(retry.RetryAfter.Seconds()))
	}
	if code := appkeys.CodeOf(err); code != "" {
		switch code.Stage() {
		case appkeys.StageShape:
			return validationProblem(locationBody, codeInvalid, "The signed message is not in its shape for this route.")
		case appkeys.StageFields:
			return validationProblem(locationBody+".payload", codeInvalid, "A field of the signed payload is missing or malformed.")
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

var appKeyProblems = []struct {
	err    error
	kind   ProblemType
	detail string
}{
	{auth.ErrKeySignatureInvalid, TypeSignatureInvalid, "The signed message did not verify."},
	{auth.ErrKeyChallengeInvalid, TypeChallengeInvalid, "The challenge is unknown, used or expired; ask for a new one."},
	{auth.ErrKeyOriginMismatch, TypeOriginMismatch, "The signed origin is not the address this request arrived on."},
	{auth.ErrAppKeyDenied, TypeKeyDenied, "This device was not approved."},
	{auth.ErrAppKeyAlreadyEnrolled, TypeConflict, "That public key is already enrolled on this server."},
	{auth.ErrPersonKeyConflict, TypeConflict, "The account already has a different person key."},
	{auth.ErrPersonKeyExists, TypeConflict, "The account already has a different person key."},
	{auth.ErrAppKeyNotFound, TypeNotFound, "No such device key on this account."},
	{auth.ErrSessionNotAllowed, TypePermissionDenied, "This sign-in cannot manage app keys."},
	{auth.ErrSessionRevoked, TypeSessionExpired, "The login session is no longer valid."},
	{auth.ErrUserDisabled, TypePermissionDenied, "The account is disabled."},
	{auth.ErrLocalLoginDisabled, TypeLocalLoginDisabled, "This server signs this account in only through its sign-in provider."},
	{auth.ErrNotPermitted, TypeNotPermitted, "The sign-in provider does not permit this account to sign in."},
	{auth.ErrProviderUnavailable, TypeProviderUnavailable, "The sign-in provider is unavailable; try again later."},
}
