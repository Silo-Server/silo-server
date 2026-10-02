package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAPIKeyScopesAllow(t *testing.T) {
	users := []string{auth.ScopeAdminUsers}
	groups := []string{auth.ScopeAdminAccessGroupsRead}
	both := []string{auth.ScopeAdminUsers, auth.ScopeAdminAccessGroupsRead}

	tests := []struct {
		name   string
		scopes []string
		method string
		path   string
		want   bool
	}{
		{"unscoped allows anything", nil, http.MethodPost, "/api/v1/admin/settings", true},
		{"empty scopes allow anything", []string{}, http.MethodGet, "/api/v1/watch/home", true},

		{"users list", users, http.MethodGet, "/api/v1/admin/users", true},
		{"users create", users, http.MethodPost, "/api/v1/admin/users", true},
		{"users get", users, http.MethodGet, "/api/v1/admin/users/42", true},
		{"users update", users, http.MethodPut, "/api/v1/admin/users/42", true},
		{"users delete", users, http.MethodDelete, "/api/v1/admin/users/42", true},
		{"users profiles", users, http.MethodGet, "/api/v1/admin/users/42/profiles", true},
		{"users v2 list", users, http.MethodGet, "/api/v2/admin/users", true},
		{"users exact identity lookup", users, http.MethodGet, "/api/v2/admin/users?identity=admin%40example.test", true},
		{"summary read", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodGet, "/api/v2/admin/sessions/summary?user_id=42&limit=5", true},
		{"summary capabilities", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodGet, "/api/v2/admin/sessions/capabilities", true},
		{"summary denies diagnostics", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodGet, "/api/v2/admin/sessions", false},
		{"summary denies control", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodDelete, "/api/v2/admin/sessions/session-a", false},
		{"summary denies account editing", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodPut, "/api/v2/admin/users/42", false},
		{"library discovery", []string{auth.ScopeLibrariesRead}, http.MethodGet, "/api/v2/user/libraries", true},
		{"library capabilities", []string{auth.ScopeLibrariesRead}, http.MethodGet, "/api/v2/user/libraries/capabilities", true},
		{"library denies storage metadata", []string{auth.ScopeLibrariesRead}, http.MethodGet, "/api/v2/libraries", false},
		{"library denies management", []string{auth.ScopeLibrariesRead}, http.MethodPost, "/api/v2/libraries", false},
		{"library denies playback", []string{auth.ScopeLibrariesRead}, http.MethodPost, "/api/v2/playback/start", false},
		{"users v2 create", users, http.MethodPost, "/api/v2/admin/users", true},
		{"users v2 update", users, http.MethodPut, "/api/v2/admin/users/42", true},
		{"users v2 delete", users, http.MethodDelete, "/api/v2/admin/users/42", true},
		{"users v2 profiles", users, http.MethodGet, "/api/v2/admin/users/42/profiles", true},
		{"users v2 denies impersonation", users, http.MethodPost, "/api/v2/admin/users/42/impersonate", false},
		{"users v2 denies settings", users, http.MethodPut, "/api/v2/admin/users/42/settings/values/key", false},
		{"users v2 denies keys", users, http.MethodGet, "/api/v2/admin/users/42/api-keys", false},
		{"users v2 denies IP history", users, http.MethodGet, "/api/v2/admin/users/42/ips", false},
		{"groups v2 read", groups, http.MethodGet, "/api/v2/admin/access-groups/3", true},
		{"groups v2 denies write", groups, http.MethodPut, "/api/v2/admin/access-groups/3", false},
		{"users scope denies v2 profile update", users, http.MethodPatch, "/api/v2/profiles/1", false},

		{"users scope denies impersonate", users, http.MethodPost, "/api/v1/admin/users/42/impersonate", false},
		{"users scope denies settings values", users, http.MethodGet, "/api/v1/admin/users/42/settings/values", false},
		{"users scope denies user api keys", users, http.MethodGet, "/api/v1/admin/users/42/api-keys", false},
		{"users scope denies user ips", users, http.MethodGet, "/api/v1/admin/users/42/ips", false},
		{"users scope denies collection delete", users, http.MethodDelete, "/api/v1/admin/users", false},
		{"users scope denies admin settings", users, http.MethodGet, "/api/v1/admin/settings", false},
		{"users scope denies access groups", users, http.MethodGet, "/api/v1/admin/access-groups", false},
		{"users scope denies non-admin surface", users, http.MethodGet, "/api/v1/watch/home", false},
		{"users scope denies the scope catalog", users, http.MethodGet, "/api/v1/api-keys/scopes", false},
		{"users scope denies non-numeric id", users, http.MethodGet, "/api/v1/admin/users/abc", false},

		{"traversal cannot dodge the allowlist", users, http.MethodGet, "/api/v1/admin/settings/../users", true},
		{"traversal cannot reach unlisted route", users, http.MethodGet, "/api/v1/admin/users/42/../../settings", false},
		{"double slash is cleaned before matching", users, http.MethodGet, "/api/v1//admin//users", true},

		{"groups read list", groups, http.MethodGet, "/api/v1/admin/access-groups", true},
		{"groups read get", groups, http.MethodGet, "/api/v1/admin/access-groups/3", true},
		{"groups scope denies create", groups, http.MethodPost, "/api/v1/admin/access-groups", false},
		{"groups scope denies update", groups, http.MethodPut, "/api/v1/admin/access-groups/3", false},
		{"groups scope denies delete", groups, http.MethodDelete, "/api/v1/admin/access-groups/3", false},
		{"groups scope denies users", groups, http.MethodGet, "/api/v1/admin/users", false},
		{"groups scope denies v2 users", groups, http.MethodGet, "/api/v2/admin/users", false},

		{"combined scopes union", both, http.MethodGet, "/api/v1/admin/access-groups", true},
		{"combined scopes still deny elsewhere", both, http.MethodPut, "/api/v1/admin/settings", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			if got := apiKeyScopesAllow(tt.scopes, r); got != tt.want {
				t.Fatalf("apiKeyScopesAllow(%v, %s %s) = %v, want %v", tt.scopes, tt.method, tt.path, got, tt.want)
			}
		})
	}
}

