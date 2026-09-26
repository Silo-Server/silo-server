package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ownerRefusal is what the account services return when a caller other than
// the server Owner targets the Owner's account.
var ownerRefusal = &handlers.APIError{Status: http.StatusForbidden, Code: "owner_protected", Message: "Only the server owner can change the owner account"}

// ownerRefusingAccounts refuses every account write the way the Owner rules do.
type ownerRefusingAccounts struct{ *fakeAdminAccounts }

func (ownerRefusingAccounts) UpdateAdminAccount(context.Context, int, int64, int64, models.UpdateUserInput) (int64, error) {
	return 0, ownerRefusal
}
func (ownerRefusingAccounts) DeleteAdminAccount(context.Context, int, int64, int64) error {
	return ownerRefusal
}

func TestOwnerRefusalsRenderAsPermissionDenied(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = ownerRefusingAccounts{fixtureAdminAccounts()}
	resets := fixturePasswordResets()
	resets.issueErr = ownerRefusal
	deps.PasswordResets = resets
	keys := fixtureAdminAPIKeys()
	keys.err = ownerRefusal
	deps.AdminAPIKeys = keys
	h := NewHandler(deps)

	account := Prefix + "/admin/users/7"
	key := Prefix + adminAPIKeyPath + "/7"
	anyMatch := with(actingRequestAdmin, "If-Match", "*")
	for _, tc := range []struct{ name, method, path, body string }{
		{"update account", http.MethodPut, account, `{"enabled":false}`},
		{"delete account", http.MethodDelete, account, ""},
		{"issue reset", http.MethodPost, account + "/password-reset", `{"delivery":"link"}`},
		{"create API key", http.MethodPost, Prefix + adminAPIKeyPath, `{"label":"takeover","user_id":"7"}`},
		{"change key tier", http.MethodPut, key + "/tier", `{"rate_tier":"elevated"}`},
		{"revoke API key", http.MethodDelete, key, ""},
	} {
		headers := anyMatch
		if tc.method == http.MethodPost {
			headers = actingRequestAdmin
		}
		t.Run(tc.name, func(t *testing.T) {
			requireProblem(t, do(t, h, tc.method, tc.path, tc.body, headers), TypePermissionDenied)
		})
	}
}
