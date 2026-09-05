package apiv2

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func TestGetCurrentUser(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/account/me", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"id":"1","username":"laura","email":"laura@example.test","role":"user","permissions":["marker_edit"],"download_allowed":true}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// No profile is involved: a declared header is ignored, not judged.
	rec = do(t, h, http.MethodGet, "/api/v2/account/me", "", with(bearer(memberToken), "X-Profile-Id", "p-unknown"))
	if rec.Code != 200 {
		t.Fatalf("profile header judged on an account operation: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGetCurrentUserImpersonation(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.Accounts = fakeAccounts{users: map[int]handlers.UserView{
		1: {ID: 1, Username: "laura", Role: "user", Permissions: nil,
			Impersonation: &handlers.ImpersonationView{Active: true, ImpersonatorUserID: 2, ImpersonatorUsername: "ada"}},
	}}
	rec := do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/account/me", "", bearer(memberToken))
	var body struct {
		Permissions   []string `json:"permissions"`
		Impersonation *struct {
			Active bool   `json:"active"`
			ID     string `json:"impersonator_user_id"`
			Name   string `json:"impersonator_username"`
		} `json:"impersonation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if body.Permissions == nil || body.Impersonation == nil || !body.Impersonation.Active || body.Impersonation.ID != "2" || body.Impersonation.Name != "ada" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestGetCurrentUserDenied(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/account/me", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/account/me", "", bearer(expiredToken)), TypeSessionExpired)
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/account/me?fields=id", "", bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.fields" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	deps := pilotDeps(nil, nil)
	deps.Accounts = fakeAccounts{err: errStore}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/account/me", "", bearer(memberToken)), TypeInternalError)
}

func TestGetAccountPasswordCapability(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	// The admin with no profile declared may change the password.
	rec := do(t, h, http.MethodGet, "/api/v2/account/password/capability", "", bearer(adminToken))
	want := `{"revision":"1","state":"available","allowed":true,"requires_current_password":true,"minimum_password_length":8,"maximum_password_bytes":72}` + "\n"
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" || rec.Body.String() != want {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	// A member without a profile, or on a secondary profile, is answered
	// allowed=false rather than refused.
	for _, hdr := range []map[string]string{bearer(memberToken), with(bearer(memberToken), "X-Profile-Id", "p-owner"), with(bearer(apiKeyToken), "X-Profile-Id", "p-primary")} {
		rec = do(t, h, http.MethodGet, "/api/v2/account/password/capability", "", hdr)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"allowed":false`) {
			t.Fatalf("%v: %d %s", hdr, rec.Code, rec.Body.String())
		}
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/account/password/capability", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/account/password/capability", "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/account/password/capability", "", with(bearer(memberToken), "X-Profile-Id", "p-missing")), TypeNotFound)
	deps := pilotDeps(nil, nil)
	deps.Accounts = fakeAccounts{err: errStore}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/account/password/capability", "", bearer(adminToken)), TypeInternalError)
}

