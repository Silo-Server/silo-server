package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

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
	return models.User{ID: testOwnerID, Username: "owner", Email: "owner@example.test", Role: models.RoleAdmin, Enabled: true, IsOwner: true, LocalPasswordLoginEnabled: true, MaxProfiles: new(5)}
}

func adminAccount() models.User {
	return models.User{ID: testAdminID, Username: "admin", Email: "admin@example.test", Role: models.RoleAdmin, Enabled: true, LocalPasswordLoginEnabled: true, MaxProfiles: new(5)}
}

func userAccount() models.User {
	return models.User{ID: testAdminID, Username: "user", Email: "user@example.test", Role: models.RoleUser, Enabled: true, LocalPasswordLoginEnabled: true, MaxProfiles: new(5)}
}

// providerAdminAccount is an admin that signs in only through a provider:
// its local password sign-in is off.
func providerAdminAccount() models.User {
	account := adminAccount()
	account.LocalPasswordLoginEnabled = false
	return account
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
		{"admin sets own password", adminAccount(), testAdminID, update(models.UpdateUserInput{Password: new("long-enough")}), true},
		{"provider-only admin sets own password", providerAdminAccount(), testAdminID, update(models.UpdateUserInput{Password: new("long-enough")}), false},
		{"owner sets a provider-only admin's password", providerAdminAccount(), testOwnerID, update(models.UpdateUserInput{Password: new("long-enough")}), true},
		{"admin makes itself break-glass", adminAccount(), testAdminID, update(models.UpdateUserInput{BreakGlass: new(true)}), false},
		{"owner makes an admin break-glass", adminAccount(), testOwnerID, update(models.UpdateUserInput{BreakGlass: new(true)}), true},
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

// TestAdminAccountServiceGuardsBreakGlass: a scoped API key, even the
// Owner's, may not change an admin's break-glass flag, and the flag is only
// valid on an admin account.
func TestAdminAccountServiceGuardsBreakGlass(t *testing.T) {
	scopedKey := func(userID int) context.Context {
		return apimw.SetClaims(context.Background(), &auth.Claims{UserID: userID, Role: "admin", TokenType: auth.TokenTypeAPIKey, APIKeyScopes: []string{"admin"}})
	}
	for _, tc := range []struct {
		name       string
		stored     models.User
		ctx        context.Context
		breakGlass bool
		status     int
		code       string
		field      string
	}{
		{"owner's scoped key on an admin", adminAccount(), scopedKey(testOwnerID), true, http.StatusForbidden, "insufficient_scope", ""},
		{"owner's scoped key clears an admin's flag", adminAccount(), scopedKey(testOwnerID), false, http.StatusForbidden, "insufficient_scope", ""},
		{"owner on a user account", userAccount(), claimsCtx(testOwnerID), true, http.StatusBadRequest, policyErrorBadRequest, "break_glass"},
		{"owner's session on an admin", adminAccount(), claimsCtx(testOwnerID), true, 0, "", ""},
	} {
		repo := &mutatingUserRepo{current: tc.stored}
		_, err := (&AdminHandler{userRepo: repo}).UpdateAdminAccount(tc.ctx, tc.stored.ID, -1, 0, models.UpdateUserInput{BreakGlass: new(tc.breakGlass)})
		if tc.status == 0 {
			if err != nil || !repo.applied {
				t.Errorf("%s: err = %v, applied %v", tc.name, err, repo.applied)
			}
			continue
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.Code != tc.code || apiErr.Field != tc.field {
			t.Errorf("%s: err = %#v, want %d %s field %q", tc.name, err, tc.status, tc.code, tc.field)
		}
		if repo.applied {
			t.Errorf("%s: the refused change was applied", tc.name)
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
}

// Only the Owner grants the admin role, including on account creation. The
// refusal comes before anything is written; the Owner's request passes the
// rule and reaches storage, which this test's pool cannot reach.
func TestCreateAdminNeedsOwner(t *testing.T) {
	unreachable, err := pgxpool.New(t.Context(), "postgres://silo@127.0.0.1:1/silo?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unreachable.Close)
	for _, tc := range []struct {
		name    string
		actor   int
		refused bool
	}{
		{"admin creates admin", 7, true},
		{"owner creates admin", scopedKeyTestOwnerID, false},
	} {
		h, repo := newScopedKeyAdminHandler(models.RoleUser)
		h.pool = unreachable
		_, err := h.CreateAdminAccount(claimsCtx(tc.actor), auth.CreateAccountInput{User: models.CreateUserInput{
			Username: "new", Email: "new@example.test", Password: "long-enough", Role: models.RoleAdmin,
		}})
		var apiErr *APIError
		protected := errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden && apiErr.Code == codeOwnerProtected
		if protected != tc.refused || repo.created != nil {
			t.Errorf("%s: err = %v, created %v; want owner_protected %v", tc.name, err, repo.created != nil, tc.refused)
		}
	}
}
