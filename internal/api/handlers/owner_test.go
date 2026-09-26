package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/passwordreset"
)

const testOwnerID = 42

func ownerAccount() models.User {
	return models.User{ID: testOwnerID, Username: "owner", Email: "owner@example.test", Role: models.RoleAdmin, Enabled: true, IsOwner: true, LocalPasswordLoginEnabled: true, MaxProfiles: 5}
}

func claimsCtx(userID int) context.Context {
	return apimw.SetClaims(context.Background(), &auth.Claims{UserID: userID, Role: "admin", TokenType: auth.TokenTypeAccess, SessionID: "s1"})
}

func requireOwnerProtected(t *testing.T, name string, err error) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != codeOwnerProtected {
		t.Errorf("%s: err = %v, want 403 owner_protected", name, err)
	}
}

func TestAdminAccountServiceProtectsOwner(t *testing.T) {
	other, rename, demote := 7, "renamed", models.RoleUser
	cases := []struct {
		name    string
		actor   int
		run     func(*AdminHandler, context.Context) error
		allowed bool
	}{
		{"admin edits owner", other, func(h *AdminHandler, ctx context.Context) error {
			_, err := h.UpdateAdminAccount(ctx, testOwnerID, -1, 0, models.UpdateUserInput{Username: &rename})
			return err
		}, false},
		{"admin deletes owner", other, func(h *AdminHandler, ctx context.Context) error {
			return h.DeleteAdminAccount(ctx, testOwnerID, -1, 0)
		}, false},
		{"owner edits self", testOwnerID, func(h *AdminHandler, ctx context.Context) error {
			_, err := h.UpdateAdminAccount(ctx, testOwnerID, -1, 0, models.UpdateUserInput{Username: &rename})
			return err
		}, true},
		{"owner demotes self", testOwnerID, func(h *AdminHandler, ctx context.Context) error {
			_, err := h.UpdateAdminAccount(ctx, testOwnerID, -1, 0, models.UpdateUserInput{Role: &demote})
			return err
		}, false},
		{"owner deletes self", testOwnerID, func(h *AdminHandler, ctx context.Context) error {
			return h.DeleteAdminAccount(ctx, testOwnerID, -1, 0)
		}, false},
	}
	for _, tc := range cases {
		repo := &mutatingUserRepo{current: ownerAccount()}
		err := tc.run(&AdminHandler{userRepo: repo}, claimsCtx(tc.actor))
		if tc.allowed {
			if err != nil || !repo.applied {
				t.Errorf("%s: err = %v, applied %v", tc.name, err, repo.applied)
			}
			continue
		}
		requireOwnerProtected(t, tc.name, err)
		if repo.applied {
			t.Errorf("%s: the refused change was applied", tc.name)
		}
	}
}

func TestV1AdminUserHandlersProtectOwner(t *testing.T) {
	request := func(h *AdminHandler, method string, actor int, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/admin/users/42", strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "42")
		req = req.WithContext(apimw.SetClaims(context.WithValue(req.Context(), chi.RouteCtxKey, rctx), &auth.Claims{UserID: actor, Role: "admin", TokenType: auth.TokenTypeAccess, SessionID: "s1"}))
		rec := httptest.NewRecorder()
		if method == http.MethodDelete {
			h.HandleDeleteUser(rec, req)
		} else {
			h.HandleUpdateUser(rec, req)
		}
		return rec
	}
	owner := ownerAccount()
	for _, tc := range []struct {
		name, method, body string
		actor, status      int
	}{
		{"admin edits owner", http.MethodPut, `{"email":"new@example.test"}`, 7, http.StatusForbidden},
		{"admin disables owner", http.MethodPut, `{"enabled":false}`, 7, http.StatusForbidden},
		{"admin deletes owner", http.MethodDelete, "", 7, http.StatusForbidden},
		{"owner disables self", http.MethodPut, `{"enabled":false}`, testOwnerID, http.StatusForbidden},
		{"owner deletes self", http.MethodDelete, "", testOwnerID, http.StatusForbidden},
		{"owner edits self", http.MethodPut, `{"email":"new@example.test"}`, testOwnerID, http.StatusOK},
	} {
		repo := &scopedKeyUserRepo{user: &owner}
		rec := request(&AdminHandler{userRepo: repo}, tc.method, tc.actor, tc.body)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, rec.Code, tc.status, rec.Body.String())
			continue
		}
		if tc.status == http.StatusForbidden {
			if code := decodeErrorCode(t, rec); code != codeOwnerProtected {
				t.Errorf("%s: error = %q, want owner_protected", tc.name, code)
			}
			if repo.updated != nil {
				t.Errorf("%s: the refused update reached the repository", tc.name)
			}
		}
	}
}

