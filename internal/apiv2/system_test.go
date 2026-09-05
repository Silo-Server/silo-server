package apiv2

import (
	"net/http"
	"strings"
	"testing"
)

func TestGetSetupStatus(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.Accounts = fakeAccounts{needsSetup: true}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/system/setup", "", nil)
	if rec.Code != 200 || rec.Body.String() != `{"needs_setup":true}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Public: a credential or profile header changes nothing.
	rec = do(t, h, http.MethodGet, "/api/v2/system/setup", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needs_setup":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestGetSetupStatusFailures(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.Accounts = fakeAccounts{err: errStore}
	h := newTestHandler(t, deps)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/system/setup", "", nil), TypeInternalError)
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/system/setup?verbose=1", "", nil), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.verbose" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	deps.Accounts = nil
	rec := do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/system/setup", "", nil)
	requireProblem(t, rec, TypeDependencyUnavailable)
}
