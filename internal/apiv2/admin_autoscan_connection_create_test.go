package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAutoscanConnectionCreation struct {
	calls int
	err   error
}

func (f *fakeAutoscanConnectionCreation) CreateAdminAutoscanConnection(_ context.Context, in handlers.AdminAutoscanConnectionCreateInput) (handlers.AdminAutoscanConnectionView, error) {
	f.calls++
	return handlers.AdminAutoscanConnectionView{ID: "created", Name: in.Name, Kind: in.Kind, HasAPIKey: in.APIKeyRef != ""}, f.err
}
func TestAdminAutoscanConnectionCreate(t *testing.T) {
	f := new(fakeAutoscanConnectionCreation)
	deps := pilotDeps(nil, nil)
	deps.AdminAutoscanConnectionCreation = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/connections"
	body := `{"name":"Synthetic","kind":"sonarr","base_url":"https://example.invalid","api_key_ref":"private-key"}`
	requireProblem(t, do(t, h, "POST", path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "POST", path, body, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "POST", path, `{}`, bearer(adminToken)), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid request dispatched")
	}
	rec := do(t, h, "POST", path, body, bearer(adminToken))
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"has_api_key":true`) || strings.Contains(rec.Body.String(), "private-key") || strings.Contains(rec.Body.String(), "api_key_ref") || f.calls != 1 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, test := range []struct {
		err    error
		status int
	}{{handlers.ErrAdminAutoscanConnectionCreateInvalid, 422}, {handlers.ErrAdminAutoscanConnectionCreateUnavailable, 503}, {errors.New("private-store-key"), 500}} {
		f.err = test.err
		rec = do(t, h, "POST", path, body, bearer(adminToken))
		if rec.Code != test.status || strings.Contains(rec.Body.String(), "private-store-key") {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	deps.AdminAutoscanConnectionCreation = nil
	requireProblem(t, do(t, NewHandler(deps), "POST", path, body, bearer(adminToken)), TypeDependencyUnavailable)
}
