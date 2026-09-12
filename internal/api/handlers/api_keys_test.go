package handlers

import (
	"context"
	"encoding/json"
	"errors"
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

func (s *fakeAPIKeyStore) ListByUser(context.Context, int) ([]*models.APIKeyMetadataWithUsage, error) {
	return nil, nil
}

func (s *fakeAPIKeyStore) ListByUserAdmin(context.Context, int) ([]*models.APIKeyMetadataWithUsage, error) {
	return nil, nil
}

func (s *fakeAPIKeyStore) ListAll(context.Context) ([]*models.APIKeyMetadataWithUser, error) {
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

	t.Run("lists the frozen v1 scopes with descriptions", func(t *testing.T) {
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
		// V2 scope additions must not expand the frozen v1 discovery payload.
		valid := []string{auth.ScopeAdminUsers, auth.ScopeAdminAccessGroupsRead}
		if len(resp.Scopes) != len(valid) {
			t.Fatalf("scopes = %+v, want %d entries", resp.Scopes, len(valid))
		}
		for i, scope := range resp.Scopes {
			if scope.Name != valid[i] {
				t.Fatalf("scope %d = %q, want frozen v1 scope %q", i, scope.Name, valid[i])
			}
			if strings.TrimSpace(scope.Description) == "" {
				t.Fatalf("scope %q has no description", scope.Name)
			}
		}
	})
}

func (s *fakeAPIKeyStore) GetMetadataByID(context.Context, int64) (*models.APIKeyMetadata, error) {
	return nil, auth.ErrAPIKeyNotFound
}
func (s *fakeAPIKeyStore) ListAllPage(context.Context, *auth.APIKeyPageKey, int) ([]*models.APIKeyMetadataWithUser, bool, error) {
	return nil, false, nil
}
func (s *fakeAPIKeyStore) UpdateTierConditional(context.Context, int64, string, auth.APIKeyPrecondition) (*models.APIKeyMetadata, error) {
	return nil, auth.ErrAPIKeyNotFound
}
func (s *fakeAPIKeyStore) DeleteByAdminConditional(context.Context, int64, auth.APIKeyPrecondition) error {
	return auth.ErrAPIKeyNotFound
}

func TestCreateAdminAPIKeyApplicationValidatesBeforeStorage(t *testing.T) {
	for _, input := range []struct {
		userID int
		label  string
		scopes []string
	}{
		{0, "new", nil},
		{7, "", nil},
		{7, "new", []string{"unknown"}},
	} {
		store := &fakeAPIKeyStore{}
		_, err := NewAPIKeyHandler(store).CreateAdminAPIKey(t.Context(), input.userID, input.label, input.scopes)
		if !errors.Is(err, ErrInvalidAPIKeyCreation) || store.created {
			t.Fatalf("invalid input reached storage: %v", err)
		}
	}
	store := &fakeAPIKeyStore{}
	key, err := NewAPIKeyHandler(store).CreateAdminAPIKey(t.Context(), 7, "new", []string{auth.ScopeAdminUsers, auth.ScopeAdminUsers})
	if err != nil || key.UserID != 7 || !slices.Equal(store.createdScopes, []string{auth.ScopeAdminUsers}) {
		t.Fatalf("scope normalization failed: %v", err)
	}
}

// Account filtering must survive the transport-to-store boundary: the personal
// service cannot accidentally call the unfiltered administrator delete.
type personalAPIKeyStore struct {
	fakeAPIKeyStore
	userID int
	keyID  int64
	limit  int
	after  *auth.APIKeyPageKey
}

func (s *personalAPIKeyStore) Delete(_ context.Context, id int64, userID int) error {
	s.keyID, s.userID = id, userID
	return auth.ErrAPIKeyNotFound
}
func (s *personalAPIKeyStore) DeleteByAdmin(context.Context, int64) error { panic("unfiltered delete") }
func (s *personalAPIKeyStore) ListByUserAdminPage(_ context.Context, userID int, after *auth.APIKeyPageKey, limit int) ([]*models.APIKeyMetadataWithUser, bool, error) {
	s.userID, s.after, s.limit = userID, after, limit
	return []*models.APIKeyMetadataWithUser{}, false, nil
}
func TestPersonalAPIKeyServiceAccountBoundary(t *testing.T) {
	store := new(personalAPIKeyStore)
	handler := NewAPIKeyHandler(store)
	if err := handler.RevokePersonalAPIKey(t.Context(), 42, 7); !errors.Is(err, auth.ErrAPIKeyNotFound) {
		t.Fatal(err)
	}
	if store.userID != 42 || store.keyID != 7 {
		t.Fatal(store.userID, store.keyID)
	}
	after := &auth.APIKeyPageKey{ID: 9}
	rows, more, err := handler.ListPersonalAPIKeysPage(t.Context(), 42, after, 25)
	if err != nil || more || rows == nil || store.userID != 42 || store.after != after || store.limit != 25 {
		t.Fatal(rows, more, err, store)
	}
}
