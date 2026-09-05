package apiv2

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestListAuthProviders(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/auth/providers", "", nil)
	want := `{"items":[{"id":"local","display_name":"Silo account","mode":"credentials","default":true},{"id":"plugin-3","display_name":"Example SSO","mode":"oauth","default":false,"icon_url":"https://plugins.example.test/icon.svg","installation_id":"3"}]}` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	deps := pilotDeps(nil, nil)
	deps.Sessions = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/auth/providers", "", nil), TypeDependencyUnavailable)
}

func TestRefreshSession(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodPost, "/api/v2/auth/refresh", `{"refresh_token":"ref"}`, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	if want := `{"access_token":"acc2","refresh_token":"ref2","expires_in":3600}` + "\n"; rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/refresh", `{"refresh_token":"revoked"}`, nil), TypeSessionExpired)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/refresh", `{"refresh_token":"nope"}`, nil), TypeInvalidToken)
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/refresh", `{}`, nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.refresh_token" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

type loginSessionPage struct {
	Items []struct {
		ID        string `json:"id"`
		CreatedAt string `json:"created_at"`
	} `json:"items"`
	Page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}

func decodeLoginSessions(t *testing.T, rec interface{ String() string }) loginSessionPage {
	t.Helper()
	var page loginSessionPage
	if err := json.Unmarshal([]byte(rec.String()), &page); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.String())
	}
	return page
}

func TestListAndDeleteSessions(t *testing.T) {
	deps := pilotDeps(nil, nil)
	sessions := &fakeSessionService{}
	deps.Sessions = sessions
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/auth/sessions", "", bearer(memberToken))
	want := `{"items":[{"id":"s3","device_name":"Silo/1.0 (tvOS)","ip_address":"127.0.0.1","created_at":"2026-01-02T04:04:05.678Z","expires_at":"2026-02-01T03:04:05.678Z"},{"id":"s2","device_name":"Silo/1.0 (iOS)","ip_address":"127.0.0.2","created_at":"2026-01-02T04:04:05.678Z","expires_at":"2026-02-01T03:04:05.678Z"},{"id":"s1","device_name":"","ip_address":"","created_at":"2026-01-02T03:04:05.678Z","expires_at":"2026-02-01T03:04:05.678Z"}],"page":{"has_more":false}}` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if q := sessions.pageCalls[0]; q.After != nil || q.Limit != 50 {
		t.Fatalf("default query = %+v", q)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/auth/sessions", "", nil), TypeAuthenticationRequired)
	rec = do(t, h, http.MethodDelete, "/api/v2/auth/sessions/s9", "", bearer(memberToken))
	if rec.Code != 204 || rec.Body.Len() != 0 || len(sessions.revoked) != 1 || sessions.revoked[0] != "s9" {
		t.Fatalf("%d %s %v", rec.Code, rec.Body.String(), sessions.revoked)
	}
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/auth/sessions/other", "", bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/auth/sessions/s1", "", nil), TypeAuthenticationRequired)
}

