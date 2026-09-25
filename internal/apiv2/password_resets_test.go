package apiv2

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/passwordreset"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type fakePasswordResets struct {
	caps      passwordreset.Capabilities
	issued    *passwordreset.IssueInput
	issueErr  error
	completed int
	// Self-service reset: whether it is on and deliverable, and the logins
	// requested so far.
	selfEnabled, selfConfigured bool
	requested                   []string
}

func fixturePasswordResets() *fakePasswordResets {
	return &fakePasswordResets{caps: passwordreset.Capabilities{Link: true, Email: true}, selfEnabled: true, selfConfigured: true}
}

func (f *fakePasswordResets) PasswordResetSelfService(context.Context) (bool, bool, error) {
	return f.selfEnabled, f.selfConfigured, nil
}

func (f *fakePasswordResets) RequestPasswordReset(_ context.Context, login string) error {
	switch {
	case !f.selfConfigured:
		return passwordreset.ErrSelfServiceNotConfigured
	case !f.selfEnabled:
		return passwordreset.ErrSelfServiceDisabled
	}
	f.requested = append(f.requested, login)
	return nil
}

func (f *fakePasswordResets) PasswordResetCapabilities(context.Context) passwordreset.Capabilities {
	return f.caps
}

func (f *fakePasswordResets) IssuePasswordReset(_ context.Context, in passwordreset.IssueInput) (*passwordreset.IssueResult, error) {
	f.issued = &in
	if f.issueErr != nil {
		return nil, f.issueErr
	}
	result := &passwordreset.IssueResult{ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}
	if in.Delivery == passwordreset.DeliveryLink {
		result.URL = "https://server.example.invalid/reset-password/synthetic-token"
		return result, nil
	}
	// Account 8 stands for a mail server that did not confirm delivery.
	if in.UserID == 8 {
		return result, errors.New("private SMTP credential")
	}
	result.EmailSent = true
	return result, nil
}

