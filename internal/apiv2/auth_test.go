package apiv2

import (
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
	want := `{"access_token":"acc","refresh_token":"ref","expires_in":3600,"user":{"id":"1","username":"laura","email":"laura@example.test","role":"user","permissions":["marker_edit"],"download_allowed":true}}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// Wrong credentials: 401 invalid_token, never authentication_required.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":"nope"}`, nil), TypeInvalidToken)
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
	deps = pilotDeps(nil, nil)
	deps.Sessions = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/login", `{"username":"laura","password":"pw"}`, nil), TypeDependencyUnavailable)
}

func TestLoginRateLimited(t *testing.T) {
	deps := pilotDeps(nil, nil)
	var bucket string
	deps.PublicRateLimit = func(b string) func(http.Handler) http.Handler {
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
	rec := do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"c0de"}`, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"access_token":"acc","refresh_token":"ref","expires_in":3600,"next":"/me"}`+"\n" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"nope"}`, nil), TypeInvalidToken)
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":""}`, nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.code" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Whitespace-only passes the schema and is the seam's own refusal.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"  "}`, nil), TypeValidationFailed)
	deps := pilotDeps(nil, nil)
	deps.OAuth = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/oauth/complete", `{"code":"c0de"}`, nil), TypeDependencyUnavailable)
}
