package apiv2

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

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
