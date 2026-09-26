package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// scopeCatalogStore is the storage half of the real API key application
// service, so these tests exercise the validator v2 creation actually runs
// instead of a fake that re-implements it.
type scopeCatalogStore struct {
	created bool
	scopes  []string
}

func (s *scopeCatalogStore) Create(_ context.Context, userID int, label string, scopes []string) (*models.APIKey, error) {
	s.created, s.scopes = true, scopes
	return &models.APIKey{ID: 8, UserID: userID, Label: label, Key: "creation-secret", RateTier: adminAPIKeyStandardTier, Scopes: scopes, CreatedAt: fixedTime()}, nil
}
func (s *scopeCatalogStore) ListByUser(context.Context, int) ([]*models.APIKeyMetadataWithUsage, error) {
	return nil, nil
}
func (s *scopeCatalogStore) ListByUserAdmin(context.Context, int) ([]*models.APIKeyMetadataWithUsage, error) {
	return nil, nil
}
func (s *scopeCatalogStore) ListAll(context.Context) ([]*models.APIKeyMetadataWithUser, error) {
	return nil, nil
}
func (s *scopeCatalogStore) Delete(context.Context, int64, int) error   { return nil }
func (s *scopeCatalogStore) DeleteByAdmin(context.Context, int64) error { return nil }
func (s *scopeCatalogStore) UpdateTier(context.Context, int64, string) error {
	return nil
}
func (s *scopeCatalogStore) GetMetadataByID(context.Context, int64) (*models.APIKeyMetadata, error) {
	return nil, auth.ErrAPIKeyNotFound
}
func (s *scopeCatalogStore) ListAllPage(context.Context, *auth.APIKeyPageKey, int) ([]*models.APIKeyMetadataWithUser, bool, error) {
	return nil, false, nil
}
func (s *scopeCatalogStore) UpdateTierConditional(context.Context, int64, string, auth.APIKeyPrecondition) (*models.APIKeyMetadata, error) {
	return nil, auth.ErrAPIKeyNotFound
}
func (s *scopeCatalogStore) DeleteByAdminConditional(context.Context, int64, auth.APIKeyPrecondition) error {
	return auth.ErrAPIKeyNotFound
}

func realAPIKeyHandler() (http.Handler, *scopeCatalogStore) {
	store := &scopeCatalogStore{}
	svc := handlers.NewAPIKeyHandler(store)
	deps := pilotDeps(nil, nil)
	deps.PersonalAPIKeys = svc
	deps.AdminAPIKeys = svc
	return NewHandler(deps), store
}

// The v2 scope catalog is what clients read from the scope-discovery endpoint,
// so every scope it advertises has to be creatable.
func TestV2APIKeyCreateAcceptsEveryAdvertisedScope(t *testing.T) {
	for _, scope := range auth.APIKeyScopeCatalog() {
		for _, path := range []string{"/api-keys", adminAPIKeyPath} {
			h, store := realAPIKeyHandler()
			body := `{"label":"Tool","scopes":["` + scope.Name + `"]}`
			created := do(t, h, http.MethodPost, Prefix+path, body, bearer(adminToken))
			if created.Code != 201 {
				t.Fatalf("%s with scope %q: status %d body %s", path, scope.Name, created.Code, created.Body.String())
			}
			if !store.created || len(store.scopes) != 1 || store.scopes[0] != scope.Name {
				t.Fatalf("%s with scope %q stored %v", path, scope.Name, store.scopes)
			}
			if !strings.Contains(created.Body.String(), `"`+scope.Name+`"`) {
				t.Fatalf("%s response omits the granted scope: %s", path, created.Body.String())
			}
		}
	}
}

func TestV2APIKeyCreateReportsUnknownScope(t *testing.T) {
	for _, path := range []string{"/api-keys", adminAPIKeyPath} {
		h, store := realAPIKeyHandler()
		rec := do(t, h, http.MethodPost, Prefix+path, `{"label":"Tool","scopes":["admin:everything"]}`, bearer(adminToken))
		requireProblem(t, rec, TypeValidationFailed)
		if store.created {
			t.Fatalf("%s: an unknown scope reached storage", path)
		}
		var p Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Errors) != 1 || p.Errors[0].Location != "body.scopes" || p.Errors[0].Code != codeInvalid {
			t.Fatalf("%s: errors = %+v", path, p.Errors)
		}
		if !strings.Contains(p.Errors[0].Detail, "admin:everything") {
			t.Fatalf("%s: detail does not name the scope: %q", path, p.Errors[0].Detail)
		}
	}
}
