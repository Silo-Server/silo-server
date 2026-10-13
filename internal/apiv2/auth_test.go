package apiv2

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestLogin(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":"pw"}`, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	want := `{"access_token":"acc","refresh_token":"ref","expires_in":3600,"user":{"id":"1","username":"laura","email":"laura@example.test","role":"user","permissions":["marker_edit"],"download_allowed":true,"password_change_required":false}}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// Wrong credentials: 401 invalid_token, never authentication_required.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":"nope"}`, nil), TypeInvalidToken)
	// A directory sign-in with no account while account creation is off is
	// account_required, not the not_permitted v1 answers.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/login", `{"username":"newcomer","password":"pw"}`, nil), TypeAccountRequired)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/login", `{"username":"off","password":"pw"}`, nil), TypePermissionDenied)
	// A blank password is refused by the schema, naming the member.
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":""}`, nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.password" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":"pw","remember":true}`, nil), TypeValidationFailed)
	// A plugin provider id is composite and can be long; every id discovery
	// advertises must reach the service rather than fail the schema.
	deps := pilotDeps(nil, nil)
	sessions := &fakeSessionService{}
	deps.Sessions = sessions
	longProvider := "plugin:" + strings.Repeat("silo-plugin-auth-enterprise-directory-", 2) + "0123456789abcdef:openid-connect-enterprise-directory"
	if len(longProvider) <= 64 {
		t.Fatalf("provider id %q is not long enough to exercise the bound", longProvider)
	}
	rec = do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":"pw","provider":"`+longProvider+`"}`, nil)
	if rec.Code != 200 || sessions.lastLogin.Provider != longProvider {
		t.Fatalf("%d %s provider=%q", rec.Code, rec.Body.String(), sessions.lastLogin.Provider)
	}
	// The session the login opens records the device headers.
	rec = do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":"pw"}`, map[string]string{
		"User-Agent": "okhttp/4.12.0", "X-Silo-Device-Id": "android-7f3c9a", "X-Silo-Device-Name": "Google Pixel 8 Pro", "X-Silo-Device-Platform": "android",
	})
	if got := sessions.lastLoginDevice; rec.Code != 200 || got.ID != "android-7f3c9a" || got.Name != "Google Pixel 8 Pro" || got.Platform != "android" || sessions.lastLogin.DeviceName != "okhttp/4.12.0" {
		t.Fatalf("%d device=%+v name=%q", rec.Code, got, sessions.lastLogin.DeviceName)
	}
	deps = pilotDeps(nil, nil)
	deps.Sessions = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":"pw"}`, nil), TypeDependencyUnavailable)
}

func TestLoginRateLimited(t *testing.T) {
	deps := pilotDeps(nil, nil)
	var bucket string
	deps.BucketRateLimit = func(b string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bucket = b
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded","message":"Too many requests."}`))
			})
		}
	}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":"pw"}`, nil), TypeRateLimited)
	if bucket != "login" {
		t.Fatalf("bucket = %q", bucket)
	}
}

func TestLogout(t *testing.T) {
	deps := pilotDeps(nil, nil)
	sessions := &fakeSessionService{}
	deps.Sessions = sessions
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/auth/logout", "", bearer(memberToken))
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(sessions.loggedOut) != 1 || sessions.loggedOut[0] != "s1" {
		t.Fatalf("logged out = %v", sessions.loggedOut)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/logout", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/logout", "", bearer(expiredToken)), TypeSessionExpired)
	// An API key is admitted by the gate with no session id; it is refused
	// rather than handed to the store as a revoke of "".
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/logout", "", bearer(apiKeyToken)), TypePermissionDenied)
	if len(sessions.loggedOut) != 1 {
		t.Fatalf("api key reached the session store: logged out = %v", sessions.loggedOut)
	}
	deps.Sessions = &fakeSessionService{err: errStore}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/logout", "", bearer(memberToken)), TypeInternalError)
}

func TestEndImpersonation(t *testing.T) {
	deps := pilotDeps(nil, nil)
	sessions := &fakeSessionService{}
	deps.Sessions = sessions
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/auth/impersonation/end", "", bearer(impersonatedToken))
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(sessions.ended) != 1 || sessions.ended[0] != "s4" {
		t.Fatalf("ended = %v", sessions.ended)
	}
	// A session that is not impersonating anyone: v1 400 becomes 409.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/impersonation/end", "", bearer(memberToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/impersonation/end", "", nil), TypeAuthenticationRequired)
}

func TestCompleteOAuthLogin(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	// A web code redeems only in the browser holding its completion cookie.
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"c0de"}`, nil), TypeInvalidGrant)
	if p.Detail != "This web completion code belongs to another browser." {
		t.Fatalf("detail = %q", p.Detail)
	}
	rec := do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"c0de"}`, completionCookie)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), `{"access_token":"acc","refresh_token":"ref","expires_in":3600,"next":"/me","user":{"id":"1","username":"laura"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// A native code needs its verifier; a web code refuses one.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"n4tive"}`, nil), TypeInvalidGrant)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"c0de","code_verifier":"`+fixtureNativeVerifier+`"}`, completionCookie), TypeInvalidGrant)
	if rec := do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"n4tive","code_verifier":"`+fixtureNativeVerifier+`"}`, nil); rec.Code != 200 {
		t.Fatalf("native redemption: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"nope"}`, nil), TypeInvalidToken)
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":""}`, nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.code" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Whitespace-only passes the schema and is the seam's own refusal.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"  "}`, nil), TypeValidationFailed)
	deps := pilotDeps(nil, nil)
	deps.OAuth = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"c0de"}`, nil), TypeDependencyUnavailable)

	// Redemption already supplies the account: a separate account lookup
	// failure cannot strand the tokens or revoke the new session.
	deps = pilotDeps(nil, nil)
	deps.Accounts = fakeAccounts{err: errors.New("database unavailable")}
	sessions := &fakeSessionService{}
	deps.Sessions = sessions
	if rec := do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"c0de"}`, completionCookie); rec.Code != http.StatusOK {
		t.Fatalf("redemption with account snapshot: %d %s", rec.Code, rec.Body.String())
	}
	if len(sessions.revoked) != 0 {
		t.Fatalf("successful redemption revoked sessions: %v", sessions.revoked)
	}
}
