package apiv2

import (
	"net/http"
	"strings"
	"testing"
)

func TestGetSetupStatus(t *testing.T) {
	deps := pilotDeps(nil, nil)
	// A fresh install has nothing to have completed, whatever the store says.
	deps.Accounts = fakeAccounts{needsSetup: true, wizardCompleted: true}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/system/setup", "", nil)
	if rec.Code != 200 || rec.Body.String() != `{"needs_setup":true,"wizard_completed":false}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Public: a credential or profile header changes nothing.
	rec = do(t, h, http.MethodGet, "/api/v2/system/setup", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needs_setup":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
