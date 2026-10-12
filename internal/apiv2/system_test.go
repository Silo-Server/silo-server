package apiv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

func TestGetSetupStatus(t *testing.T) {
	deps := pilotDeps(nil, nil)
	// A fresh install has nothing to have completed, whatever the store says.
	deps.Accounts = fakeAccounts{needsSetup: true, wizardCompleted: true}
	h := newTestHandler(t, deps)
	// A request without a resolved client address is not local.
	rec := do(t, h, http.MethodGet, "/api/v2/system/setup", "", nil)
	if rec.Code != 200 || rec.Body.String() != `{"needs_setup":true,"wizard_completed":false,"setup_token_required":true}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// The answer depends on the caller's network, so no cache may keep it.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	// Public: a credential or profile header changes nothing.
	rec = do(t, h, http.MethodGet, "/api/v2/system/setup", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needs_setup":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// setup_token_required follows the client's network location while setup
// is pending, and is false once an account exists wherever the client is.
func TestGetSetupStatusSetupTokenRequired(t *testing.T) {
	accounts := new(fakeAccounts)
	deps := pilotDeps(nil, nil)
	deps.Accounts = accounts
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		name       string
		needsSetup bool
		clientIP   string
		provider   string
		want       bool
	}{
		{"lan client", true, "192.168.1.20", "", false},
		{"loopback client", true, "127.0.0.1", "", false},
		{"internet client", true, "203.0.113.9", "", true},
		{"network access provider", true, "10.0.0.5", "tailscale", true},
		{"setup done", false, "203.0.113.9", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*accounts = fakeAccounts{needsSetup: tc.needsSetup}
			r := httptest.NewRequest(http.MethodGet, "/api/v2/system/setup", nil)
			ctx := clientip.SetContext(r.Context(), tc.clientIP)
			if tc.provider != "" {
				ctx = netaccess.WithPath(ctx, netaccess.Path{Provider: tc.provider})
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r.WithContext(ctx))
			var body SetupStatus
			if err := json.Unmarshal(rec.Body.Bytes(), &body); rec.Code != 200 || err != nil {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if body.SetupTokenRequired != tc.want {
				t.Fatalf("setup_token_required = %v, want %v", body.SetupTokenRequired, tc.want)
			}
		})
	}
}

func TestGetSetupStatusWizardCompleted(t *testing.T) {
	accounts := new(fakeAccounts)
	deps := pilotDeps(nil, nil)
	deps.Accounts = accounts
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		name     string
		accounts fakeAccounts
		want     string
	}{
		{"marker set", fakeAccounts{wizardCompleted: true}, `{"needs_setup":false,"wizard_completed":true,"setup_token_required":false}`},
		{"marker unset", fakeAccounts{}, `{"needs_setup":false,"wizard_completed":false,"setup_token_required":false}`},
		{"store failing", fakeAccounts{wizardErr: errStore}, `{"needs_setup":false,"wizard_completed":false,"setup_token_required":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*accounts = tc.accounts
			rec := do(t, h, http.MethodGet, "/api/v2/system/setup", "", nil)
			if rec.Code != 200 || rec.Body.String() != tc.want+"\n" {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
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
