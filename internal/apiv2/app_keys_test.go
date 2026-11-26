package apiv2

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeAppKeys answers the app key calls: the signed ones, the revocation and
// the replacement with err when set, otherwise answer. A grant redemption
// answers grantErr first when set.
type fakeAppKeys struct {
	enabled  bool
	err      error
	grantErr error
	// credentialErr answers SessionMayIssueCredential.
	credentialErr error
	answer        *handlers.AppKeyAnswer
	keys          auth.OwnAppKeys
	pending       []*models.AppDeviceKey
	// pendingErr is what the waiting-device operations answer.
	pendingErr error
	calls      int
	// households backs the real main-profile rule; nil uses fakeHouseholds.
	households userstore.UserStoreProvider
	// acting is the checked profile the last key-creating call carried.
	acting auth.ActingProfile
	// sessionAccess answers AppKeySessionAccess; appCodeErr answers
	// IssueSessionAppCode.
	sessionAccess auth.SessionKeyAccess
	appCodeErr    error
	// manageErr answers SessionMayManageKeys.
	manageErr error
}

// ActsAsVerifiedMainProfile runs the production rule against the fake
// households, so the route tests exercise it, not a stand-in.
func (f *fakeAppKeys) ActsAsVerifiedMainProfile(ctx context.Context, userID int, profileID string, verify func(*userstore.Profile) error) (bool, auth.ActingProfile, error) {
	rule := handlers.NewAuthHandler(nil, nil, nil)
	if f.households != nil {
		rule.SetHouseholdStores(f.households, false)
	} else {
		rule.SetHouseholdStores(fakeHouseholds{}, false)
	}
	return rule.ActsAsVerifiedMainProfile(ctx, userID, profileID, verify)
}

func (f *fakeAppKeys) ListPendingAppKeys(context.Context, *auth.Claims) ([]*models.AppDeviceKey, error) {
	f.calls++
	return f.pending, f.pendingErr
}

func (f *fakeAppKeys) ApprovePendingAppKey(_ context.Context, _ *auth.Claims, _ string, acting auth.ActingProfile) error {
	f.acting = acting
	f.calls++
	return f.pendingErr
}

func (f *fakeAppKeys) DenyPendingAppKey(context.Context, *auth.Claims, string) error {
	f.calls++
	return f.pendingErr
}

func (f *fakeAppKeys) AppKeySignInEnabled(context.Context) (bool, error) { return f.enabled, nil }

func (f *fakeAppKeys) IssueKeyChallenge(context.Context) (string, int64, error) {
	f.calls++
	return "8bqRFq-gVv5Dxjb61BaVfnDB3-F24muoSMMiatX49VA", 1795578600, nil
}

func (f *fakeAppKeys) signed() (*handlers.AppKeyAnswer, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.answer, nil
}

func (f *fakeAppKeys) IntroduceAppKey(context.Context, auth.KeyRequest) (*handlers.AppKeyAnswer, error) {
	return f.signed()
}

func (f *fakeAppKeys) SignInWithAppKey(context.Context, auth.KeyRequest) (*handlers.AppKeyAnswer, error) {
	return f.signed()
}

func (f *fakeAppKeys) EnrollAppKeys(_ context.Context, req auth.KeyEnrollRequest) (*handlers.AppKeyAnswer, error) {
	f.acting = req.Acting
	return f.signed()
}

func (f *fakeAppKeys) RedeemAppKeyGrant(context.Context, auth.KeyRequest) (*handlers.AppKeyAnswer, error) {
	if f.grantErr != nil {
		f.calls++
		return nil, f.grantErr
	}
	return f.signed()
}

func (f *fakeAppKeys) ListOwnAppKeys(context.Context, *auth.Claims) (auth.OwnAppKeys, error) {
	f.calls++
	return f.keys, nil
}

func (f *fakeAppKeys) RevokeOwnDeviceKey(context.Context, *auth.Claims, string) error {
	f.calls++
	return f.err
}