// TestListSessionsCursorWalk pages three live sessions one at a time: the
// third page is the last, the cursor carries the (created_at, id) key of the
// last emitted row (the id tiebreaker separates s3 and s2, which share a
// created_at), and the cursor is bound to the account and the operation.
func TestListSessionsCursorWalk(t *testing.T) {
	deps := pilotDeps(nil, nil)
	sessions := &fakeSessionService{}
	deps.Sessions = sessions
	h := newTestHandler(t, deps)

	var ids []string
	cursor := ""
	for page := 1; page <= 3; page++ {
		path := "/api/v2/auth/sessions?limit=1"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := do(t, h, http.MethodGet, path, "", bearer(memberToken))
		got := decodeLoginSessions(t, rec.Body)
		if rec.Code != 200 || len(got.Items) != 1 {
			t.Fatalf("page %d: %d %s", page, rec.Code, rec.Body.String())
		}
		ids = append(ids, got.Items[0].ID)
		if wantMore := page < 3; got.Page.HasMore != wantMore || (got.Page.NextCursor != "") != wantMore {
			t.Fatalf("page %d: page = %+v", page, got.Page)
		}
		cursor = got.Page.NextCursor
	}
	if strings.Join(ids, ",") != "s3,s2,s1" {
		t.Fatalf("ids = %v", ids)
	}
	if len(sessions.pageCalls) != 3 {
		t.Fatalf("page calls = %d", len(sessions.pageCalls))
	}
	if q := sessions.pageCalls[1]; q.After == nil || q.After.ID != "s3" || !q.After.CreatedAt.Equal(fixedTime().Add(time.Hour)) || q.Limit != 1 {
		t.Fatalf("second query = %+v", q)
	}
	if q := sessions.pageCalls[2]; q.After == nil || q.After.ID != "s2" || !q.After.CreatedAt.Equal(fixedTime().Add(time.Hour)) {
		t.Fatalf("third query = %+v", q)
	}

	// The cursor is bound to the account and to the operation.
	first := decodeLoginSessions(t, do(t, h, http.MethodGet, "/api/v2/auth/sessions?limit=1", "", bearer(memberToken)).Body)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/auth/sessions?limit=1&cursor="+url.QueryEscape(first.Page.NextCursor), "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/auth/sessions?cursor=nonsense", "", bearer(memberToken)), TypeInvalidCursor)
	// v1's offset is not part of v2 pagination; limit is bounded.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/auth/sessions?offset=50", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/auth/sessions?limit=201", "", bearer(memberToken)), TypeValidationFailed)
}

