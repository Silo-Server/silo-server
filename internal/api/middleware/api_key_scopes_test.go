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