func (f *fakePasswordResets) LookupPasswordReset(_ context.Context, token string) (*passwordreset.LookupResult, error) {
	if token != "live" && token != "sign-in-required" {
		return nil, passwordreset.ErrNotFound
	}
	return &passwordreset.LookupResult{Username: "reset-user", ServerName: "Server", ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func (f *fakePasswordResets) CompletePasswordReset(_ context.Context, token, password, _, _ string) (handlers.PasswordResetCompletionView, error) {
	if token != "live" && token != "sign-in-required" {
		return handlers.PasswordResetCompletionView{}, passwordreset.ErrNotFound
	}
	if err := auth.ValidateNewPassword(password); err != nil {
		return handlers.PasswordResetCompletionView{}, err
	}
	f.completed++
	view := handlers.PasswordResetCompletionView{Username: "reset-user"}
	if token == "sign-in-required" {
		return view, passwordreset.ErrSessionStart
	}
	view.Tokens = &handlers.TokenPairView{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", ExpiresIn: 3600, User: handlers.UserView{ID: 3, Username: "reset-user", Role: models.RoleUser}}
	return view, nil
}

func passwordResetTestHandler(f *fakePasswordResets) http.Handler {
	d := requestDeps(fixtureRequests())
	d.PasswordResets = f
	return NewHandler(d)
}

func TestAdminPasswordResetDelivery(t *testing.T) {
	f := fixturePasswordResets()
	h := passwordResetTestHandler(f)
	path := Prefix + "/admin/users/7/password-reset"

	link := do(t, h, http.MethodPost, path, `{"delivery":"link"}`, actingRequestAdmin)
	if link.Code != http.StatusCreated || !strings.Contains(link.Body.String(), "/reset-password/synthetic-token") || !strings.Contains(link.Body.String(), `"delivery_status":"not_requested"`) {
		t.Fatal(link.Code, link.Body.String())
	}
	if f.issued == nil || f.issued.UserID != 7 || f.issued.IssuedBy == 0 || f.issued.Delivery != passwordreset.DeliveryLink {
		t.Fatalf("issued %+v", f.issued)
	}

	// The emailed link is never disclosed to the administrator.
	sent := do(t, h, http.MethodPost, path, `{"delivery":"email"}`, actingRequestAdmin)
	if sent.Code != http.StatusCreated || !strings.Contains(sent.Body.String(), `"delivery_status":"sent"`) || strings.Contains(sent.Body.String(), "reset_url") {
		t.Fatal(sent.Code, sent.Body.String())
	}
	uncertain := do(t, h, http.MethodPost, Prefix+"/admin/users/8/password-reset", `{"delivery":"email"}`, actingRequestAdmin)
	if uncertain.Code != http.StatusCreated || !strings.Contains(uncertain.Body.String(), `"delivery_status":"failed_or_unknown"`) || strings.Contains(uncertain.Body.String(), "private") {
		t.Fatal(uncertain.Code, uncertain.Body.String())
	}

	requireProblem(t, do(t, h, http.MethodPost, path, `{"delivery":"sms"}`, actingRequestAdmin), TypeValidationFailed)
	for err, want := range map[error]ProblemType{
		auth.ErrPasswordLoginDisabled:    TypeConflict,
		passwordreset.ErrAccountDisabled: TypeConflict,
		passwordreset.ErrNoEmail:         TypeConflict,
		mail.ErrNotConfigured:            TypeCapabilityNotConfigured,
		passwordreset.ErrNoLinkBase:      TypeCapabilityNotConfigured,
		auth.ErrNotFound:                 TypeNotFound,
	} {
		f.issueErr = err
		requireProblem(t, do(t, h, http.MethodPost, path, `{"delivery":"email"}`, actingRequestAdmin), want)
	}
}

func TestAdminPasswordResetCapabilities(t *testing.T) {
	f := fixturePasswordResets()
	f.caps = passwordreset.Capabilities{Link: true}
	h := passwordResetTestHandler(f)
	r := do(t, h, http.MethodGet, Prefix+"/admin/users/capabilities", "", actingRequestAdmin)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"password_reset_link":true`) || !strings.Contains(r.Body.String(), `"password_reset_email":false`) {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestPublicPasswordReset(t *testing.T) {
	f := fixturePasswordResets()
	h := passwordResetTestHandler(f)

	lookup := do(t, h, http.MethodGet, Prefix+"/password-resets/live", "", nil)
	if lookup.Code != http.StatusOK || !strings.Contains(lookup.Body.String(), `"username":"reset-user"`) {
		t.Fatal(lookup.Code, lookup.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/password-resets/spent", "", nil), TypeNotFound)

	done := do(t, h, http.MethodPost, Prefix+"/password-resets/live/complete", `{"password":"password123"}`, nil)
	if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), `"login_status":"signed_in"`) || !strings.Contains(done.Body.String(), "fixture-access") {
		t.Fatal(done.Code, done.Body.String())
	}
	// The reset committed even though sign-in did not: no tokens, and the
	// caller must not replay it.
	signIn := do(t, h, http.MethodPost, Prefix+"/password-resets/sign-in-required/complete", `{"password":"password123"}`, nil)
	if signIn.Code != http.StatusOK || !strings.Contains(signIn.Body.String(), `"login_status":"sign_in_required"`) || strings.Contains(signIn.Body.String(), "tokens") {
		t.Fatal(signIn.Code, signIn.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/password-resets/spent/complete", `{"password":"password123"}`, nil), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/password-resets/live/complete", `{"password":"short"}`, nil), TypeValidationFailed)
	if f.completed != 2 {
		t.Fatalf("completed %d resets, want 2", f.completed)
	}
}

func TestPasswordResetsNotConfigured(t *testing.T) {
	h := NewHandler(requestDeps(fixtureRequests()))
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/password-resets/live", "", nil), TypeCapabilityNotConfigured)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/users/7/password-reset", `{"delivery":"link"}`, actingRequestAdmin), TypeCapabilityNotConfigured)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/password-resets", `{"login":"alice"}`, nil), TypeCapabilityNotConfigured)
	if r := do(t, h, http.MethodGet, Prefix+"/capabilities/password-reset", "", nil); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"state":"not_configured"`) {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestPasswordResetSelfServiceCapability(t *testing.T) {
	for _, tc := range []struct {
		enabled, configured bool
		state               string
	}{
		{true, true, StateAvailable},
		{false, true, StateDisabled},
		{true, false, StateNotConfigured},
		{false, false, StateNotConfigured},
	} {
		f := fixturePasswordResets()
		f.selfEnabled, f.selfConfigured = tc.enabled, tc.configured
		r := do(t, passwordResetTestHandler(f), http.MethodGet, Prefix+"/capabilities/password-reset", "", nil)
		// A public, server-wide document: no per-principal allowed answer.
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"state":"`+tc.state+`"`) || strings.Contains(r.Body.String(), "allowed") || r.Header().Get("ETag") == "" {
			t.Fatal(tc, r.Code, r.Body.String())
		}
	}
}

func TestRequestPasswordResetAnswersAlike(t *testing.T) {
	f := fixturePasswordResets()
	h := passwordResetTestHandler(f)
	path := Prefix + "/password-resets"

	// Whatever the login names, the answer is the same empty 202; the service
	// decides in the background whether anything is sent.
	for _, login := range []string{"alice", "nobody@example.test"} {
		r := do(t, h, http.MethodPost, path, `{"login":"`+login+`"}`, nil)
		if r.Code != http.StatusAccepted || strings.TrimSpace(r.Body.String()) != "" {
			t.Fatal(login, r.Code, r.Body.String())
		}
	}
	if len(f.requested) != 2 || f.requested[0] != "alice" || f.requested[1] != "nobody@example.test" {
		t.Fatalf("requested %q", f.requested)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"login":""}`, nil), TypeValidationFailed)

	f.selfEnabled = false
	requireProblem(t, do(t, h, http.MethodPost, path, `{"login":"alice"}`, nil), TypeCapabilityDisabled)
	f.selfConfigured = false
	requireProblem(t, do(t, h, http.MethodPost, path, `{"login":"alice"}`, nil), TypeCapabilityNotConfigured)
	if len(f.requested) != 2 {
		t.Fatalf("refused requests reached the service: %q", f.requested)
	}
}

func TestRequestPasswordResetRateGate(t *testing.T) {
	f := fixturePasswordResets()
	deps := requestDeps(fixtureRequests())
	deps.PasswordResets = f
	limiter := ratelimit.NewMiddleware(ratelimit.NewMemoryLimiter(), ratelimit.NewMemoryLimiter(), fakeSettings{}, true)
	if err := limiter.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	deps.BucketRateLimit = func(bucket string) func(http.Handler) http.Handler {
		if bucket != bucketPasswordResetRequest {
			return func(h http.Handler) http.Handler { return h }
		}
		return limiter.Handler
	}
	h := NewHandler(deps)
	limited := false
	for range 5 {
		req := httptest.NewRequest(http.MethodPost, Prefix+"/password-resets", strings.NewReader(`{"login":"alice"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(clientip.SetContext(req.Context(), "203.0.113.9"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			requireProblem(t, rec, TypeRateLimited)
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("rate limit missing retry header")
			}
			limited = true
			break
		}
		if rec.Code != http.StatusAccepted {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	if !limited || len(f.requested) >= 5 {
		t.Fatal("reset request limiter did not stop requests", len(f.requested))
	}
}

func TestAdminAccountTemporaryPassword(t *testing.T) {
	f := fixtureAdminAccounts()
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = f
	h := NewHandler(deps)
	path := Prefix + "/admin/users/7"
	anyRevision := with(actingRequestAdmin, "If-Match", "*")

	r := do(t, h, http.MethodPut, path, `{"require_password_change":true}`, anyRevision)
	requireProblem(t, r, TypeValidationFailed)
	if !strings.Contains(r.Body.String(), "body.require_password_change") || f.writes != 0 {
		t.Fatal(f.writes, r.Body.String())
	}
	if r := do(t, h, http.MethodPut, path, `{"password":"temporary-pass","require_password_change":true}`, anyRevision); r.Code != http.StatusNoContent {
		t.Fatal(r.Code, r.Body.String())
	}
	if f.update.Password == nil || !f.update.PasswordChangeRequired {
		t.Fatalf("temporary password not requested: %+v", f.update)
	}
	// A password without the flag is settled, which also clears a pending one.
	if r := do(t, h, http.MethodPut, path, `{"password":"settled-pass"}`, anyRevision); r.Code != http.StatusNoContent {
		t.Fatal(r.Code, r.Body.String())
	}
	if f.update.Password == nil || f.update.PasswordChangeRequired {
		t.Fatalf("settled password marked temporary: %+v", f.update)
	}
	if r := do(t, h, http.MethodPost, Prefix+"/admin/users", `{"username":"new","email":"new@example.test","password":"temporary-pass","role":"user","create_default_profile":false,"require_password_change":true}`, actingRequestAdmin); r.Code != http.StatusCreated {
		t.Fatal(r.Code, r.Body.String())
	}
	if !f.create.User.PasswordChangeRequired {
		t.Fatalf("created password not temporary: %+v", f.create.User)
	}
}

func TestTemporaryPasswordSessionIsConfined(t *testing.T) {
	h := NewHandler(pilotDeps(nil, nil))
	restricted := bearer(temporaryPasswordToken)
	r := do(t, h, http.MethodGet, Prefix+"/profiles", "", restricted)
	requireProblem(t, r, TypePasswordChangeRequired)
	if r.Code != http.StatusForbidden {
		t.Fatal(r.Code)
	}
	if me := do(t, h, http.MethodGet, Prefix+"/account/me", "", restricted); me.Code != http.StatusOK {
		t.Fatal(me.Code, me.Body.String())
	}
}
