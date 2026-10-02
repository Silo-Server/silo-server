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

const (
	testOwnerID = 42
	// testAdminID is an admin that is not the Owner; 7 is another.
	testAdminID = 9
)

func ownerAccount() models.User {
	return models.User{ID: testOwnerID, Username: "owner", Email: "owner@example.test", Role: models.RoleAdmin, Enabled: true, IsOwner: true, LocalPasswordLoginEnabled: true, MaxProfiles: 5}
}

func adminAccount() models.User {
	return models.User{ID: testAdminID, Username: "admin", Email: "admin@example.test", Role: models.RoleAdmin, Enabled: true, LocalPasswordLoginEnabled: true, MaxProfiles: 5}
}

func userAccount() models.User {
	return models.User{ID: testAdminID, Username: "user", Email: "user@example.test", Role: models.RoleUser, Enabled: true, LocalPasswordLoginEnabled: true, MaxProfiles: 5}
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
	other, rename, demote, promote := 7, "renamed", models.RoleUser, models.RoleAdmin
	update := func(input models.UpdateUserInput) func(*AdminHandler, context.Context, int) error {
		return func(h *AdminHandler, ctx context.Context, id int) error {
			_, err := h.UpdateAdminAccount(ctx, id, -1, 0, input)
			return err
		}
	}
	remove := func(h *AdminHandler, ctx context.Context, id int) error { return h.DeleteAdminAccount(ctx, id, -1, 0) }
	cases := []struct {
		name    string
		stored  models.User
		actor   int
		run     func(*AdminHandler, context.Context, int) error
		allowed bool
	}{
		{"admin edits owner", ownerAccount(), other, update(models.UpdateUserInput{Username: &rename}), false},
		{"admin deletes owner", ownerAccount(), other, remove, false},
		{"owner edits self", ownerAccount(), testOwnerID, update(models.UpdateUserInput{Username: &rename}), true},
		{"owner demotes self", ownerAccount(), testOwnerID, update(models.UpdateUserInput{Role: &demote}), false},
		{"owner deletes self", ownerAccount(), testOwnerID, remove, false},
		{"admin edits admin", adminAccount(), other, update(models.UpdateUserInput{Username: &rename}), false},
		{"admin demotes admin", adminAccount(), other, update(models.UpdateUserInput{Role: &demote}), false},
		{"admin deletes admin", adminAccount(), other, remove, false},
		{"admin promotes user", userAccount(), other, update(models.UpdateUserInput{Role: &promote}), false},
		{"admin edits user", userAccount(), other, update(models.UpdateUserInput{Username: &rename}), true},
		{"admin deletes user", userAccount(), other, remove, true},
		{"admin edits self", adminAccount(), testAdminID, update(models.UpdateUserInput{Username: &rename}), true},
		{"admin demotes self", adminAccount(), testAdminID, update(models.UpdateUserInput{Role: &demote}), false},
		{"admin disables self", adminAccount(), testAdminID, update(models.UpdateUserInput{Enabled: new(false)}), false},
		{"admin deletes self", adminAccount(), testAdminID, remove, false},
		{"owner edits admin", adminAccount(), testOwnerID, update(models.UpdateUserInput{Username: &rename}), true},
		{"owner demotes admin", adminAccount(), testOwnerID, update(models.UpdateUserInput{Role: &demote}), true},
		{"owner deletes admin", adminAccount(), testOwnerID, remove, true},
		{"owner promotes user", userAccount(), testOwnerID, update(models.UpdateUserInput{Role: &promote}), true},
		{"admin limits self", adminAccount(), testAdminID, update(models.UpdateUserInput{MaxStreams: models.SetValue(2)}), false},
		{"admin resends own policy", adminAccount(), testAdminID, update(models.UpdateUserInput{Username: &rename, MaxStreams: models.ClearValue[int]()}), true},
		{"owner limits admin", adminAccount(), testOwnerID, update(models.UpdateUserInput{MaxStreams: models.SetValue(2)}), true},
	}
	for _, tc := range cases {
		repo := &mutatingUserRepo{current: tc.stored}
		err := tc.run(&AdminHandler{userRepo: repo}, claimsCtx(tc.actor), tc.stored.ID)
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

func TestCreateAdminNeedsOwner(t *testing.T) {
	for _, tc := range []struct {
		name   string
		actor  int
		status int
	}{
		{"admin creates admin", 7, http.StatusForbidden},
		{"owner creates admin", scopedKeyTestOwnerID, http.StatusCreated},
	} {
		h, repo := newScopedKeyAdminHandler(models.RoleUser)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", strings.NewReader(`{"username":"new","email":"new@example.test","password":"long-enough","role":"admin"}`))
		req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: tc.actor, Role: "admin", TokenType: auth.TokenTypeAccess, SessionID: "s1"}))
		rec := httptest.NewRecorder()
		h.HandleCreateUser(rec, req)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, rec.Code, tc.status, rec.Body.String())
		}
		if tc.status == http.StatusForbidden && (decodeErrorCode(t, rec) != codeOwnerProtected || repo.created != nil) {
			t.Errorf("%s: refusal %s, created %v", tc.name, rec.Body.String(), repo.created != nil)
		}
	}
}

