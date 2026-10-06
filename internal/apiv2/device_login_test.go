package apiv2

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestDeviceApprovalsRevokedSession(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.Devices = fakeDevices{
		configured: true,
		err: (&handlers.APIError{
			Status: http.StatusUnauthorized, Code: "unauthorized", Message: "Login session is no longer valid",
		}).WithCause(auth.ErrSessionRevoked),
	}
	h := newTestHandler(t, deps)
	for _, route := range []string{"approve", "approve-handoff"} {
		t.Run(route, func(t *testing.T) {
			headers := with(bearer(memberToken), "X-Profile-Id", "p-owner")
			rec := do(t, h, http.MethodPost, "/api/v2/auth/device/"+route, `{"token":"br-pending"}`, headers)
			problem := requireProblem(t, rec, TypeAuthenticationRequired)
			if rec.Code != http.StatusUnauthorized || problem.Status != http.StatusUnauthorized {
				t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGetDeviceLoginCapability(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/auth/device/capability", "", nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != cachePrivateNoCache {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	want := `{"revision":"1","state":"available","remote_playback_handoff":true,"protocol_versions":[2],"cancel":true,"opened_signal":true}` + "\n"
	if !capabilityBodyMatches(t, rec.Body.Bytes(), want) {
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
	want := `{"device_code":"dev-1","user_code":"4821-7730","match_code":"warm pony","verification_uri":"https://silo.example.test/activate","verification_uri_complete":"https://silo.example.test/activate?code=48217730","expires_at":"2026-01-02T03:19:05.678Z","expires_in":900,"interval":5,"device_name":"Living room TV","device_platform":"tvos","client_purpose":"device_login","temporary":false}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// A configured public URL wins over the address the device used, so a
	// phone off the TV's network can still open the link.
	pub := pilotDeps(nil, nil)
	publicURL := "https://media.example.test/"
	pub.ServerConnections.PublicURL = func() string { return publicURL }
	public := newTestHandler(t, pub)
	rec = do(t, public, http.MethodPost, "/api/v2/auth/device/start", `{}`,
		map[string]string{"X-Forwarded-Proto": "http", "X-Forwarded-Host": "192.168.1.20:8090"})
	if rec.Code != 201 || !contains(rec.Body.String(), `"verification_uri_complete":"https://media.example.test/activate?code=48217730"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// A public URL with a path keeps it.
	publicURL = "https://Example.test/silo/"
	rec = do(t, public, http.MethodPost, "/api/v2/auth/device/start", `{}`, nil)
	if rec.Code != 201 || !contains(rec.Body.String(), `"verification_uri":"https://example.test/silo/activate"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// An unusable public URL falls back to the device's origin.
	publicURL = "not a url"
	rec = do(t, public, http.MethodPost, "/api/v2/auth/device/start", `{}`,
		map[string]string{"X-Forwarded-Proto": "http", "X-Forwarded-Host": "192.168.1.20:8090"})
	if rec.Code != 201 || !contains(rec.Body.String(), `"verification_uri":"http://192.168.1.20:8090/activate"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
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
	deps.BucketRateLimit = func(bucket string) func(http.Handler) http.Handler {
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
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/auth/device?code=4821-7730", "", nil), TypeRateLimited)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/cancel", `{"device_code":"x"}`, nil), TypeRateLimited)
	// The capability document has no bucket and is never limited.
	if rec := do(t, h, http.MethodGet, "/api/v2/auth/device/capability", "", nil); rec.Code != 200 {
		t.Fatalf("capability limited: %d", rec.Code)
	}
	if len(buckets) != 4 || buckets[0] != "device_start" || buckets[1] != "device_poll" || buckets[2] != "device_lookup" || buckets[3] != "device_poll" {
		t.Fatalf("buckets = %v", buckets)
	}
}

// TestDecideDeviceLoginRateLimited pins that the three decisions, which take
// a user code, spend the lookup's device_lookup guessing budget in place of
// the generic authenticated limiter.
func TestDecideDeviceLoginRateLimited(t *testing.T) {
	deps := pilotDeps(nil, nil)
	var buckets []string
	deps.BucketRateLimit = func(bucket string) func(http.Handler) http.Handler {
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				buckets = append(buckets, bucket)
				w.Header().Set("Retry-After", "30")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded","message":"Too many requests."}`))
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
	body := `{"code":"4821-7730"}`
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", body, bearer(memberToken)), TypeRateLimited)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/deny", body, bearer(memberToken)), TypeRateLimited)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve-handoff", body, with(bearer(memberToken), "X-Profile-Id", "p-owner")), TypeRateLimited)
	if len(buckets) != 3 || buckets[0] != "device_lookup" || buckets[1] != "device_lookup" || buckets[2] != "device_lookup" {
		t.Fatalf("buckets = %v", buckets)
	}
	if generic != 0 {
		t.Fatalf("generic limiter charged %d times alongside the bucket", generic)
	}
}

func TestGetDeviceLogin(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/auth/device?code=4821-7730", "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"status":"pending","user_code":"4821-7730","match_code":"warm pony","device_name":"Living room TV","device_platform":"tvos","ip_address_hint":"192.168.1.x","expires_at":"2026-01-02T03:14:05.678Z","requested_at":"2026-01-02T03:04:05.678Z","client_purpose":"device_login","temporary":false,"server_name":"Silo"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// With an identity service the approver also learns which deployment
	// this is, for app links that must reach the same server.
	ided := pilotDeps(nil, nil)
	ided.ServerIdentity = fakeServerIdentity{id: "3f2a9d5e-6b1c-4c7e-9a0d-2f4b8c1e7a35"}
	rec = do(t, newTestHandler(t, ided), http.MethodGet, "/api/v2/auth/device?code=48217730", "", nil)
	if rec.Code != 200 || !contains(rec.Body.String(), `"server_id":"3f2a9d5e-6b1c-4c7e-9a0d-2f4b8c1e7a35"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// A request the device withdrew reads as canceled.
	rec = do(t, h, http.MethodGet, "/api/v2/auth/device?token=br-canceled", "", nil)
	if rec.Code != 200 || !contains(rec.Body.String(), `"status":"canceled"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
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
	if rec.Code != 200 || rec.Body.String() != `{"status":"pending","poll_after":5,"opened":false,"expires_at":"2026-01-02T03:14:05.678Z","profile_id":"","profile_token":"","temporary":false}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Once an approver has the page open the device keeps its code.
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{"device_code":"dev-opened"}`, nil)
	if rec.Code != 200 || !contains(rec.Body.String(), `"status":"pending","poll_after":5,"opened":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{"device_code":"dev-canceled"}`, nil)
	if rec.Code != 200 || !contains(rec.Body.String(), `"status":"canceled"`) || contains(rec.Body.String(), "expires_at") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/poll", `{"device_code":"dev-approved"}`, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	want := `{"status":"approved","poll_after":5,"opened":false,"tokens":{"access_token":"acc","refresh_token":"ref","expires_in":3600,"user":{"id":"1","username":"laura","email":"laura@example.test","role":"user","permissions":[],"download_allowed":true,"password_change_required":false}},"profile_id":"","profile_token":"","temporary":false}` + "\n"
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
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/deny", `{"code":"4821-7730"}`, bearer(memberToken))
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
	// The device withdrew it: neither decision applies.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{"token":"br-canceled"}`, bearer(memberToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/deny", `{"token":"br-canceled"}`, bearer(memberToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{"token":"br-remote"}`, bearer(memberToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve-handoff", `{"token":"br-pending"}`, owner), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{"token":"nope"}`, bearer(memberToken)), TypeNotFound)
	// Neither code: named, not forwarded as a lookup of nothing.
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.code" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Only a login session decides: an approval gives the device a session,
	// which an API key or an impersonation session must not mint.
	for _, hdr := range []map[string]string{with(bearer(apiKeyToken), "X-Profile-Id", "p-primary"), bearer(impersonatedToken)} {
		requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve", `{"token":"br-pending"}`, hdr), TypePermissionDenied)
		requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/deny", `{"token":"br-pending"}`, hdr), TypePermissionDenied)
		requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/approve-handoff", `{"token":"br-remote"}`, with(hdr, "X-Profile-Id", "p-owner")), TypePermissionDenied)
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

func TestCancelDeviceLogin(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	// No bearer: the device code is the credential.
	rec := do(t, h, http.MethodPost, "/api/v2/auth/device/cancel", `{"device_code":"dev-pending"}`, nil)
	if rec.Code != 200 || rec.Body.String() != `{"status":"canceled"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Approved but not collected: withdrawn too, so the tokens are never issued.
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/cancel", `{"device_code":"dev-approved"}`, nil)
	if rec.Code != 200 || rec.Body.String() != `{"status":"canceled"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Repeating converges; a finished request keeps its state.
	rec = do(t, h, http.MethodPost, "/api/v2/auth/device/cancel", `{"device_code":"dev-canceled"}`, nil)
	if rec.Code != 200 || rec.Body.String() != `{"status":"canceled"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/cancel", `{"device_code":"nope"}`, nil), TypeNotFound)
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/auth/device/cancel", `{"device_code":""}`, nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.device_code" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	deps := pilotDeps(nil, nil)
	deps.Devices = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/auth/device/cancel", `{"device_code":"dev-pending"}`, nil), TypeDependencyUnavailable)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
