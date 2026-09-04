package apiv2

import (
	"encoding/json"
	"net/http"
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
