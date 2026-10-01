package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeAutoscanConnectionCheck struct {
	calls  int
	result autoscan.ConnectionTestResult
	err    error
}

func (f *fakeAutoscanConnectionCheck) TestAdminAutoscanConnection(context.Context, handlers.AdminAutoscanConnectionTestInput) (autoscan.ConnectionTestResult, error) {
	f.calls++
	return f.result, f.err
}
func TestAdminAutoscanConnectionCheck(t *testing.T) {
	f := &fakeAutoscanConnectionCheck{result: autoscan.ConnectionTestResult{OK: true, Version: "4.0"}}
	deps := pilotDeps(nil, nil)
	deps.AdminAutoscanConnectionTests = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/connections/test"
	body := `{"connection_id":"stored"}`
	requireProblem(t, do(t, h, "POST", path, body, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "POST", path, body, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "POST", path, `{}`, bearer(adminToken)), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid check dispatched")
	}
	rec := do(t, h, "POST", path, body, bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"4.0"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.result = autoscan.ConnectionTestResult{Err: "private API key failure", Version: "private"}
	rec = do(t, h, "POST", path, `{"base_url":"https://example.invalid","api_key_ref":"private"}`, bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) || strings.Contains(rec.Body.String(), "private") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, test := range []struct {
		err    error
		status int
	}{{autoscan.ErrNotFound, 404}, {handlers.ErrAdminAutoscanConnectionTestUnavailable, 503}, {errors.New("private store failure"), 500}} {
		f.err = test.err
		rec = do(t, h, "POST", path, body, bearer(adminToken))
		if rec.Code != test.status || strings.Contains(rec.Body.String(), "private") {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	deps.AdminAutoscanConnectionTests = nil
	requireProblem(t, do(t, NewHandler(deps), "POST", path, body, bearer(adminToken)), TypeDependencyUnavailable)
}
