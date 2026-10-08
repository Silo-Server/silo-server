package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if err := store.UpdateProfile(ctx, "alex", userstore.UpdateProfileInput{PIN: &pin}); err != nil {
		t.Fatalf("lock alex: %v", err)
	}
	if primaryPIN != "" {
		if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{PIN: &primaryPIN}); err != nil {
			t.Fatalf("lock primary: %v", err)
		}
	}
	return store
}

func actingAdminProfileHandler(store userstore.UserStore) *ProfileHandler {
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{user: &models.User{ID: 1, Role: models.RoleAdmin, MaxProfiles: 10}}
	handler.ProfileTokens = access.NewProfileTokenService("test-secret-value-at-least-32-chars", time.Minute)
	return handler
}

// TestProfileManagement_AdminAccountChildProfileIsRefused reproduces the
// bypass: on an admin account, a child profile could lift its own limits,
// clear another profile's PIN, and add or delete profiles, because the
// household check let the account's admin role stand in for the primary
// profile. It is now refused exactly as on a regular account.
func TestProfileManagement_AdminAccountChildProfileIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, id, body string
	}{
		{"create a profile", http.MethodPost, "/profiles", "", `{"name":"Extra"}`},
		{"lift its own rating ceiling", http.MethodPut, "/profiles/kid", "kid", `{"max_content_rating":""}`},
		{"lift its own library restrictions", http.MethodPut, "/profiles/kid", "kid", `{"library_restrictions_enabled":false}`},
		{"clear another profile's PIN", http.MethodPut, "/profiles/alex", "alex", `{"pin":""}`},
		{"delete another profile", http.MethodDelete, "/profiles/alex", "alex", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := actingAdminHousehold(t, "")
			handler := actingAdminProfileHandler(store)
			req := newAuthorizedProfileRequestWithSession(tc.method, tc.path, tc.body, models.RoleAdmin, "kid", "sess-1")
			if tc.id != "" {
				req = withProfileRouteParam(req, "id", tc.id)
			}
			rr := httptest.NewRecorder()
			switch tc.method {
			case http.MethodPost:
				handler.HandleCreateProfile(rr, req)
			case http.MethodPut:
				handler.HandleUpdateProfile(rr, req)
			case http.MethodDelete:
				handler.HandleDeleteProfile(rr, req)
			}
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
			}

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

// TestProfileManagement_AdminAccountActsThroughPrimary: the acting admin is
// the primary profile, verified when it has a PIN; a profile-less admin
// request manages only a household with no limited profile.
func TestProfileManagement_AdminAccountActsThroughPrimary(t *testing.T) {
	create := func(t *testing.T, store userstore.UserStore, profileID, token string) *httptest.ResponseRecorder {
		t.Helper()
		handler := actingAdminProfileHandler(store)
		req := newAuthorizedProfileRequestWithSession(http.MethodPost, "/profiles", `{"name":"Extra"}`, models.RoleAdmin, profileID, "sess-1")
		if token != "" {
			req.Header.Set("X-Profile-Token", token)
		}
		rr := httptest.NewRecorder()
		handler.HandleCreateProfile(rr, req)
		return rr
	}

	t.Run("primary without a PIN may", func(t *testing.T) {
		if rr := create(t, actingAdminHousehold(t, ""), "profile-1", ""); rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("PIN-locked primary needs its token", func(t *testing.T) {
		store := actingAdminHousehold(t, "1234")
		rr := create(t, store, "profile-1", "")
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "verifying the primary profile PIN") {
			t.Fatalf("without token = %d %s, want 403 verification", rr.Code, rr.Body.String())
		}
		token, _, err := access.NewProfileTokenService("test-secret-value-at-least-32-chars", time.Minute).Mint(access.ProfileTokenClaims{
			UserID: 1, SessionID: "sess-1", ProfileID: "profile-1",
		})
		if err != nil {
			t.Fatalf("mint token: %v", err)
		}
		if rr := create(t, store, "profile-1", token); rr.Code != http.StatusCreated {
			t.Fatalf("with token = %d, want 201: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("profile-less on a restricted household may not", func(t *testing.T) {
		if rr := create(t, actingAdminHousehold(t, ""), "", ""); rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("profile-less on an unrestricted household may", func(t *testing.T) {
		if rr := create(t, newProfileTestStore(t), "", ""); rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
		}
	})
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
	unverified := func(string) error { return access.ErrProfileUnverified }

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

// TestHandleCreateAPIKey_RequiresActingAdmin: an API key skips every profile's
// PIN, so a child profile on an admin account must not mint one; only the
// verified primary profile, or a profile-less request on a household with no
// limited profile, may.
func TestHandleCreateAPIKey_RequiresActingAdmin(t *testing.T) {
	tokens := access.NewProfileTokenService("test-secret-value-at-least-32-chars", time.Minute)
	create := func(t *testing.T, store userstore.UserStore, profileID, token string) (*httptest.ResponseRecorder, *fakeAPIKeyStore) {
		t.Helper()
		keys := &fakeAPIKeyStore{}
		h := NewAPIKeyHandler(keys)
		h.Stores = testUserStoreProvider{store: store}
		h.ProfileTokens = tokens
		req := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"label":"ci"}`))
		req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{
			UserID: 1, Role: models.RoleAdmin, TokenType: auth.TokenTypeAccess, SessionID: "sess-1",
		}))
		if profileID != "" {
			req.Header.Set("X-Profile-Id", profileID)
		}
		if token != "" {
			req.Header.Set("X-Profile-Token", token)
		}
		rec := httptest.NewRecorder()
		h.HandleCreateAPIKey(rec, req)
		return rec, keys
	}

	for _, tc := range []struct {
		name, profileID string
		primaryPIN      string
		want            int
	}{
		{"child profile", "kid", "", http.StatusForbidden},
		{"PIN-locked sibling", "alex", "", http.StatusForbidden},
		{"profile-less on a restricted household", "", "", http.StatusForbidden},
		{"PIN-locked primary without a token", "profile-1", "1234", http.StatusForbidden},
		{"primary without a PIN", "profile-1", "", http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, keys := create(t, actingAdminHousehold(t, tc.primaryPIN), tc.profileID, "")
			if rec.Code != tc.want || keys.created != (tc.want == http.StatusCreated) {
				t.Fatalf("status = %d created = %v, want %d: %s", rec.Code, keys.created, tc.want, rec.Body.String())
			}
		})
	}

	t.Run("PIN-locked primary with a token", func(t *testing.T) {
		token, _, err := tokens.Mint(access.ProfileTokenClaims{UserID: 1, SessionID: "sess-1", ProfileID: "profile-1"})
		if err != nil {
			t.Fatalf("mint token: %v", err)
		}
		if rec, _ := create(t, actingAdminHousehold(t, "1234"), "profile-1", token); rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("profile-less on an unrestricted household", func(t *testing.T) {
		if rec, _ := create(t, newProfileTestStore(t), "", ""); rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("fails closed without stores", func(t *testing.T) {
		keys := &fakeAPIKeyStore{}
		h := NewAPIKeyHandler(keys)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"label":"ci"}`))
		req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: models.RoleAdmin, TokenType: auth.TokenTypeAccess, SessionID: "sess-1"}))
		rec := httptest.NewRecorder()
		h.HandleCreateAPIKey(rec, req)
		if rec.Code != http.StatusForbidden || keys.created {
			t.Fatalf("status = %d created = %v, want 403", rec.Code, keys.created)
		}
	})
}
