package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeAutoscanConnectionUpdate struct {
	calls int
	err   error
}

func (f *fakeAutoscanConnectionUpdate) UpdateAdminAutoscanConnection(_ context.Context, id string, in handlers.AdminAutoscanConnectionUpdateInput) (handlers.AdminAutoscanConnectionView, error) {
	f.calls++
	return handlers.AdminAutoscanConnectionView{ID: id, Name: in.Name, Kind: in.Kind, HasAPIKey: in.APIKeyRef != ""}, f.err
}
func TestAdminAutoscanConnectionUpdate(t *testing.T) {
	f := new(fakeAutoscanConnectionUpdate)
	deps := pilotDeps(nil, nil)
	deps.AdminAutoscanConnectionUpdate = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/connections/connection-a"
	body := `{"name":"Synthetic","kind":"sonarr","base_url":"https://example.invalid","api_key_ref":"private-key"}`
	requireProblem(t, do(t, h, "PUT", path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "PUT", path, body, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "PUT", path, `{}`, bearer(adminToken)), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid request dispatched")
	}
	rec := do(t, h, "PUT", path, body, bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"has_api_key":true`) || strings.Contains(rec.Body.String(), "private-key") || strings.Contains(rec.Body.String(), "api_key_ref") || f.calls != 1 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, test := range []struct {
		err    error
		status int
	}{{handlers.ErrAdminAutoscanConnectionUpdateInvalid, 422}, {handlers.ErrAdminAutoscanConnectionUpdateUnavailable, 503}, {autoscan.ErrNotFound, 404}, {errors.New("private-store-key"), 500}} {
		f.err = test.err
		rec = do(t, h, "PUT", path, body, bearer(adminToken))
		if rec.Code != test.status || strings.Contains(rec.Body.String(), "private-store-key") {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	deps.AdminAutoscanConnectionUpdate = nil
	requireProblem(t, do(t, NewHandler(deps), "PUT", path, body, bearer(adminToken)), TypeDependencyUnavailable)
}