func TestSetupServer(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	body := `{"username":"admin","email":"admin@example.test","password":"pw","create_default_profile":true}`
	rec := do(t, h, http.MethodPost, "/api/v2/auth/setup", body, nil)
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"user":{"id":"1","username":"admin"`) || !strings.Contains(rec.Body.String(), `"access_token":"acc"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/setup", `{"username":"admin","password":"pw"}`, nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.email" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	deps := pilotDeps(nil, nil)
	deps.Sessions = &fakeSessionService{setupDone: true}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/setup", body, nil), TypeConflict)
}

func TestSignup(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/auth/signup", "", nil)
	if rec.Code != 200 || rec.Body.String() != `{"enabled":true}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	body := `{"username":"alice","email":"alice@example.test","password":"pw","invite_code":"WELCOME-2026"}`
	rec = do(t, h, http.MethodPost, "/api/v2/auth/signup", body, nil)
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"username":"alice"`) {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/signup", strings.Replace(body, "WELCOME-2026", "USED", 1), nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.invite_code" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/signup", strings.Replace(body, "alice", "laura", 1), nil), TypeConflict)
	deps := pilotDeps(nil, nil)
	deps.Sessions = &fakeSessionService{}
	off := newTestHandler(t, deps)
	requireProblem(t, do(t, off, http.MethodPost, "/api/v2/auth/signup", body, nil), TypePermissionDenied)
	if rec := do(t, off, http.MethodGet, "/api/v2/auth/signup", "", nil); rec.Body.String() != `{"enabled":false}`+"\n" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestLaunchPlugin(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodPost, "/api/v2/auth/plugin-launch", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 || rec.Body.String() != `{"expires_in":300}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	for _, want := range []string{"silo_plugin_access=plugin-s1-p-owner", "Path=/api/v2/plugins", "Max-Age=300", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(cookie, want) {
			t.Fatalf("cookie %q lacks %q", cookie, want)
		}
	}
	if strings.Contains(cookie, "Secure") {
		t.Fatalf("cookie %q is Secure on a plain request", cookie)
	}
	rec = do(t, h, http.MethodPost, "/api/v2/auth/plugin-launch", "", with(bearer(memberToken), "X-Forwarded-Proto", "https"))
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "Secure") {
		t.Fatalf("cookie %q is not Secure behind TLS", rec.Header().Get("Set-Cookie"))
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/plugin-launch", "", nil), TypeAuthenticationRequired)
	// The declared profile goes through viewer access as on v1: a PIN-locked
	// profile without proof and an unknown profile never reach the token.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/plugin-launch", "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/plugin-launch", "", with(bearer(memberToken), "X-Profile-Id", "p-missing")), TypeNotFound)
	rec = do(t, h, http.MethodPost, "/api/v2/auth/plugin-launch", "", with(with(bearer(memberToken), "X-Profile-Id", "p-locked"), "X-Profile-Token", "pvt-ok"))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Set-Cookie"), "silo_plugin_access=plugin-s1-p-locked") {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Set-Cookie"))
	}
}

func TestOAuthHandshakes(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodPost, "/api/v2/auth/oauth/3/init?next=/me", "", nil)
	if rec.Code != 302 || rec.Header().Get("Location") != "https://sso.example.test/authorize?redirect_uri=https%3A%2F%2Fsilo.example.test%2Fapi%2Fv2%2Fauth%2Foauth%2F3%2Fcallback&next=%2Fme" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/v2/auth/oauth/9/init", "", nil); rec.Code != 502 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/v2/auth/oauth/x/init", "", nil); rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/auth/oauth/3/callback?state=st&code=pc", "", nil)
	if rec.Code != 302 || rec.Header().Get("Location") != "https://silo.example.test/login/oauth-complete?code=c0de" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Location"), rec.Header().Get("Cache-Control"))
	}
	rec = do(t, h, http.MethodGet, "/api/v2/auth/oauth/3/callback?state=bad&code=pc", "", nil)
	if rec.Code != 302 || rec.Header().Get("Location") != "/login?error=oauth_failed&reason=state_invalid" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do(t, h, http.MethodGet, "/api/v2/auth/oauth/3/callback?state=st", "", nil); rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// The path answers 405 for a method the registry does not declare.
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/auth/oauth/3/init", "", nil), TypeMethodNotAllowed)
	deps := pilotDeps(nil, nil)
	deps.OAuth = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/oauth/3/init", "", nil), TypeDependencyUnavailable)
}

func TestOnboarding(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	hdr := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	rec := do(t, h, http.MethodGet, "/api/v2/onboarding/flow?surface=tv", "", hdr)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tour_id":"core-2026-07"`) || !strings.Contains(rec.Body.String(), `"links":[]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/onboarding/flow?surface=watch", "", hdr), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/onboarding/flow", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/onboarding/flow", "", nil), TypeAuthenticationRequired)
	rec = do(t, h, http.MethodGet, "/api/v2/onboarding/state", "", hdr)
	if rec.Code != 200 || rec.Body.String() != `{"tour_id":"core-2026-07","last_step":"playback-quality","done":false}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/onboarding/progress", `{"last_step":"apps","completed":true}`, hdr)
	if rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/onboarding/progress", `{"tour_id":"old"}`, hdr), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/onboarding/progress", `{"step":"apps"}`, hdr), TypeValidationFailed)
	deps := pilotDeps(nil, nil)
	deps.Onboarding = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/onboarding/state", "", hdr), TypeDependencyUnavailable)
}

func TestGetPolicyCapability(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/policy/capability", "", bearer(memberToken))
	want := `{"revision":"1","state":"available","editor_available":true,"decision_types":["download","playback"],"generation":3,"degraded":false,"degraded_domains":[],"eval_timeouts":0}` + "\n"
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" || rec.Body.String() != want {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/policy/capability", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/policy/capability", "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	deps := pilotDeps(nil, nil)
	deps.Policy = nil
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/policy/capability", "", bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"not_configured"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestListUserLibraries(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/user/libraries", "", bearer(memberToken))
	want := `{"items":[{"id":"1","name":"Movies","type":"movies","sort_order":0,"poster_url":"https://s3.example.test/silo/posters/1.jpg"},{"id":"3","name":"Kids","type":"series","sort_order":2}]}` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/v2/user/libraries", "", with(bearer(memberToken), "X-Profile-Id", "p-owner")); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/user/libraries", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/user/libraries?offset=1", "", bearer(memberToken)), TypeValidationFailed)
	deps := pilotDeps(nil, nil)
	deps.UserLibraries = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/user/libraries", "", bearer(memberToken)), TypeDependencyUnavailable)
}
