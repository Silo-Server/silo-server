package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminAPIKeys struct {
	row        handlers.APIKeyConfiguration
	guard      auth.APIKeyPrecondition
	writes     int
	race       bool
	after      *auth.APIKeyPageKey
	createUser int
	err        error
}

func fixtureAdminAPIKeys() *fakeAdminAPIKeys {
	return &fakeAdminAPIKeys{row: handlers.APIKeyConfiguration{ID: 7, UserID: 2, Label: "Automation", KeyPrefix: "sa_12345678", RateTier: adminAPIKeyStandardTier, Scopes: []string{}, CreatedAt: fixedTime(), Revision: 11}}
}
func (f *fakeAdminAPIKeys) GetAdminAPIKey(context.Context, int64) (*handlers.APIKeyConfiguration, error) {
	row := f.row
	return &row, f.err
}
func (f *fakeAdminAPIKeys) ListAdminAPIKeysPage(_ context.Context, after *auth.APIKeyPageKey, _ int) ([]handlers.AdminAPIKeyListItem, bool, error) {
	f.after = after
	row := f.row
	if after != nil {
		row.ID = 6
	}
	return []handlers.AdminAPIKeyListItem{{APIKeyListItem: handlers.APIKeyListItem{APIKeyConfiguration: row, LastUsedAt: new(fixedTime())}, Username: "owner"}}, after == nil, f.err
}
func (f *fakeAdminAPIKeys) CreateAdminAPIKey(_ context.Context, user int, label string, scopes []string) (*models.APIKey, error) {
	f.createUser = user
	f.writes++
	return &models.APIKey{ID: 8, UserID: user, Label: label, Scopes: scopes, Key: "creation-secret", RateTier: adminAPIKeyStandardTier, CreatedAt: fixedTime()}, f.err
}
func (f *fakeAdminAPIKeys) UpdateAdminAPIKeyTier(_ context.Context, _ int64, tier string, guard auth.APIKeyPrecondition) (*handlers.APIKeyConfiguration, error) {
	f.guard = guard
	if f.race {
		f.row.Revision++
		return nil, &auth.APIKeyRevisionConflict{Current: &models.APIKeyMetadata{ID: f.row.ID, Revision: f.row.Revision}}
	}
	f.writes++
	f.row.Revision++
	f.row.RateTier = tier
	row := f.row
	return &row, f.err
}
func (f *fakeAdminAPIKeys) DeleteAdminAPIKey(_ context.Context, _ int64, guard auth.APIKeyPrecondition) error {
	f.guard = guard
	f.writes++
	return f.err
}
func adminAPIKeyHandler(f *fakeAdminAPIKeys) http.Handler {
	deps := requestDeps(fixtureRequests())
	if f != nil {
		deps.AdminAPIKeys = f
	}
	return NewHandler(deps)
}

func TestAdminAPIKeyAuthorityAndUnavailable(t *testing.T) {
	h := adminAPIKeyHandler(nil)
	path := Prefix + adminAPIKeyPath
	caps := do(t, h, http.MethodGet, path+"/capabilities", "", actingRequestAdmin)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"available":false`) {
		t.Fatal(caps.Code, caps.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
	requireProblem(t, do(t, h, http.MethodGet, path, "", requestOwner), TypePermissionDenied)
}

func TestAdminAPIKeyAuthenticationPolicy(t *testing.T) {
	credentials := []struct {
		name    string
		user    int
		scopes  []string
		allowed bool
	}{
		{"unscoped administrator", 2, nil, true},
		{"scoped administrator", 2, []string{auth.ScopeAdminUsers}, false},
		{"ordinary account", 1, nil, false},
	}
	f := fixtureAdminAPIKeys()
	deps := requestDeps(fixtureRequests())
	deps.AdminAPIKeys = f
	keys := make(map[string]*models.APIKey, len(credentials))
	for i, credential := range credentials {
		keys["sa_"+strings.ReplaceAll(credential.name, " ", "_")] = &models.APIKey{ID: int64(i + 1), UserID: credential.user, Scopes: credential.scopes}
	}
	deps.Auth = apimw.NewAuthMiddleware(nil, nil, fakeAPIKeys{keys: keys}, fakeUsers{users: map[int]*models.User{1: {ID: 1, Role: "user", Enabled: true}, 2: {ID: 2, Role: "admin", Enabled: true}}})
	h := NewHandler(deps)
	for _, credential := range credentials {
		t.Run(credential.name, func(t *testing.T) {
			*f = *fixtureAdminAPIKeys()
			for _, op := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodGet, adminAPIKeyPath + "/capabilities", "", 200},
				{http.MethodGet, adminAPIKeyPath, "", 200},
				{http.MethodGet, adminAPIKeyPath + "/7", "", 200},
				{http.MethodPost, adminAPIKeyPath, `{"label":"Tool"}`, 201},
				{http.MethodPut, adminAPIKeyPath + "/7/tier", `{"rate_tier":"elevated"}`, 200},
				{http.MethodDelete, adminAPIKeyPath + "/7", "", 204},
			} {
				got := do(t, h, op.method, Prefix+op.path, op.body, with(bearer("sa_"+strings.ReplaceAll(credential.name, " ", "_")), "If-Match", "*"))
				if credential.allowed {
					if got.Code != op.status {
						t.Fatalf("%s %s: %d %s", op.method, op.path, got.Code, got.Body.String())
					}
				} else if got.Code != http.StatusForbidden {
					t.Fatalf("%s %s admitted: %d %s", op.method, op.path, got.Code, got.Body.String())
				}
			}
			if !credential.allowed && f.writes != 0 {
				t.Fatal("denied credential reached mutations")
			}
		})
	}
}
