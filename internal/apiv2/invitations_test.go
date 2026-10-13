package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/invitations"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type fakeInvitations struct {
	profile                           bool
	row                               models.Invitation
	send                              *invitations.SendInput
	writes                            int
	lookupErr, errorAccept, errorSend error
	after                             *invitations.PageKey
	acceptEmail                       string
	emailDelivery                     bool
	resendDelivery                    *invitations.Delivery
}

func fixtureInvitations() *fakeInvitations {
	return &fakeInvitations{profile: true, row: models.Invitation{ID: 7, Email: "invitee@example.invalid", Role: models.RoleUser, TokenHash: "private-token-hash", CreateProfile: true, ShowTour: true, InvitedBy: 2, InvitedByName: "Admin", CreatedAt: fixedTime(), ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}}
}
func (f *fakeInvitations) SupportsDefaultProfile() bool { return f.profile }
func (f *fakeInvitations) EmailDeliveryAvailable(context.Context) bool {
	return f.emailDelivery
}
func (f *fakeInvitations) Lookup(_ context.Context, token string) (*invitations.LookupResult, error) {
	if token == "expired" || token == "revoked" || token == "consumed" {
		return nil, invitations.ErrNotFound
	}
	return &invitations.LookupResult{Email: f.row.Email, EmailRequired: f.row.Email == "", Note: f.row.Note, InviterName: f.row.InvitedByName, ServerName: "Server", ExpiresAt: f.row.ExpiresAt, ShowTour: true, CreateProfile: f.row.CreateProfile}, f.lookupErr
}
func (f *fakeInvitations) AcceptInvitation(_ context.Context, token, email, _, _, _ string) (handlers.InvitationAcceptanceView, error) {
	f.writes++
	f.acceptEmail = email
	v := handlers.InvitationAcceptanceView{Username: f.row.Email, Tokens: &handlers.TokenPairView{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", ExpiresIn: 3600, User: handlers.UserView{ID: 3, Username: f.row.Email, Role: models.RoleUser}}}
	if token == "sign-in-required" {
		return v, invitations.ErrSessionStart
	}
	return v, f.errorAccept
}
func (f *fakeInvitations) GetByID(context.Context, int64) (*models.Invitation, error) {
	r := f.row
	return &r, nil
}
func (f *fakeInvitations) ListPage(_ context.Context, after *invitations.PageKey, _ int) ([]*models.Invitation, bool, error) {
	f.after = after
	r := f.row
	if after != nil {
		r.ID = 6
	}
	return []*models.Invitation{&r}, after == nil, nil
}
func (f *fakeInvitations) Send(_ context.Context, in invitations.SendInput) (*invitations.SendResult, error) {
	f.send = &in
	f.writes++
	if in.Delivery == invitations.DeliveryEmail && !f.emailDelivery {
		return nil, invitations.ErrEmailUnavailable
	}
	r := f.row
	r.CreateProfile = in.CreateProfile
	r.ShowTour = in.ShowTour
	r.LibraryIDs = in.LibraryIDs
	r.Email = in.Email
	// Mirror the service: an address is emailed when a sender is configured;
	// otherwise, or for delivery=link, the link is delivered manually.
	emailed := in.Email != "" && f.emailDelivery
	r.Delivery = models.InvitationDeliveryLink
	if emailed {
		r.Delivery = models.InvitationDeliveryEmailSent
	}
	result := &invitations.SendResult{Invitation: &r, ClaimURL: "https://server.example.invalid/invite/synthetic-token", EmailSent: emailed, Delivery: in.Delivery}
	if in.Note == "smtp-failed" {
		return result, errors.New("private SMTP credential")
	}
	return result, f.errorSend
}
func (f *fakeInvitations) Resend(_ context.Context, _, _ int64, delivery invitations.Delivery) (*invitations.SendResult, error) {
	f.resendDelivery = &delivery
	if f.row.Email == "" && delivery == invitations.DeliveryEmail {
		return nil, invitations.ErrNoAddress
	}
	f.writes++
	r := f.row
	r.ID = 8
	return &invitations.SendResult{Invitation: &r, ClaimURL: "https://server.example.invalid/invite/replacement-token", Delivery: delivery}, f.errorSend
}
func (f *fakeInvitations) Revoke(context.Context, int64) error { f.writes++; return nil }
func invitationTestHandler(f *fakeInvitations) http.Handler {
	d := requestDeps(fixtureRequests())
	d.Invitations = f
	return NewHandler(d)
}
func TestInvitationAcceptanceAndDelivery(t *testing.T) {
	f := fixtureInvitations()
	h := invitationTestHandler(f)
	for _, token := range []string{"pending", "sign-in-required"} {
		r := do(t, h, http.MethodPost, Prefix+"/invitations/"+token+"/accept", `{"password":"password123"}`, nil)
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body.String())
		}
		if token == "pending" {
			if !strings.Contains(r.Body.String(), `"login_status":"signed_in"`) || !strings.Contains(r.Body.String(), "fixture-access") {
				t.Fatal(r.Body.String())
			}
		} else if !strings.Contains(r.Body.String(), `"login_status":"sign_in_required"`) || strings.Contains(r.Body.String(), "tokens") {
			t.Fatal(r.Body.String())
		}
	}
	for _, note := range []string{"", "smtp-failed"} {
		r := do(t, h, http.MethodPost, Prefix+"/admin/invitations", `{"email":"invitee@example.invalid","note":"`+note+`"}`, actingRequestAdmin)
		if r.Code != 201 || r.Header().Get("Location") != Prefix+"/admin/invitations/7" || !strings.Contains(r.Body.String(), "synthetic-token") || strings.Contains(r.Body.String(), "private") {
			t.Fatal(r.Code, r.Body.String())
		}
		if note != "" && !strings.Contains(r.Body.String(), "failed_or_unknown") {
			t.Fatal(r.Body.String())
		}
	}
	resent := do(t, h, http.MethodPost, Prefix+"/admin/invitations/7/resend", "", actingRequestAdmin)
	if resent.Code != 201 || !strings.Contains(resent.Body.String(), "not_configured") {
		t.Fatal(resent.Code, resent.Body.String())
	}
	revoked := do(t, h, http.MethodDelete, Prefix+"/admin/invitations/7", "", actingRequestAdmin)
	if revoked.Code != 204 || revoked.Body.Len() != 0 {
		t.Fatal(revoked.Code, revoked.Body.String())
	}
}
func TestInvitationValidationAndCapability(t *testing.T) {
	f := fixtureInvitations()
	h := invitationTestHandler(f)
	path := Prefix + "/admin/invitations"
	for _, body := range []string{`{"email":"a@example.invalid","create_profile":null}`, `{"email":"a@example.invalid","show_tour":null}`, `{"email":"a@example.invalid","library_ids":null}`, `{"email":"a@example.invalid","access_group_id":"9999999999999999999999"}`, `{"email":"a@example.invalid","library_ids":["9999999999999999999999"]}`, `{"email":"someone@intranet"}`} {
		requireProblem(t, do(t, h, http.MethodPost, path, body, actingRequestAdmin), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, http.MethodGet, path+"/9999999999999999999999", "", actingRequestAdmin), TypeValidationFailed)
	if f.writes != 0 {
		t.Fatal("invalid request reached service")
	}
	r := do(t, h, http.MethodPost, path, `{"email":"a@example.invalid"}`, actingRequestAdmin)
	if r.Code != 201 || f.send == nil || !f.send.CreateProfile || !f.send.ShowTour || f.send.LibraryIDs != nil {
		t.Fatal(r.Code, r.Body.String(), f.send)
	}
	r = do(t, h, http.MethodPost, path, `{"email":"a@example.invalid","create_profile":false,"show_tour":false,"library_ids":[]}`, actingRequestAdmin)
	if r.Code != 201 || f.send.CreateProfile || f.send.ShowTour || f.send.LibraryIDs == nil {
		t.Fatal(r.Code, r.Body.String(), f.send)
	}
	f.profile = false
	before := f.writes
	caps := do(t, h, http.MethodGet, Prefix+"/invitations/capabilities", "", nil)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"default_profile":false`) {
		t.Fatal(caps.Code, caps.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"email":"a@example.invalid"}`, actingRequestAdmin), TypeCapabilityUnsupported)
	requireProblem(t, do(t, h, http.MethodPost, path+"/7/resend", "", actingRequestAdmin), TypeCapabilityUnsupported)
	if f.writes != before {
		t.Fatal("unsupported profile reached effects")
	}
	lookup := do(t, h, http.MethodGet, Prefix+"/invitations/pending", "", nil)
	if lookup.Code != 200 || !strings.Contains(lookup.Body.String(), `"acceptance_available":false`) {
		t.Fatal(lookup.Code, lookup.Body.String())
	}
	f.errorAccept = auth.ErrTransactionalProfileUnavailable
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/invitations/pending/accept", `{"password":"password123"}`, nil), TypeCapabilityUnsupported)
}
func TestInvitationLookupAndCursor(t *testing.T) {
	f := fixtureInvitations()
	h := invitationTestHandler(f)
	for _, token := range []string{"expired", "revoked", "consumed"} {
		requireProblem(t, do(t, h, http.MethodGet, Prefix+"/invitations/"+token, "", nil), TypeNotFound)
	}
	f.lookupErr = errors.New("private DB connection")
	r := do(t, h, http.MethodGet, Prefix+"/invitations/pending", "", nil)
	requireProblem(t, r, TypeInternalError)
	if strings.Contains(r.Body.String(), "private") {
		t.Fatal(r.Body.String())
	}
	path := Prefix + "/admin/invitations"
	first := do(t, h, http.MethodGet, path+"?limit=1", "", actingRequestAdmin)
	var page Collection[AdminInvitation]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Page.HasMore || page.Page.NextCursor == "" {
		t.Fatal(first.Body.String())
	}
	for _, url := range []string{path + "?limit=2&cursor=" + page.Page.NextCursor, path + "?limit=1&cursor=broken"} {
		requireProblem(t, do(t, h, http.MethodGet, url, "", actingRequestAdmin), TypeInvalidCursor)
	}
	requireProblem(t, do(t, h, http.MethodGet, path+"?limit=1&cursor="+page.Page.NextCursor, "", bearer(otherAdminToken)), TypeInvalidCursor)
	next := do(t, h, http.MethodGet, path+"?limit=1&cursor="+page.Page.NextCursor, "", actingRequestAdmin)
	if next.Code != 200 || f.after == nil || f.after.ID != 7 {
		t.Fatal(next.Code, next.Body.String())
	}
	for _, url := range []string{path, path + "/7"} {
		got := do(t, h, http.MethodGet, url, "", actingRequestAdmin)
		if got.Code != 200 || strings.Contains(got.Body.String(), "token") || strings.Contains(got.Body.String(), "claim_url") {
			t.Fatal(got.Code, got.Body.String())
		}
		requireProblem(t, do(t, h, http.MethodGet, url, "", requestOwner), TypePermissionDenied)
	}
}

