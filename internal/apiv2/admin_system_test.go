package apiv2

import (
	"net/http"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAdminResourceScopedKeysStayDenied(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{}, fakeSessions{}, fakeAPIKeys{keys: map[string]*models.APIKey{
		"sa_users": {ID: 1, UserID: 2, Scopes: []string{auth.ScopeAdminUsers}},
		"sa_admin": {ID: 2, UserID: 2},
	}}, fakeUsers{users: map[int]*models.User{2: {ID: 2, Role: "admin", Enabled: true}}})
	h := NewHandler(deps)
	for _, path := range []string{Prefix + "/admin/system/resources", Prefix + "/admin/system/resources/capabilities"} {
		requireProblem(t, do(t, h, "GET", path, "", bearer("sa_users")), TypePermissionDenied)
		if rec := do(t, h, "GET", path, "", bearer("sa_admin")); rec.Code != http.StatusOK {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
}
