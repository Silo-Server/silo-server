package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// actingAdminHousehold is an admin account's household: profile-1 is the
// primary (with primaryPIN when set), kid carries a rating ceiling and library
// restrictions, and alex is PIN-protected.
func actingAdminHousehold(t *testing.T, primaryPIN string) userstore.UserStore {
	t.Helper()
	ctx := context.Background()
	store := newProfileTestStore(t)
	rating := "PG"
	restricted := true
	for _, p := range []userstore.Profile{{ID: "kid", Name: "Kiddo"}, {ID: "alex", Name: "Alex"}} {
		if err := store.CreateProfile(ctx, p); err != nil {
			t.Fatalf("create %s: %v", p.ID, err)
		}
	}
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{
		MaxContentRating: &rating, LibraryRestrictionsEnabled: &restricted,
	}); err != nil {
		t.Fatalf("limit kid: %v", err)
	}
	pin := "4321"
	if err := store.UpdateProfile(ctx, "alex", userstore.UpdateProfileInput{PINHash: storedPIN(t, pin)}); err != nil {
		t.Fatalf("lock alex: %v", err)
	}
	if primaryPIN != "" {
		if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{PINHash: storedPIN(t, primaryPIN)}); err != nil {
			t.Fatalf("lock primary: %v", err)
		}
	}
	return store
}

func actingAdminProfileHandler(store userstore.UserStore) *ProfileHandler {
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{user: &models.User{ID: 1, Role: models.RoleAdmin, MaxProfiles: new(10)}}
	handler.ProfileTokens = access.NewProfileTokenService("test-secret-value-at-least-32-chars", time.Minute)
	return handler
}

// TestHouseholdManagement_AdminAPIKeyWithoutProfile: automation holding an
// admin API key keeps its profile-less household management on a restricted
// household (v1 and v2 share these handler methods), while a key naming a
// non-primary profile is refused like any other caller.
func TestHouseholdManagement_AdminAPIKeyWithoutProfile(t *testing.T) {
	apiKeyCtx := func(profileID string) context.Context {
		ctx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: 1, Role: models.RoleAdmin, TokenType: auth.TokenTypeAPIKey})
		if profileID != "" {
			ctx = apimw.SetProfileID(ctx, profileID)
		}
		return ctx
	}
	unverified := func(*userstore.Profile) error { return access.ErrProfileUnverified }

	t.Run("create without a profile", func(t *testing.T) {
		store := actingAdminHousehold(t, "1234")
		_, err := actingAdminProfileHandler(store).CreateProfile(apiKeyCtx(""), ProfileCreateCommand{
			UserID: 1, Request: ProfileCreateRequest{Name: "Extra"}, VerifyProfile: unverified,
		})
		if err != nil {
			t.Fatalf("admin API key create: %v", err)
		}
	})

	t.Run("household sessions without a profile", func(t *testing.T) {
		store := actingAdminHousehold(t, "1234")
		_, err := actingAdminProfileHandler(store).ListHouseholdSessions(apiKeyCtx(""), HouseholdSessionsQuery{UserID: 1, VerifyProfile: unverified})
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
			t.Fatalf("admin API key refused household sessions: %v", err)
		}
	})

	t.Run("create naming a non-primary profile", func(t *testing.T) {
		store := actingAdminHousehold(t, "")
		_, err := actingAdminProfileHandler(store).CreateProfile(apiKeyCtx("kid"), ProfileCreateCommand{
			UserID: 1, ActiveProfileID: "kid", Request: ProfileCreateRequest{Name: "Extra"}, VerifyProfile: unverified,
		})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
			t.Fatalf("admin API key as kid = %v, want 403", err)
		}
	})
}

// actingAdminSession is the admin account's login session sess-1 acting as
// profileID.
func actingAdminSession(profileID string) context.Context {
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: 1, Role: models.RoleAdmin, TokenType: auth.TokenTypeAccess, SessionID: "sess-1"})
	if profileID != "" {
		ctx = apimw.SetProfileID(ctx, profileID)
	}
	return ctx
}