func TestChangePassword(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	const path = "/api/v2/account/password"
	primary := with(bearer(adminToken), "X-Profile-Id", "p-primary")
	rec := do(t, h, http.MethodPost, path, `{"current_password":"pw","new_password":"margin fossil quench"}`, primary)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// The admin with no profile declared keeps its authority, as on v1.
	if rec = do(t, h, http.MethodPost, path, `{"current_password":"pw","new_password":"margin fossil quench"}`, bearer(adminToken)); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// A missing or blank member is refused by the schema before authority is
	// judged.
	p := requireProblem(t, do(t, h, http.MethodPost, path, `{"new_password":"margin fossil quench"}`, primary), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.current_password" || p.Errors[0].Code != codeRequired {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, path, `{"current_password":"pw","new_password":""}`, primary), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.new_password" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// The wrong current password and a rejected new one are validation
	// problems at the member (v1: 400 invalid_current_password, weak_password,
	// password_too_long); the value is never echoed.
	p = requireProblem(t, do(t, h, http.MethodPost, path, `{"current_password":"nope","new_password":"margin fossil quench"}`, primary), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.current_password" || p.Errors[0].Code != codeInvalid || strings.Contains(rec.Body.String(), "nope") {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, path, `{"current_password":"pw","new_password":"short"}`, primary), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.new_password" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, path, `{"current_password":"pw","new_password":"`+strings.Repeat("x", 73)+`"}`, primary), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.new_password" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Not the primary profile: a member on its own profile, an admin on a
	// secondary profile, an API key, an impersonated session.
	body := `{"current_password":"pw","new_password":"margin fossil quench"}`
	for _, hdr := range []map[string]string{
		with(bearer(memberToken), "X-Profile-Id", "p-owner"), bearer(memberToken),
		with(bearer(adminToken), "X-Profile-Id", "p-owner"), with(bearer(apiKeyToken), "X-Profile-Id", "p-primary"), bearer(impersonatedToken),
	} {
		requireProblem(t, do(t, h, http.MethodPost, path, body, hdr), TypePermissionDenied)
	}
	// An account without local password sign-in is a conflict (v1: 409
	// password_login_disabled).
	requireProblem(t, do(t, h, http.MethodPost, path, `{"current_password":"pw","new_password":"oauth-only"}`, primary), TypeConflict)
	// Class denials: no credential, an expired session, a locked or unknown
	// declared profile.
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, body, bearer(expiredToken)), TypeSessionExpired)
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "X-Profile-Id", "p-primary-locked")), TypeProfileVerificationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "X-Profile-Id", "p-missing")), TypeNotFound)
	// The service itself failing is an internal error with no detail leaked.
	deps := pilotDeps(nil, nil)
	deps.Accounts = fakeAccounts{err: errStore}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, body, primary), TypeInternalError)
}

// TestChangePasswordRateLimitBucket pins that changePassword spends v1's
// dedicated password_change budget in place of the generic authenticated
// limiter: a bucket limiter that refuses answers 429 rate_limited with the
// limiter's Retry-After, the generic limiter is not also charged, and the
// other authenticated operations keep running the generic limiter only.
func TestChangePasswordRateLimitBucket(t *testing.T) {
	deps := pilotDeps(nil, nil)
	var buckets []string
	deps.BucketRateLimit = func(bucket string) func(http.Handler) http.Handler {
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				buckets = append(buckets, bucket)
				w.Header().Set("Retry-After", "12")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded","message":"Too many requests.","retry_after":12}`))
			})
		}
	}
	generic := 0
	deps.RateLimit = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			generic++
			next.ServeHTTP(w, r)
		})
	}
	h := newTestHandler(t, deps)
	primary := with(bearer(adminToken), "X-Profile-Id", "p-primary")
	body := `{"current_password":"pw","new_password":"margin fossil quench"}`
	rec := do(t, h, http.MethodPost, "/api/v2/account/password", body, primary)
	requireProblem(t, rec, TypeRateLimited)
	if rec.Header().Get("Retry-After") != "12" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if len(buckets) != 1 || buckets[0] != "password_change" {
		t.Fatalf("buckets = %v", buckets)
	}
	if generic != 0 {
		t.Fatalf("generic limiter charged %d times alongside the bucket", generic)
	}
	// The sibling authenticated operations are unaffected: generic limiter
	// only, no bucket.
	for _, path := range []string{"/api/v2/account/me", "/api/v2/account/password/capability"} {
		if rec := do(t, h, http.MethodGet, path, "", primary); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if generic != 2 || len(buckets) != 1 {
		t.Fatalf("generic = %d, buckets = %v", generic, buckets)
	}
	// Without a wired bucket limiter the operation falls back to the generic
	// limiter rather than running unlimited.
	deps.BucketRateLimit = nil
	generic = 0
	if rec := do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/account/password", body, primary); rec.Code != http.StatusNoContent {
		t.Fatalf("fallback: %d %s", rec.Code, rec.Body.String())
	}
	if generic != 1 {
		t.Fatalf("fallback generic = %d", generic)
	}
}