type fakeAPIKeyValidator struct {
	key *models.APIKey
}

func (f *fakeAPIKeyValidator) GetByKey(_ context.Context, key string) (*models.APIKey, error) {
	if f.key != nil && f.key.Key == key {
		return f.key, nil
	}
	return nil, auth.ErrAPIKeyNotFound
}

func (f *fakeAPIKeyValidator) UpdateLastUsed(context.Context, int64) error { return nil }

type fakeAPIKeyUserLoader struct {
	user *models.User
}

func (f *fakeAPIKeyUserLoader) GetByID(context.Context, int) (*models.User, error) {
	return f.user, nil
}

func TestRequireAuthEnforcesAPIKeyScopes(t *testing.T) {
	key := &models.APIKey{
		ID:     1,
		UserID: 7,
		Key:    "sa_test",
		Scopes: []string{auth.ScopeAdminUsers},
	}
	owner := &models.User{ID: 7, Role: "admin", Enabled: true}
	am := NewAuthMiddleware(nil, nil, &fakeAPIKeyValidator{key: key}, &fakeAPIKeyUserLoader{user: owner})

	handler := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil || claims.TokenType != auth.TokenTypeAPIKey {
			t.Fatal("expected api key claims in context")
		}
		if len(claims.APIKeyScopes) != 1 || claims.APIKeyScopes[0] != auth.ScopeAdminUsers {
			t.Fatalf("claims scopes = %v", claims.APIKeyScopes)
		}
		w.WriteHeader(http.StatusOK)
	}))

	allowed := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil)
	allowed.Header.Set("Authorization", "Bearer sa_test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, allowed)
	if rec.Code != http.StatusOK {
		t.Fatalf("in-scope route: status = %d, want 200", rec.Code)
	}

	denied := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	denied.Header.Set("Authorization", "Bearer sa_test")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, denied)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope route: status = %d, want 403", rec.Code)
	}
}

func TestAPIKeyScopesCannotManageV2Credentials(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v2/admin/api-keys/capabilities"},
		{http.MethodGet, "/api/v2/admin/api-keys"},
		{http.MethodGet, "/api/v2/admin/api-keys/1"},
		{http.MethodPost, "/api/v2/admin/api-keys"},
		{http.MethodPut, "/api/v2/admin/api-keys/1/tier"},
		{http.MethodDelete, "/api/v2/admin/api-keys/1"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, nil)
			if apiKeyScopesAllow(auth.ValidAPIKeyScopes(), request) {
				t.Fatal("scoped key can escape its allowlist through key management")
			}
			if !apiKeyScopesAllow(nil, request) {
				t.Fatal("unscoped key lost its existing account-level access")
			}
		})
	}
}