func (f *fakeAppKeys) ReplacePersonKey(_ context.Context, _ *auth.Claims, _ appkeys.PublicKey, acting auth.ActingProfile) error {
	f.acting = acting
	f.calls++
	return f.err
}

func (f *fakeAppKeys) IssueSessionAppCode(_ context.Context, _ *auth.Claims, acting auth.ActingProfile) (auth.IssuedAppCode, error) {
	f.acting = acting
	f.calls++
	if f.appCodeErr != nil {
		return auth.IssuedAppCode{}, f.appCodeErr
	}
	return testIssuedAppCode(), nil
}

func (f *fakeAppKeys) RedeemAppCode(context.Context, auth.KeyRedeemRequest) (*handlers.AppKeyAnswer, error) {
	return f.signed()
}

func (f *fakeAppKeys) AppKeySessionAccess(context.Context, *auth.Claims) (auth.SessionKeyAccess, error) {
	return f.sessionAccess, nil
}

// testAppCode is a well-formed app code.
const testAppCode = "q0yR3lbxN7D5mE4pTH2n6Qv8sK1aWcJfUgXoYdZiBtM"

// testIssuedAppCode is a fresh code; the half second keeps expires_in at
// 600 however long the request takes to answer.
func testIssuedAppCode() auth.IssuedAppCode {
	return auth.IssuedAppCode{Code: testAppCode, ExpiresAt: time.Now().Add(auth.AppCodeLifetime + 500*time.Millisecond)}
}

func (f *fakeAppKeys) SessionMayIssueCredential(context.Context, *auth.Claims) error {
	return f.credentialErr
}

// A syntactically valid envelope; the fake service never verifies it.
const testEnvelope = `{"kid":"zT321tSgcB7emUH9k0B0oA","payload":"eyJ9","sig":"c2ln"}`

const testPersonKey = "BIXPevG2yxPrIkwoRlSuZ0c0DldsVX_3OET3S0YeP0uDN7Lj-SQMccAMdvNimX-OvE62RqjlO_Fll-F9MU_kw5Q"

func appKeyDeps(keys *fakeAppKeys) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.ServerIdentity = fakeServerIdentity{id: fixtureServerID}
	deps.AppKeys = keys
	return deps
}