// TestProfileManagement_AdminAccountChildProfileIsRefused reproduces the
// bypass: on an admin account, a child profile could lift its own limits,
// clear another profile's PIN, and add or delete profiles, because the
// household check let the account's admin role stand in for the primary
// profile. It is now refused exactly as on a regular account.
func TestProfileManagement_AdminAccountChildProfileIsRefused(t *testing.T) {
	update := func(id string, req ProfileUpdateRequest) func(*ProfileHandler) error {
		return func(h *ProfileHandler) error {
			_, err := h.UpdateProfile(actingAdminSession("kid"), ProfileUpdateCommand{
				UserID: 1, ProfileID: id, ActiveProfileID: "kid", Request: req, VerifyProfile: noProfileVerification,
			})
			return err
		}
	}
	for _, tc := range []struct {
		name string
		run  func(*ProfileHandler) error
	}{
		{"create a profile", func(h *ProfileHandler) error {
			_, err := h.CreateProfile(actingAdminSession("kid"), ProfileCreateCommand{
				UserID: 1, ActiveProfileID: "kid", Request: ProfileCreateRequest{Name: "Extra"}, VerifyProfile: noProfileVerification,
			})
			return err
		}},
		{"lift its own rating ceiling", update("kid", ProfileUpdateRequest{MaxContentRating: new("")})},
		{"lift its own library restrictions", update("kid", ProfileUpdateRequest{LibraryRestrictionsEnabled: new(false)})},
		{"clear another profile's PIN", update("alex", ProfileUpdateRequest{PIN: new("")})},
		{"delete another profile", func(h *ProfileHandler) error {
			return h.DeleteProfile(actingAdminSession("kid"), ProfileDeleteCommand{
				UserID: 1, ProfileID: "alex", ActiveProfileID: "kid", VerifyProfile: noProfileVerification,
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := actingAdminHousehold(t, "")
			requireAPIStatus(t, tc.run(actingAdminProfileHandler(store)), http.StatusForbidden)

			profiles, err := store.ListProfiles(context.Background())
			if err != nil {
				t.Fatalf("list profiles: %v", err)
			}
			if len(profiles) != 3 {
				t.Fatalf("profile count = %d, want 3", len(profiles))
			}
			for _, p := range profiles {
				switch p.ID {
				case "kid":
					if p.MaxContentRating != "PG" || !p.LibraryRestrictionsEnabled {
						t.Fatalf("kid limits changed: %+v", p)
					}
				case "alex":
					if p.PINHash == "" {
						t.Fatal("alex lost her PIN")
					}
				}
			}
		})
	}
}

// TestMayCreatePersonalAPIKey_RequiresActingAdmin: an API key skips every
// profile's PIN, so a child profile on an admin account must not mint one;
// only the verified primary profile, or a profile-less request on a household
// with no limited profile, may.
func TestMayCreatePersonalAPIKey_RequiresActingAdmin(t *testing.T) {
	verified := func(*userstore.Profile) error { return nil }
	for _, tc := range []struct {
		name, profileID, primaryPIN string
		unrestricted                bool
		verify                      func(*userstore.Profile) error
		want                        bool
		wantErr                     error
	}{
		{name: "child profile", profileID: "kid"},
		{name: "PIN-locked sibling", profileID: "alex", verify: verified},
		{name: "profile-less on a restricted household"},
		{name: "PIN-locked primary unverified", profileID: "profile-1", primaryPIN: "1234", wantErr: access.ErrProfileUnverified},
		{name: "PIN-locked primary verified", profileID: "profile-1", primaryPIN: "1234", verify: verified, want: true},
		{name: "primary without a PIN", profileID: "profile-1", want: true},
		{name: "profile-less on an unrestricted household", unrestricted: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var store userstore.UserStore
			if tc.unrestricted {
				store = newProfileTestStore(t)
			} else {
				store = actingAdminHousehold(t, tc.primaryPIN)
			}
			h := NewAPIKeyHandler(&fakeAPIKeyStore{})
			h.Stores = testUserStoreProvider{store: store}
			ok, err := h.MayCreatePersonalAPIKey(actingAdminSession(tc.profileID), 1, tc.profileID, tc.verify)
			if ok != tc.want || !errors.Is(err, tc.wantErr) {
				t.Fatalf("may create = (%v, %v), want (%v, %v)", ok, err, tc.want, tc.wantErr)
			}
		})
	}

	ok, err := NewAPIKeyHandler(&fakeAPIKeyStore{}).MayCreatePersonalAPIKey(actingAdminSession("profile-1"), 1, "profile-1", verified)
	if ok || err != nil {
		t.Fatalf("without stores = (%v, %v), want a closed refusal", ok, err)
	}
}

// TestMayCreateProfileAPIKey: the primary profile mints a user key for any
// profile on the account or for the account; every other profile mints only
// a key bound to itself, and a PIN-locked one only with its own PIN proof.
// A credential that is not the account's own login session mints none.
func TestMayCreateProfileAPIKey(t *testing.T) {
	verified := func(*userstore.Profile) error { return nil }
	unverified := func(*userstore.Profile) error { return access.ErrProfileUnverified }
	for _, tc := range []struct {
		name, acting, target string
		verify               func(*userstore.Profile) error
		want                 bool
		wantErr              error
	}{
		{name: "primary for itself", acting: "profile-1", target: "profile-1", verify: verified, want: true},
		{name: "primary for the account", acting: "profile-1", verify: verified, want: true},
		{name: "primary for a child", acting: "profile-1", target: "kid", verify: verified, want: true},
		{name: "child for itself", acting: "kid", target: "kid", verify: verified, want: true},
		{name: "child for the account", acting: "kid", verify: verified},
		{name: "child for a sibling", acting: "kid", target: "alex", verify: verified},
		{name: "child for the primary", acting: "kid", target: "profile-1", verify: verified},
		{name: "PIN-locked sibling without its PIN", acting: "alex", target: "alex", verify: unverified, wantErr: access.ErrProfileUnverified},
		{name: "PIN-locked sibling with its PIN", acting: "alex", target: "alex", verify: verified, want: true},
		{name: "no profile named", target: "kid", verify: verified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewAPIKeyHandler(&fakeAPIKeyStore{})
			h.Stores = testUserStoreProvider{store: actingAdminHousehold(t, "")}
			ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess})
			ok, acting, err := h.MayCreateProfileAPIKey(ctx, 1, tc.acting, tc.target, tc.verify)
			if ok != tc.want || !errors.Is(err, tc.wantErr) {
				t.Fatalf("may create = (%v, %v), want (%v, %v)", ok, err, tc.want, tc.wantErr)
			}
			// The verified profile travels to the insert, which rechecks it.
			if ok && acting.ID != tc.acting {
				t.Fatalf("acting profile = %+v, want %q", acting, tc.acting)
			}
		})
	}

	h := NewAPIKeyHandler(&fakeAPIKeyStore{})
	h.Stores = testUserStoreProvider{store: actingAdminHousehold(t, "")}
	admin := 9
	for name, claims := range map[string]*auth.Claims{
		"api key": {UserID: 1, Role: "user", TokenType: auth.TokenTypeAPIKey, APIKeyID: 3},
		"view-as": {UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess, ImpersonatorUserID: &admin},
	} {
		if ok, _, err := h.MayCreateProfileAPIKey(apimw.SetClaims(t.Context(), claims), 1, "profile-1", "kid", verified); ok || err != nil {
			t.Fatalf("%s = (%v, %v), want a refusal", name, ok, err)
		}
	}

	// Profiles stored outside Postgres: no user key, even for the primary.
	h.ProfilesOutsidePostgres = true
	own := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess})
	if ok, _, err := h.MayCreateProfileAPIKey(own, 1, "profile-1", "profile-1", verified); ok || err != nil {
		t.Fatalf("profiles outside postgres = (%v, %v), want a refusal", ok, err)
	}
}