func TestInvitationActualRateGate(t *testing.T) {
	f := fixtureInvitations()
	deps := requestDeps(fixtureRequests())
	deps.Invitations = f
	limiter := ratelimit.NewMiddleware(ratelimit.NewMemoryLimiter(), ratelimit.NewMemoryLimiter(), fakeSettings{}, true)
	if err := limiter.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	deps.BucketRateLimit = func(bucket string) func(http.Handler) http.Handler {
		if bucket != "invitation" {
			return func(h http.Handler) http.Handler { return h }
		}
		return limiter.Handler
	}
	h := NewHandler(deps)
	limited := false
	for range 5 {
		req := httptest.NewRequest(http.MethodPost, Prefix+"/invitations/pending/accept", strings.NewReader(`{"password":"password123"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(clientip.SetContext(req.Context(), "203.0.113.9"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 429 {
			requireProblem(t, rec, TypeRateLimited)
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("rate limit missing retry header")
			}
			limited = true
			break
		}
	}
	if !limited || f.writes >= 5 {
		t.Fatal("invitation limiter did not stop acceptance", f.writes)
	}
}

func TestInvitationMutationRetryDeclarations(t *testing.T) {
	expected := map[string]RetrySafety{"acceptInvitation": RetrySafetyNonRetryable, "createAdminInvitation": RetrySafetyNonRetryable, "resendAdminInvitation": RetrySafetyNonRetryable, "revokeAdminInvitation": RetrySafetyNaturalIdempotent}
	for _, op := range DeclaredOperations() {
		if want, ok := expected[op.OperationID]; ok {
			if op.RetrySafety != want {
				t.Fatalf("%s retry safety=%s", op.OperationID, op.RetrySafety)
			}
			delete(expected, op.OperationID)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing invitation operations=%v", expected)
	}
}

func TestInvitationUnconfiguredAndPasswordBounds(t *testing.T) {
	d := requestDeps(fixtureRequests())
	h := NewHandler(d)
	caps := do(t, h, http.MethodGet, Prefix+"/invitations/capabilities", "", nil)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"state":"not_configured"`) || !strings.Contains(caps.Body.String(), `"profileless":false`) {
		t.Fatal(caps.Code, caps.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/invitations/token", "", nil), TypeCapabilityNotConfigured)
	f := fixtureInvitations()
	h = invitationTestHandler(f)
	for _, password := range []string{"short", strings.Repeat("界", 25)} {
		body, _ := json.Marshal(map[string]string{"password": password})
		requireProblem(t, do(t, h, http.MethodPost, Prefix+"/invitations/token/accept", string(body), nil), TypeValidationFailed)
	}
	if f.writes != 0 {
		t.Fatal("invalid password reached acceptance")
	}
}

func TestLinkInvitationDelivery(t *testing.T) {
	f := fixtureInvitations()
	h := invitationTestHandler(f)
	path := Prefix + "/admin/invitations"

	requireProblem(t, do(t, h, http.MethodPost, path, `{"delivery":"link","email":"a@example.invalid"}`, actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"delivery":"email"}`, actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"delivery":"carrier-pigeon"}`, actingRequestAdmin), TypeValidationFailed)
	if f.writes != 0 {
		t.Fatal("invalid delivery reached service")
	}

	r := do(t, h, http.MethodPost, path, `{"delivery":"link","note":"For Sam"}`, actingRequestAdmin)
	if r.Code != 201 || f.send.Delivery != invitations.DeliveryLink || f.send.Email != "" || !strings.Contains(r.Body.String(), `"delivery_status":"not_requested"`) || !strings.Contains(r.Body.String(), `"delivery":"link"`) {
		t.Fatal(r.Code, r.Body.String(), f.send)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"delivery":"email","email":"a@example.invalid"}`, actingRequestAdmin), TypeCapabilityNotConfigured)

	caps := do(t, h, http.MethodGet, path+"/capabilities", "", actingRequestAdmin)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"email_delivery":false`) {
		t.Fatal(caps.Code, caps.Body.String())
	}
	f.emailDelivery = true
	caps = do(t, h, http.MethodGet, path+"/capabilities", "", actingRequestAdmin)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"email_delivery":true`) {
		t.Fatal(caps.Code, caps.Body.String())
	}
	if public := do(t, h, http.MethodGet, Prefix+"/invitations/capabilities", "", nil); strings.Contains(public.Body.String(), "email_delivery") {
		t.Fatal("public capabilities disclose email configuration:", public.Body.String())
	}

	f.row.Email, f.row.Note, f.row.Delivery = "", "For Sam", models.InvitationDeliveryLink
	lookup := do(t, h, http.MethodGet, Prefix+"/invitations/pending", "", nil)
	if lookup.Code != 200 || !strings.Contains(lookup.Body.String(), `"email_required":true`) || !strings.Contains(lookup.Body.String(), `"note":"For Sam"`) {
		t.Fatal(lookup.Code, lookup.Body.String())
	}
	listed := do(t, h, http.MethodGet, path+"/7", "", actingRequestAdmin)
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"delivery":"link"`) {
		t.Fatal(listed.Code, listed.Body.String())
	}

	accepted := do(t, h, http.MethodPost, Prefix+"/invitations/pending/accept", `{"email":"sam@example.invalid","password":"password123"}`, nil)
	if accepted.Code != 201 || f.acceptEmail != "sam@example.invalid" {
		t.Fatal(accepted.Code, accepted.Body.String(), f.acceptEmail)
	}
	f.errorAccept = invitations.ErrEmailTaken
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/invitations/pending/accept", `{"email":"taken@example.invalid","password":"password123"}`, nil), TypeConflict)
	f.errorAccept = invitations.ErrEmailRequired
	missing := do(t, h, http.MethodPost, Prefix+"/invitations/pending/accept", `{"password":"password123"}`, nil)
	requireProblem(t, missing, TypeValidationFailed)
	if !strings.Contains(missing.Body.String(), `"location":"body.email"`) {
		t.Fatal(missing.Body.String())
	}

	f.row.Delivery = ""
	if legacy := do(t, h, http.MethodGet, path+"/7", "", actingRequestAdmin); !strings.Contains(legacy.Body.String(), `"delivery":"unknown"`) {
		t.Fatal(legacy.Body.String())
	}
}

func TestResendInvitationDeliveryChoice(t *testing.T) {
	f := fixtureInvitations()
	h := invitationTestHandler(f)
	path := Prefix + "/admin/invitations/7/resend"

	requireProblem(t, do(t, h, http.MethodPost, path, `{"delivery":"carrier-pigeon"}`, actingRequestAdmin), TypeValidationFailed)
	if f.resendDelivery != nil {
		t.Fatal("invalid delivery reached service")
	}
	for _, tc := range []struct {
		body string
		want invitations.Delivery
	}{
		{body: "", want: invitations.DeliveryDefault},
		{body: `{}`, want: invitations.DeliveryDefault},
		{body: `{"delivery":"link"}`, want: invitations.DeliveryLink},
		{body: `{"delivery":"email"}`, want: invitations.DeliveryEmail},
	} {
		r := do(t, h, http.MethodPost, path, tc.body, actingRequestAdmin)
		if r.Code != 201 || f.resendDelivery == nil || *f.resendDelivery != tc.want {
			t.Fatalf("body %q: code=%d delivery=%v %s", tc.body, r.Code, f.resendDelivery, r.Body.String())
		}
	}
	// Replacing an emailed invitation's link keeps its address and emails nothing.
	linked := do(t, h, http.MethodPost, path, `{"delivery":"link"}`, actingRequestAdmin)
	if !strings.Contains(linked.Body.String(), `"delivery_status":"not_requested"`) || !strings.Contains(linked.Body.String(), `"email":"invitee@example.invalid"`) {
		t.Fatal(linked.Body.String())
	}

	f.row.Email = ""
	noAddress := do(t, h, http.MethodPost, path, `{"delivery":"email"}`, actingRequestAdmin)
	requireProblem(t, noAddress, TypeValidationFailed)
	if !strings.Contains(noAddress.Body.String(), `"location":"body.delivery"`) {
		t.Fatal(noAddress.Body.String())
	}
}