func TestV1AdminUserHandlersProtectAdmins(t *testing.T) {
	for _, tc := range []struct {
		name, method, body string
		target             models.User
		actor, status      int
	}{
		{"admin edits admin", http.MethodPut, `{"email":"new@example.test"}`, adminAccount(), 7, http.StatusForbidden},
		{"admin demotes admin", http.MethodPut, `{"role":"user"}`, adminAccount(), 7, http.StatusForbidden},
		{"admin deletes admin", http.MethodDelete, "", adminAccount(), 7, http.StatusForbidden},
		{"admin promotes user", http.MethodPut, `{"role":"admin"}`, userAccount(), 7, http.StatusForbidden},
		{"admin edits user", http.MethodPut, `{"email":"new@example.test"}`, userAccount(), 7, http.StatusOK},
		{"admin edits self", http.MethodPut, `{"email":"new@example.test"}`, adminAccount(), testAdminID, http.StatusOK},
		{"admin demotes self", http.MethodPut, `{"role":"user"}`, adminAccount(), testAdminID, http.StatusForbidden},
		{"admin disables self", http.MethodPut, `{"enabled":false}`, adminAccount(), testAdminID, http.StatusForbidden},
		{"admin deletes self", http.MethodDelete, "", adminAccount(), testAdminID, http.StatusForbidden},
		{"admin saves self with its role unchanged", http.MethodPut, `{"role":"admin","email":"new@example.test"}`, adminAccount(), testAdminID, http.StatusOK},
		{"admin changes own policy", http.MethodPut, `{"download_transcode_allowed":false}`, adminAccount(), testAdminID, http.StatusForbidden},
		{"admin saves self with its policy unchanged", http.MethodPut, `{"email":"new@example.test","max_streams":null}`, adminAccount(), testAdminID, http.StatusOK},
		{"owner limits admin", http.MethodPut, `{"max_streams":2}`, adminAccount(), scopedKeyTestOwnerID, http.StatusOK},
		{"owner demotes admin", http.MethodPut, `{"role":"user"}`, adminAccount(), scopedKeyTestOwnerID, http.StatusOK},
		{"owner deletes admin", http.MethodDelete, "", adminAccount(), scopedKeyTestOwnerID, http.StatusNoContent},
	} {
		repo := &scopedKeyUserRepo{user: new(tc.target)}
		h := &AdminHandler{userRepo: repo}
		req := httptest.NewRequest(tc.method, "/api/v1/admin/users/9", strings.NewReader(tc.body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "9")
		req = req.WithContext(apimw.SetClaims(context.WithValue(req.Context(), chi.RouteCtxKey, rctx), &auth.Claims{UserID: tc.actor, Role: "admin", TokenType: auth.TokenTypeAccess, SessionID: "s1"}))
		rec := httptest.NewRecorder()
		if tc.method == http.MethodDelete {
			h.HandleDeleteUser(rec, req)
		} else {
			h.HandleUpdateUser(rec, req)
		}
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, rec.Code, tc.status, rec.Body.String())
			continue
		}
		if tc.status == http.StatusForbidden && (decodeErrorCode(t, rec) != codeOwnerProtected || repo.updated != nil || repo.deleted) {
			t.Errorf("%s: refusal %s, updated %v, deleted %v", tc.name, rec.Body.String(), repo.updated != nil, repo.deleted)
		}
	}
}

