package apiv2

import (
	"context"
	"net/http"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

// TestActingAdminGateHouseholdRule drives the acting_admin class through the
// real router: on a household with a PIN-protected or access-limited profile,
// an admin session that declares no profile is refused like a non-primary
// profile, while the primary profile and API keys keep admin powers. An
// unrestricted household keeps profile-less admin access.
func TestActingAdminGateHouseholdRule(t *testing.T) {
	primary := func(_ context.Context, userID int, profileID string) (bool, bool, error) {
		return profileID == "p-primary", profileID == "p-primary" || profileID == "p-owner", nil
	}
	for _, tc := range []struct {
		name       string
		restricted bool
		headers    map[string]string
		want       ProblemType
	}{
		{"no profile, restricted household", true, bearer(adminToken), TypePermissionDenied},
		{"non-primary profile", true, with(bearer(adminToken), "X-Profile-Id", "p-owner"), TypePermissionDenied},
		{"primary profile, restricted household", true, with(bearer(adminToken), "X-Profile-Id", "p-primary"), ProblemType{}},
		{"no profile, unrestricted household", false, bearer(adminToken), ProblemType{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := parityDeps(false)
			restricted := tc.restricted
			deps.ActingAdmin = apimw.RequireActingAdmin(primary, func(context.Context, int) (bool, error) { return restricted, nil })
			rec := do(t, newTestHandler(t, deps), http.MethodPost, "/api/v2/probe/"+string(ClassActingAdmin), `{"name":"x","cleared":null}`, tc.headers)
			if tc.want == (ProblemType{}) {
				if rec.Code != http.StatusOK {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				return
			}
			requireProblem(t, rec, tc.want)
		})
	}
}