func TestPasswordResetRefusesOwnerForOtherAdmins(t *testing.T) {
	owner := ownerAccount()
	h := &PasswordResetHandler{users: &scopedKeyUserRepo{user: &owner}}
	_, err := h.IssuePasswordReset(claimsCtx(7), passwordreset.IssueInput{UserID: testOwnerID, IssuedBy: 7})
	requireOwnerProtected(t, "admin resets owner", err)
}

// fakeOwners stands in for the users table: testOwnerID is the Owner.
type fakeOwners struct{}

func (fakeOwners) CheckOwnerTargetByID(_ context.Context, actorID, userID int) error {
	return auth.CheckOwnerTarget(actorID, &models.User{ID: userID, IsOwner: userID == testOwnerID})
}

// ownerKeyStore holds one key, owned by the Owner.
type ownerKeyStore struct {
	fakeAPIKeyStore
	changed bool
}

func (s *ownerKeyStore) GetMetadataByID(context.Context, int64) (*models.APIKeyMetadata, error) {
	return &models.APIKeyMetadata{ID: 5, UserID: testOwnerID}, nil
}
func (s *ownerKeyStore) DeleteByAdmin(context.Context, int64) error { s.changed = true; return nil }
func (s *ownerKeyStore) UpdateTier(context.Context, int64, string) error {
	s.changed = true
	return nil
}
func (s *ownerKeyStore) DeleteByAdminConditional(context.Context, int64, auth.APIKeyPrecondition) error {
	s.changed = true
	return nil
}
func (s *ownerKeyStore) UpdateTierConditional(context.Context, int64, string, auth.APIKeyPrecondition) (*models.APIKeyMetadata, error) {
	s.changed = true
	return &models.APIKeyMetadata{ID: 5, UserID: testOwnerID}, nil
}

func TestAdminAPIKeysProtectOwner(t *testing.T) {
	newHandler := func() (*APIKeyHandler, *ownerKeyStore) {
		store := &ownerKeyStore{}
		h := NewAPIKeyHandler(store)
		h.Owners = fakeOwners{}
		return h, store
	}

	h, store := newHandler()
	_, err := h.CreateAdminAPIKey(claimsCtx(7), testOwnerID, "takeover", nil)
	requireOwnerProtected(t, "admin mints owner key", err)
	_, err = h.UpdateAdminAPIKeyTier(claimsCtx(7), 5, "elevated", auth.APIKeyPrecondition{})
	requireOwnerProtected(t, "admin retiers owner key", err)
	requireOwnerProtected(t, "admin revokes owner key", h.DeleteAdminAPIKey(claimsCtx(7), 5, auth.APIKeyPrecondition{}))
	if store.created || store.changed {
		t.Fatal("a refused key operation reached the store")
	}
	if _, err := h.CreateAdminAPIKey(claimsCtx(7), 8, "ordinary", nil); err != nil || !store.created {
		t.Fatalf("admin minting a key for another account: %v", err)
	}
	if _, err := h.CreateAdminAPIKey(claimsCtx(testOwnerID), testOwnerID, "own", nil); err != nil {
		t.Fatalf("owner minting its own key: %v", err)
	}
	if err := h.DeleteAdminAPIKey(claimsCtx(testOwnerID), 5, auth.APIKeyPrecondition{}); err != nil || !store.changed {
		t.Fatalf("owner revoking its own key: %v", err)
	}

	// The v1 transport refuses the same operations.
	for _, tc := range []struct {
		name, method, path, body string
		serve                    func(*APIKeyHandler, http.ResponseWriter, *http.Request)
	}{
		{"v1 create", http.MethodPost, "/api/v1/admin/api-keys", `{"label":"takeover","user_id":42}`, (*APIKeyHandler).HandleAdminCreateAPIKey},
		{"v1 delete", http.MethodDelete, "/api/v1/admin/api-keys/5", "", (*APIKeyHandler).HandleAdminDeleteAPIKey},
		{"v1 tier", http.MethodPut, "/api/v1/admin/api-keys/5/tier", `{"tier":"elevated"}`, (*APIKeyHandler).HandleAdminUpdateTier},
	} {
		h, store := newHandler()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "5")
		req = req.WithContext(apimw.SetClaims(context.WithValue(req.Context(), chi.RouteCtxKey, rctx), &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess, SessionID: "s1"}))
		rec := httptest.NewRecorder()
		tc.serve(h, rec, req)
		if rec.Code != http.StatusForbidden || decodeErrorCode(t, rec) != codeOwnerProtected || store.created || store.changed {
			t.Errorf("%s: status = %d body %s, store touched %v", tc.name, rec.Code, rec.Body.String(), store.created || store.changed)
		}
	}
}