// Apps read the state to decide whether to offer key sign-in, so it must
// follow the server setting, and name the protocol and the server_id they
// sign for.
func TestAppKeyCapabilityFollowsSetting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		demo    bool
		state   string
	}{
		{name: "setting on", enabled: true, state: StateAvailable},
		{name: "setting off", state: StateDisabled},
		{name: "setting on, demo mode", enabled: true, demo: true, state: StateDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := appKeyDeps(&fakeAppKeys{enabled: tc.enabled})
			deps.DemoSettings = fakeSettings{demo: tc.demo}
			rec := do(t, NewHandler(deps), http.MethodGet, Prefix+"/auth/keys/capability", "", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			var body AppKeyCapability
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.State != tc.state || !body.SignIn || !body.Introduce || !body.Enroll || !body.Grants || body.Protocol != appkeys.Protocol || body.ServerID != fixtureServerID {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

// While auth.app_key_sign_in is off, every route that issues a challenge,
// opens a session or changes the person key answers capability_disabled
// before the service runs; a person's own key list and its revocation stay
// open, so keys can still be reviewed and revoked.
func TestAppKeyRoutesFollowTheSwitch(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		open               int
	}{
		{method: http.MethodPost, path: "/auth/keys/challenge", body: `{}`},
		{method: http.MethodPost, path: "/auth/keys/introduce", body: testEnvelope},
		{method: http.MethodPost, path: "/auth/keys/sign-in", body: testEnvelope},
		{method: http.MethodPost, path: "/auth/grants/redeem", body: testEnvelope},
		{method: http.MethodPost, path: "/auth/keys/enroll", body: `{"person_key":"` + testPersonKey + `","message":` + testEnvelope + `}`, headers: bearer(memberToken)},
		{method: http.MethodPut, path: "/auth/keys/person", body: `{"person_key":"` + testPersonKey + `"}`, headers: bearer(memberToken)},
		{method: http.MethodGet, path: "/auth/keys", headers: bearer(memberToken), open: http.StatusOK},
		{method: http.MethodDelete, path: "/auth/keys/devices/0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11", headers: bearer(memberToken), open: http.StatusNoContent},
		{method: http.MethodPost, path: "/auth/keys/pending/0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11/approve", body: `{}`, headers: mainProfile(memberToken)},
		{method: http.MethodGet, path: "/auth/keys/pending", headers: bearer(memberToken), open: http.StatusOK},
		{method: http.MethodPost, path: "/auth/keys/pending/0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11/deny", body: `{}`, headers: bearer(memberToken), open: http.StatusNoContent},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			keys := &fakeAppKeys{}
			rec := do(t, NewHandler(appKeyDeps(keys)), tc.method, Prefix+tc.path, tc.body, tc.headers)
			if tc.open != 0 {
				if rec.Code != tc.open || keys.calls != 1 {
					t.Fatalf("status = %d, calls %d: %s", rec.Code, keys.calls, rec.Body.String())
				}
				return
			}
			requireProblem(t, rec, TypeCapabilityDisabled)
			if keys.calls != 0 {
				t.Fatalf("service ran %d times with the switch off", keys.calls)
			}
			// A demo server's visitors share one account, so app keys are
			// off there whatever the setting says: the same routes close,
			// and the same ones stay open.
			demo := &fakeAppKeys{enabled: true}
			deps := appKeyDeps(demo)
			deps.DemoSettings = fakeSettings{demo: true}
			requireProblem(t, do(t, NewHandler(deps), tc.method, Prefix+tc.path, tc.body, tc.headers), TypeCapabilityDisabled)
			if demo.calls != 0 {
				t.Fatalf("service ran %d times in demo mode", demo.calls)
			}
			// A demo setting that can't be read closes them too: the
			// route fails, and the service doesn't run.
			unread := &fakeAppKeys{enabled: true}
			deps = appKeyDeps(unread)
			deps.DemoSettings = fakeSettings{err: errors.New("settings unavailable")}
			if rec := do(t, NewHandler(deps), tc.method, Prefix+tc.path, tc.body, tc.headers); rec.Code < http.StatusInternalServerError || unread.calls != 0 {
				t.Fatalf("with the demo setting unreadable: status %d, service ran %d times", rec.Code, unread.calls)
			}
		})
	}
}

// The sealed reply is the only place session tokens leave a key sign-in, and
// another team's client parses its plaintext: only the reply key's holder can
// open it, and its fields are the protocol's. A signed_in answer also carries
// the pinned local route the device moves to; approval_required doesn't.
// person_key_id has three states a client tells apart: a string, null (the
// account has no person key) and absent (approval_required, and every answer
// to another app's key, which also gets no email and no creation time).
func TestAppKeySealedReply(t *testing.T) {
	reply, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	replyKey, err := appkeys.PublicKeyFromBytes(reply.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	challenge := make([]byte, appkeys.ChallengeSize)
	user := &models.User{ID: 4}
	tokens := &handlers.TokenPairView{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 28800,
		User: handlers.UserView{ID: 4, Username: "sam", Email: "sam@example.test", Role: "user", DownloadAllowed: true, CreatedAt: fixtureAccountCreatedAt}}
	const (
		deviceID = "0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11"
		ownUser  = `"user":{"id":"4","username":"sam","email":"sam@example.test","role":"user","permissions":[],"download_allowed":true,"password_change_required":false},"account_created_at":"2025-03-14T09:26:53.589793Z"}`
		appUser  = `"user":{"id":"4","username":"sam","email":"","role":"user","permissions":[],"download_allowed":false,"password_change_required":false}}`
		pair     = `"tokens":{"access_token":"access","refresh_token":"refresh","expires_in":28800,`
		route    = `"local_route":{"spki_sha256":"n4bQgYhMfWWaL+qgxVrQFaO/TxsrC4Is0V1sFbDwCgg=","url":"http://192.168.1.20:8080"}}`
		signedIn = `{"status":"signed_in","device_id":"` + deviceID + `","account_id":"4",`
	)
	for _, tc := range []struct {
		name   string
		result auth.KeySignInResult
		tokens *handlers.TokenPairView
		want   string
	}{
		{name: "signed in", tokens: tokens,
			result: auth.KeySignInResult{Status: appkeys.ReplySignedIn, DeviceID: deviceID, User: user, PersonKeyReported: true, PersonKeyID: "PgujdSfBbNNyXngLWCsOgw"},
			want:   signedIn + pair + ownUser + `,"person_key_id":"PgujdSfBbNNyXngLWCsOgw",` + route},
		{name: "signed in, no person key", tokens: tokens,
			result: auth.KeySignInResult{Status: appkeys.ReplySignedIn, DeviceID: deviceID, User: user, PersonKeyReported: true},
			want:   signedIn + pair + ownUser + `,"person_key_id":null,` + route},
		{name: "another app's key", tokens: tokens,
			result: auth.KeySignInResult{Status: appkeys.ReplySignedIn, DeviceID: deviceID, User: user, GrantedApp: true},
			want:   signedIn + pair + appUser + `,` + route},
		{name: "approval required",
			result: auth.KeySignInResult{Status: appkeys.ReplyApprovalRequired, DeviceID: deviceID, User: user, PersonKeyReported: true, PersonKeyID: "PgujdSfBbNNyXngLWCsOgw"},
			want:   `{"status":"approval_required","device_id":"` + deviceID + `","account_id":"4"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.result
			result.ReplyKey, result.Challenge = replyKey, challenge
			keys := &fakeAppKeys{enabled: true, answer: &handlers.AppKeyAnswer{Result: &result, Tokens: tc.tokens}}
			deps := appKeyDeps(keys)
			deps.ServerConnections = ServerConnections{
				LocalTLSPin: "n4bQgYhMfWWaL+qgxVrQFaO/TxsrC4Is0V1sFbDwCgg=",
				LocalURL:    func() string { return "HTTP://192.168.1.20:8080/" },
			}
			rec := do(t, NewHandler(deps), http.MethodPost, Prefix+"/auth/keys/sign-in", testEnvelope, nil)
			if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status = %d, cache %q: %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
			}
			var sealed appkeys.SealedReply
			if err := json.Unmarshal(rec.Body.Bytes(), &sealed); err != nil {
				t.Fatal(err)
			}
			plaintext, err := appkeys.Open(reply.Bytes(), challenge, sealed)
			if err != nil || string(plaintext) != tc.want {
				t.Fatalf("opened %s, %v", plaintext, err)
			}
			other, _ := ecdh.P256().GenerateKey(rand.Reader)
			if _, err := appkeys.Open(other.Bytes(), challenge, sealed); err == nil {
				t.Fatal("another private key opened the reply")
			}
		})
	}
}

// openSealedAnswer posts a signed request to path with the service answering
// result and tokens, and returns the decrypted plaintext's tokens.user.
func openSealedAnswer(t *testing.T, path string, result auth.KeySignInResult, tokens *handlers.TokenPairView) Account {
	t.Helper()
	reply, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	result.ReplyKey, err = appkeys.PublicKeyFromBytes(reply.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	result.Challenge = make([]byte, appkeys.ChallengeSize)
	keys := &fakeAppKeys{enabled: true, answer: &handlers.AppKeyAnswer{Result: &result, Tokens: tokens}}
	rec := do(t, NewHandler(appKeyDeps(keys)), http.MethodPost, Prefix+path, testEnvelope, nil)
	var sealed appkeys.SealedReply
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &sealed) != nil {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	plaintext, err := appkeys.Open(reply.Bytes(), result.Challenge, sealed)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Tokens TokenPair `json:"tokens"`
	}
	if err := json.Unmarshal(plaintext, &body); err != nil {
		t.Fatal(err)
	}
	return body.Tokens.User
}

// A session another app's key opened reads one account object, wherever it
// reads it: the current-user read and tokens.user in its redeem's and its
// sign-ins' sealed replies are built by one function, so the email is the
// empty string and download_allowed false (downloads are refused to it) in
// all of them, while the person's own device key reads the real values.
// Catches the sealed reply drifting from the current-user read, which once
// carried the email and the account's download permission to such an app.
func TestGrantedAppReadsOneAccount(t *testing.T) {
	deps := appKeyDeps(&fakeAppKeys{enabled: true})
	view := deps.Accounts.(fakeAccounts).users[1]
	tokens := &handlers.TokenPairView{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600, User: view}
	rec := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/account/me", "", bearer(grantedAppToken))
	var me Account
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &me) != nil {
		t.Fatalf("current user: %d %s", rec.Code, rec.Body.String())
	}
	if me.Email != "" || me.DownloadAllowed || me.Username != view.Username {
		t.Fatalf("current user for another app = %+v", me)
	}
	user := &models.User{ID: 1}
	for _, path := range []string{"/auth/grants/redeem", "/auth/keys/sign-in"} {
		got := openSealedAnswer(t, path, auth.KeySignInResult{Status: appkeys.ReplySignedIn, User: user, GrantedApp: true}, tokens)
		if !reflect.DeepEqual(got, me) {
			t.Fatalf("%s: sealed tokens.user %+v, current user %+v", path, got, me)
		}
	}
	own := openSealedAnswer(t, "/auth/keys/sign-in", auth.KeySignInResult{Status: appkeys.ReplySignedIn, User: user, PersonKeyReported: true}, tokens)
	if own.Email != view.Email || !own.DownloadAllowed {
		t.Fatalf("the person's own key: email %q, download_allowed %v", own.Email, own.DownloadAllowed)
	}
}

// Clients branch on the problem identifier, so each refusal must keep its
// type: a bad key or signature can't look like a spent challenge, and a
// malformed message once signed must not look like a bad signature.
func TestAppKeyProblems(t *testing.T) {
	for _, tc := range []struct {
		err   error
		want  ProblemType
		retry string
	}{
		{err: auth.ErrKeySignatureInvalid, want: TypeSignatureInvalid},
		{err: &appkeys.Error{Code: appkeys.CodeSignatureInvalid}, want: TypeSignatureInvalid},
		{err: &appkeys.Error{Code: appkeys.CodeKidMismatch}, want: TypeSignatureInvalid},
		{err: &appkeys.Error{Code: appkeys.CodeServerMismatch}, want: TypeSignatureInvalid},
		{err: &appkeys.Error{Code: appkeys.CodePurposeMismatch}, want: TypeSignatureInvalid},
		{err: &appkeys.Error{Code: appkeys.CodeExpired}, want: TypeSignatureInvalid},
		{err: &appkeys.Error{Code: appkeys.CodeEnvelopeMalformed}, want: TypeValidationFailed},
		{err: &appkeys.Error{Code: appkeys.CodeFieldInvalid}, want: TypeValidationFailed},
		{err: auth.ErrKeyChallengeInvalid, want: TypeChallengeInvalid},
		{err: auth.ErrKeyOriginMismatch, want: TypeOriginMismatch},
		{err: auth.ErrAppKeyDenied, want: TypeKeyDenied},
		{err: auth.ErrAppKeyAlreadyEnrolled, want: TypeConflict},
		{err: auth.ErrPendingKeyLimit, want: TypeConflict},
		{err: auth.ErrLocalLoginDisabled, want: TypeLocalLoginDisabled},
		{err: auth.ErrNotPermitted, want: TypeNotPermitted},
		{err: auth.ErrProviderSignInRequired, want: TypeProviderSignInRequired},
		{err: &auth.KeyRetryError{RetryAfter: 3 * time.Second}, want: TypeRateLimited, retry: "3"},
		{err: fmt.Errorf("getting session: %w", auth.ErrSessionCheckUnavailable), want: TypeDependencyUnavailable, retry: strconv.Itoa(apimw.CredentialCheckRetryAfterSeconds)},
		{err: auth.ErrProviderCheckIncomplete, want: TypeDependencyUnavailable},
		// The provider anchor's lookup failing is reported like the session
		// check's store failure.
		{err: fmt.Errorf("reading provider identity: %w: %w", auth.ErrSessionCheckUnavailable, errors.New("connection reset")), want: TypeDependencyUnavailable, retry: strconv.Itoa(apimw.CredentialCheckRetryAfterSeconds)},
		{err: auth.ErrProviderUnavailable, want: TypeProviderUnavailable},
		// An app tells a used or wrong grant apart from an expired one,
		// which a wrong clock where it was signed causes.
		{err: auth.ErrGrantInvalid, want: TypeAppGrantInvalid},
		{err: &appkeys.Error{Code: appkeys.CodeGrantInvalid}, want: TypeAppGrantInvalid},
		{err: &appkeys.Error{Code: appkeys.CodeOriginNotGranted}, want: TypeAppGrantInvalid},
		{err: &appkeys.Error{Code: appkeys.CodeGrantExpired}, want: TypeAppGrantExpired},
	} {
		t.Run(tc.want.ID+" "+tc.err.Error(), func(t *testing.T) {
			keys := &fakeAppKeys{enabled: true, err: tc.err}
			rec := do(t, NewHandler(appKeyDeps(keys)), http.MethodPost, Prefix+"/auth/keys/sign-in", testEnvelope, nil)
			requireProblem(t, rec, tc.want)
			if got := rec.Header().Get("Retry-After"); got != tc.retry {
				t.Fatalf("Retry-After = %q, want %q", got, tc.retry)
			}
		})
	}
}

// Enrolling keys gives lasting access to the account, so an API key and an
// administrator acting as the account are refused before anything is
// verified.
func TestEnrollAppKeysNeedsOwnSession(t *testing.T) {
	body := `{"person_key":"` + testPersonKey + `","message":` + testEnvelope + `}`
	for _, token := range []string{apiKeyToken, impersonatedToken} {
		keys := &fakeAppKeys{enabled: true}
		rec := do(t, NewHandler(appKeyDeps(keys)), http.MethodPost, Prefix+"/auth/keys/enroll", body, bearer(token))
		requireProblem(t, rec, TypePermissionDenied)
		if keys.calls != 0 {
			t.Fatalf("%s: service ran", token)
		}
	}
}

// The server refuses a signed origin only where it can judge the address:
// through a network access provider (its ingress token) and on the public
// address. Elsewhere (a LAN address, a reverse proxy) it accepts, and the
// sealed reply protects the answer.
func TestAppKeyOriginRule(t *testing.T) {
	cache := netaccess.NewStatusCache()
	cache.Report(netaccess.Status{InstallationID: 7, Provider: "stub", State: netaccess.StateConnected, Origin: "HTTPS://Silo.Overlay.Example.Test/"})
	// A second installation of the same provider, on another overlay.
	cache.Report(netaccess.Status{InstallationID: 8, Provider: "stub", State: netaccess.StateConnected, Origin: "https://other.overlay.example.test"})
	reg := &Registry{deps: Dependencies{ServerConnections: ServerConnections{
		PublicURL: func() string { return "https://media.example.test/silo/" },
		Providers: cache,
	}}}
	for _, tc := range []struct {
		name, host, signed string
		provider           string
		installation       int
		refused            bool
	}{
		{name: "public address, its origin", host: "media.example.test", signed: "https://media.example.test"},
		{name: "public address, another origin", host: "media.example.test", signed: "https://evil.example.test", refused: true},
		{name: "provider path, its origin", host: "anything.example.test", provider: "stub", signed: "https://silo.overlay.example.test"},
		{name: "provider path, the public origin", host: "media.example.test", provider: "stub", signed: "https://media.example.test", refused: true},
		{name: "LAN address", host: "192.168.1.20:8080", signed: "https://evil.example.test"},
		{name: "second installation, its origin", host: "anything.example.test", provider: "stub", installation: 8, signed: "https://other.overlay.example.test"},
		{name: "second installation, the first one's origin", host: "anything.example.test", provider: "stub", installation: 8, signed: "https://silo.overlay.example.test", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "https://"+tc.host+"/api/v2/auth/keys/sign-in", nil)
			ctx := context.WithValue(r.Context(), requestKey{}, r)
			if tc.provider != "" {
				installation := tc.installation
				if installation == 0 {
					installation = 7
				}
				ctx = netaccess.WithPath(ctx, netaccess.Path{Provider: tc.provider, InstallationID: installation})
			}
			err := judgeSignedOrigin(reg.appKeyKnownOrigins(ctx), tc.signed)
			if (err != nil) != tc.refused {
				t.Fatalf("judge(%q) = %v, refused want %v", tc.signed, err, tc.refused)
			}
		})
	}
}

// An envelope has exactly its fields, and the route refuses anything else
// before a key is looked up: an extra field is 422 validation_failed, and a
// body that isn't JSON is 400 malformed_request.
func TestAppKeyEnvelopeIsStrict(t *testing.T) {
	for _, tc := range []struct {
		body string
		want ProblemType
	}{
		{body: `{"kid":"zT321tSgcB7emUH9k0B0oA","payload":"eyJ9","sig":"c2ln","alg":"ES256"}`, want: TypeValidationFailed},
		{body: `{"kid":"zT321tSgcB7emUH9k0B0oA","payload":"eyJ9"}`, want: TypeValidationFailed},
		{body: `not json`, want: TypeMalformedRequest},
		{body: `{"KID":"zT321tSgcB7emUH9k0B0oA","Payload":"eyJ9","Sig":"c2ln"}`, want: TypeValidationFailed},
		{body: `{"kid":"zT321tSgcB7emUH9k0B0oA","payload":"eyJ9","sig":"c2ln","kid":"PgujdSfBbNNyXngLWCsOgw"}`, want: TypeValidationFailed},
	} {
		keys := &fakeAppKeys{enabled: true}
		rec := do(t, NewHandler(appKeyDeps(keys)), http.MethodPost, Prefix+"/auth/keys/sign-in", tc.body, nil)
		requireProblem(t, rec, tc.want)
		if keys.calls != 0 {
			t.Fatalf("%s: service ran", tc.body)
		}
	}
}

// The enroll body is exactly person_key and message, each once, and a bad
// person_key is reported at its own field.
func TestEnrollAppKeysBodyIsStrict(t *testing.T) {
	for _, tc := range []struct {
		body, location string
	}{
		{body: `{"person_key":"` + testPersonKey + `","message":` + testEnvelope + `,"person_key":"` + testPersonKey + `"}`, location: "body"},
		{body: `{"person_key":"` + strings.Repeat("A", 87) + `","message":` + testEnvelope + `}`, location: "body.person_key"},
	} {
		keys := &fakeAppKeys{enabled: true}
		p := requireProblem(t, do(t, NewHandler(appKeyDeps(keys)), http.MethodPost, Prefix+"/auth/keys/enroll", tc.body, mainProfile(memberToken)), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || keys.calls != 0 {
			t.Fatalf("errors = %+v, calls %d; want location %s", p.Errors, keys.calls, tc.location)
		}
	}
}

// A malformed field of the signed payload is reported where that payload sits
// in the route's body: the envelope itself on sign-in, message on enroll.
func TestAppKeyPayloadErrorLocation(t *testing.T) {
	enrollEnvelope := `{"kid":"zT321tSgcB7emUH9k0B0oA","payload":"eyJ9","sig":"c2ln","device_sig":"c2ln"}`
	for _, tc := range []struct {
		path, body, location string
		headers              map[string]string
	}{
		{path: "/auth/keys/sign-in", body: testEnvelope, location: "body.payload"},
		{path: "/auth/keys/enroll", body: `{"person_key":"` + testPersonKey + `","message":` + enrollEnvelope + `}`, headers: mainProfile(memberToken), location: "body.message.payload"},
	} {
		keys := &fakeAppKeys{enabled: true, err: &appkeys.Error{Code: appkeys.CodeFieldInvalid}}
		p := requireProblem(t, do(t, NewHandler(appKeyDeps(keys)), http.MethodPost, Prefix+tc.path, tc.body, tc.headers), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location {
			t.Fatalf("%s: errors = %+v, want location %s", tc.path, p.Errors, tc.location)
		}
	}
}

func (f *fakeAppKeys) VerifyProfileSet(context.Context, auth.KeyRequest) (*auth.ProfileSetAuthority, error) {
	return nil, f.err
}

func (f *fakeAppKeys) SessionMayManageKeys(context.Context, *auth.Claims) error { return f.manageErr }

// In demo mode every visitor shares one account, so the person's own key
// lists answer no enrolled address to a caller who is not an administrator;
// an administrator still sees it. Regression: a key enrolled before demo mode
// was turned on would show its address to every visitor.
func TestDemoModeHidesEnrolledAddresses(t *testing.T) {
	key := &models.AppDeviceKey{ID: "0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11", KeyID: "zT321tSgcB7emUH9k0B0oA", Kind: models.DeviceKeyKindDevice,
		Status: models.DeviceKeyStatusPending, EnrolledVia: models.AppKeyEnrolledIntroduce, EnrolledIP: "127.0.0.2", CreatedAt: fixedTime()}
	keys := &fakeAppKeys{enabled: true, keys: auth.OwnAppKeys{Person: &models.AppPersonKey{KeyID: "PgujdSfBbNNyXngLWCsOgw", EnrolledIP: "127.0.0.2", CreatedAt: fixedTime()},
		Devices: []*models.AppDeviceKey{key}}, pending: []*models.AppDeviceKey{key}}
	deps := appKeyDeps(keys)
	deps.DemoSettings = fakeSettings{demo: true}
	h := NewHandler(deps)
	for _, tc := range []struct {
		token, want string
	}{{memberToken, ""}, {adminToken, "127.0.0.2"}} {
		var list AppKeyList
		rec := do(t, h, http.MethodGet, Prefix+"/auth/keys", "", bearer(tc.token))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: key list status %d: %s", tc.token, rec.Code, rec.Body.String())
		}
		decodeJSON(t, rec.Body, &list)
		if list.PersonKey == nil || list.PersonKey.EnrolledIPAddress != tc.want || len(list.Devices) != 1 || list.Devices[0].EnrolledIPAddress != tc.want {
			t.Fatalf("%s: key list = %+v, want address %q", tc.token, list, tc.want)
		}
		var pending PendingAppKeyList
		rec = do(t, h, http.MethodGet, Prefix+"/auth/keys/pending", "", bearer(tc.token))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: pending list status %d: %s", tc.token, rec.Code, rec.Body.String())
		}
		decodeJSON(t, rec.Body, &pending)
		if len(pending.Items) != 1 || pending.Items[0].EnrolledIPAddress != tc.want {
			t.Fatalf("%s: pending list = %+v, want address %q", tc.token, pending, tc.want)
		}
	}
}
