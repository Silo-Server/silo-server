package handlers

import (
	"context"
	"errors"
	"slices"
	"testing"

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

// The v2 surface is the canonical editor, so the shared creation method
// validates against the full catalog; the frozen v1 transports keep the v1
// catalog.
func TestCreateAdminAPIKeyAcceptsFullScopeCatalog(t *testing.T) {
	for _, scope := range auth.APIKeyScopeCatalog() {
		store := &fakeAPIKeyStore{}
		key, err := NewAPIKeyHandler(store).CreateAdminAPIKey(t.Context(), 7, "new", []string{scope.Name})
		if err != nil || !slices.Equal(key.Scopes, []string{scope.Name}) {
			t.Fatalf("scope %q rejected by the canonical editor: %v", scope.Name, err)
		}
	}
	store := &fakeAPIKeyStore{}
	_, err := NewAPIKeyHandler(store).CreateAdminAPIKey(t.Context(), 7, "new", []string{"admin:everything"})
	var unknown *auth.UnknownAPIKeyScopeError
	if !errors.Is(err, ErrInvalidAPIKeyCreation) || !errors.As(err, &unknown) || unknown.Scope != "admin:everything" || store.created {
		t.Fatalf("unknown scope must name itself and stay out of storage: %v", err)
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
