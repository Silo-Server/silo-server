package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeAPIKeyStore records what the handler asked to store.
type fakeAPIKeyStore struct {
	createdScopes []string
	created       bool
}

func (s *fakeAPIKeyStore) Create(_ context.Context, userID int, label string, scopes []string) (*models.APIKey, error) {
	s.created = true
	s.createdScopes = scopes
	return &models.APIKey{ID: 1, UserID: userID, Label: label, Key: "sa_generated", RateTier: "standard", Scopes: scopes}, nil
}

func (s *fakeAPIKeyStore) ListByUser(context.Context, int) ([]*models.APIKey, error) { return nil, nil }

func (s *fakeAPIKeyStore) ListByUserAdmin(context.Context, int) ([]*models.APIKey, error) {
	return nil, nil
}

func (s *fakeAPIKeyStore) ListAll(context.Context) ([]*models.APIKeyWithUser, error) {
	return nil, nil
}

func (s *fakeAPIKeyStore) Delete(context.Context, int64, int) error   { return nil }
func (s *fakeAPIKeyStore) DeleteByAdmin(context.Context, int64) error { return nil }
func (s *fakeAPIKeyStore) UpdateTier(context.Context, int64, string) error {
	return nil
}

func createAPIKey(t *testing.T, body string) (*httptest.ResponseRecorder, *fakeAPIKeyStore) {
	t.Helper()
	store := &fakeAPIKeyStore{}
	h := NewAPIKeyHandler(store)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(body))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    7,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
		SessionID: "s1",
	}))
	rec := httptest.NewRecorder()
	h.HandleCreateAPIKey(rec, req)
	return rec, store
}

func TestHandleCreateAPIKeyHonorsRequestedScopes(t *testing.T) {
	rec, store := createAPIKey(t, `{"label":"ci","scopes":["admin:access-groups:read","admin:users","admin:users"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	want := []string{auth.ScopeAdminAccessGroupsRead, auth.ScopeAdminUsers}
	if !reflect.DeepEqual(store.createdScopes, want) {
		t.Fatalf("stored scopes = %v, want %v (normalized: deduplicated and sorted)", store.createdScopes, want)
	}

	var resp apiKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !reflect.DeepEqual(resp.Scopes, want) {
		t.Fatalf("response scopes = %v, want %v", resp.Scopes, want)
	}
}

func TestHandleCreateAPIKeyWithoutScopesStaysUnscoped(t *testing.T) {
	rec, store := createAPIKey(t, `{"label":"ci"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if len(store.createdScopes) != 0 {
		t.Fatalf("stored scopes = %v, want none", store.createdScopes)
	}
}

func TestHandleCreateAPIKeyRejectsUnknownScope(t *testing.T) {
	rec, store := createAPIKey(t, `{"label":"ci","scopes":["admin:everything"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if store.created {
		t.Fatal("an unknown scope must not create a key")
	}
}

func TestHandleListAPIKeyScopes(t *testing.T) {
	h := NewAPIKeyHandler(&fakeAPIKeyStore{})

	t.Run("requires authentication", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/api-keys/scopes", nil)
		rec := httptest.NewRecorder()
		h.HandleListAPIKeyScopes(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("lists every valid scope with a description", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/api-keys/scopes", nil)
		req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7, TokenType: auth.TokenTypeAccess}))
		rec := httptest.NewRecorder()
		h.HandleListAPIKeyScopes(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}

		var resp apiKeyScopesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		valid := auth.ValidAPIKeyScopes()
		if len(resp.Scopes) != len(valid) {
			t.Fatalf("scopes = %+v, want %d entries", resp.Scopes, len(valid))
		}
		for _, scope := range resp.Scopes {
			if !slices.Contains(valid, scope.Name) {
				t.Fatalf("scope %q is not accepted by NormalizeAPIKeyScopes", scope.Name)
			}
			if strings.TrimSpace(scope.Description) == "" {
				t.Fatalf("scope %q has no description", scope.Name)
			}
		}
	})
}