func TestTransferAdminOwnership(t *testing.T) {
	session := func(userID int) context.Context { return claimsCtx(userID) }
	apiKey := apimw.SetClaims(context.Background(), &auth.Claims{UserID: testOwnerID, Role: "admin", TokenType: auth.TokenTypeAPIKey})
	impersonating := apimw.SetClaims(context.Background(), &auth.Claims{UserID: testOwnerID, Role: "admin", TokenType: auth.TokenTypeAccess, SessionID: "s1", ImpersonatorUserID: new(1)})
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		repoErr error
		want    int
	}{
		{"owner moves ownership", session(testOwnerID), nil, 0},
		{"api key", apiKey, nil, http.StatusForbidden},
		{"impersonation session", impersonating, nil, http.StatusForbidden},
		{"caller is not the owner", session(7), auth.ErrNotOwner, http.StatusForbidden},
		{"target is not an enabled admin", session(testOwnerID), auth.ErrOwnershipTarget, http.StatusUnprocessableEntity},
	} {
		repo := &transferRepo{err: tc.repoErr}
		err := (&AdminHandler{userRepo: repo}).TransferAdminOwnership(tc.ctx, testAdminID)
		if tc.want == 0 {
			if err != nil || repo.from != testOwnerID || repo.to != testAdminID {
				t.Errorf("%s: err = %v, moved %d -> %d", tc.name, err, repo.from, repo.to)
			}
			continue
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != tc.want {
			t.Errorf("%s: err = %v, want status %d", tc.name, err, tc.want)
		}
		if tc.repoErr == nil && repo.to != 0 {
			t.Errorf("%s: the refused transfer reached the repository", tc.name)
		}
	}
}

// transferRepo records an ownership transfer.
type transferRepo struct {
	UserRepository
	err      error
	from, to int
}

func (r *transferRepo) TransferOwnership(_ context.Context, fromID, toID int) error {
	if r.err != nil {
		return r.err
	}
	r.from, r.to = fromID, toID
	return nil
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
		repo := &scopedKeyUserRepo{user: new(owner)}
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

	admin := adminAccount()
	h = &PasswordResetHandler{users: &scopedKeyUserRepo{user: &admin}}
	_, err = h.IssuePasswordReset(claimsCtx(7), passwordreset.IssueInput{UserID: testAdminID, IssuedBy: 7})
	requireOwnerProtected(t, "admin resets admin", err)
}

// fakeOwners stands in for the users table: testOwnerID is the Owner,
// testAdminID and 7 are other admins, and every other account is a user.
type fakeOwners struct{}

func (fakeOwners) CheckOwnerTargetByID(_ context.Context, actorID, userID int) error {
	role := models.RoleUser
	if userID == testOwnerID || userID == testAdminID || userID == 7 {
		role = models.RoleAdmin
	}
	return auth.CheckOwnerTarget(auth.OwnerActor{ID: actorID, IsOwner: actorID == testOwnerID}, &models.User{ID: userID, Role: role, IsOwner: userID == testOwnerID})
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
	if _, err := h.CreateAdminAPIKey(claimsCtx(7), testAdminID, "takeover", nil); err == nil || store.created {
		t.Fatalf("admin minting a key for another admin: %v", err)
	}
	if _, err := h.CreateAdminAPIKey(claimsCtx(7), 8, "ordinary", nil); err != nil || !store.created {
		t.Fatalf("admin minting a key for another account: %v", err)
	}
	store.created = false
	if _, err := h.CreateAdminAPIKey(claimsCtx(testOwnerID), testAdminID, "delegated", nil); err != nil || !store.created {
		t.Fatalf("owner minting a key for another admin: %v", err)
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
