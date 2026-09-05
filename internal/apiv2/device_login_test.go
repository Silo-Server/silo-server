package apiv2

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestGetDeviceLoginCapability(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/auth/device/capability", "", nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	want := `{"revision":"1","state":"available","remote_playback_handoff":true,"protocol_versions":[2]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	deps := pilotDeps(nil, nil)
	deps.Devices = fakeDevices{configured: false}
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/auth/device/capability", "", nil)
	if rec.Code != 200 || !json.Valid(rec.Body.Bytes()) || !contains(rec.Body.String(), `"state":"not_configured"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestStartDeviceLogin(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodPost, "/api/v2/auth/device/start", `{"device_name":"Living room TV","device_platform":"tvos"}`,
		map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "silo.example.test"})
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	want := `{"device_code":"dev-1","user_code":"ABCD-1234","match_code":"42","verification_uri":"https://silo.example.test/link","verification_uri_complete":"https://silo.example.test/link?code=ABCD-1234","expires_at":"2026-01-02T03:14:05.678Z","expires_in":600,"interval":5,"device_name":"Living room TV","device_platform":"tvos","client_purpose":"device_login","temporary":false}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// An empty body is the v1 default request.
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/start", `{}`, nil)
	if rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// remote_playback without temporary is the seam's rejection, at the member.
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/start", `{"client_purpose":"remote_playback"}`, nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.client_purpose" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// An unknown purpose is refused by the schema before the seam.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/start", `{"client_purpose":"other"}`, nil), TypeValidationFailed)
	deps := pilotDeps(nil, nil)
	deps.Devices = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/device/start", `{}`, nil), TypeDependencyUnavailable)
}

func TestStartDeviceLoginRateLimited(t *testing.T) {
	deps := pilotDeps(nil, nil)
	var buckets []string
	deps.PublicRateLimit = func(bucket string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buckets = append(buckets, bucket)
				w.Header().Set("Retry-After", "30")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded","message":"Too many requests."}`))
			})
		}
	}
	h := newTestHandler(t, deps)
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/start", `{}`, nil), TypeRateLimited)
	if p.Status != 429 {
		t.Fatalf("status = %d", p.Status)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{"device_code":"x"}`, nil), TypeRateLimited)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/auth/device?code=ABCD-1234", "", nil), TypeRateLimited)
	// The capability document has no bucket and is never limited.
	if rec := do(t, h, http.MethodGet, "/api/v2/auth/device/capability", "", nil); rec.Code != 200 {
		t.Fatalf("capability limited: %d", rec.Code)
	}
	if len(buckets) != 3 || buckets[0] != "device_start" || buckets[1] != "device_poll" || buckets[2] != "device_lookup" {
		t.Fatalf("buckets = %v", buckets)
	}
}

func TestGetDeviceLogin(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/auth/device?code=ABCD-1234", "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"status":"pending","user_code":"ABCD-1234","match_code":"42","device_name":"Living room TV","device_platform":"tvos","ip_address_hint":"192.168.1.x","expires_at":"2026-01-02T03:14:05.678Z","client_purpose":"device_login","temporary":false}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// Expired: expires_at is omitted, the codes are blank, the status says so.
	rec = do(t, h, http.MethodGet, "/api/v2/auth/device?token=br-expired", "", nil)
	if rec.Code != 200 || contains(rec.Body.String(), "expires_at") || !contains(rec.Body.String(), `"status":"expired"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/auth/device?token=nope", "", nil), TypeNotFound)
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/auth/device", "", nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.code" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestPollDeviceLogin(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{"device_code":"dev-pending"}`, nil)
	if rec.Code != 200 || rec.Body.String() != `{"status":"pending","poll_after":5,"profile_id":"","profile_token":"","temporary":false}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{"device_code":"dev-approved"}`, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	want := `{"status":"approved","poll_after":5,"tokens":{"access_token":"acc","refresh_token":"ref","expires_in":3600,"user":{"id":"1","username":"laura","email":"laura@example.test","role":"user","permissions":[],"download_allowed":true}},"profile_id":"","profile_token":"","temporary":false}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{"device_code":"dev-handoff"}`, nil)
	if rec.Code != 200 || !contains(rec.Body.String(), `"profile_id":"p-owner","profile_token":"ptok","temporary":true,"session_expires_at":"2026-01-02T05:04:05.678Z"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{"device_code":"nope"}`, nil), TypeNotFound)
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{"device_code":""}`, nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.device_code" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{}`, nil), TypeValidationFailed)
}

func TestDecideDeviceLogin(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{"token":"br-pending"}`, bearer(memberToken))
	if rec.Code != 200 || rec.Body.String() != `{"status":"approved"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/deny", `{"code":"ABCD-1234"}`, bearer(memberToken))
	if rec.Code != 200 || rec.Body.String() != `{"status":"denied"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/approve-handoff", `{"token":"br-remote"}`, owner)
	if rec.Code != 200 || rec.Body.String() != `{"status":"approved"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Each terminal state keeps its v1 status: 410 expired, 409 consumed/denied, 409 purpose mismatch.
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{"token":"br-expired"}`, bearer(memberToken)), TypeDeviceLoginExpired)
	if p.Status != http.StatusGone {
		t.Fatalf("status = %d", p.Status)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{"token":"br-consumed"}`, bearer(memberToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/deny", `{"token":"br-denied"}`, bearer(memberToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{"token":"br-remote"}`, bearer(memberToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve-handoff", `{"token":"br-pending"}`, owner), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{"token":"nope"}`, bearer(memberToken)), TypeNotFound)
	// Neither code: named, not forwarded as a lookup of nothing.
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.code" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestDecideDeviceLoginDenied(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	body := `{"token":"br-pending"}`
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/deny", body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", body, bearer(expiredToken)), TypeSessionExpired)
	// approve-handoff needs a verified profile: no header, and a locked one.
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve-handoff", body, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationProfileHeader {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve-handoff", body, with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	deps := pilotDeps(nil, nil)
	deps.Devices = fakeDevices{configured: true, err: errStore}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/device/approve", body, bearer(memberToken)), TypeInternalError)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
