package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileServiceContext is a non-admin caller acting as activeProfileID.
func profileServiceContext(activeProfileID string) context.Context {
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess})
	if activeProfileID != "" {
		ctx = apimw.SetProfileID(ctx, activeProfileID)
	}
	return ctx
}

func noProfileVerification(string) error { return nil }

func requireAPIStatus(t *testing.T, err error, status int) *APIError {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != status {
		t.Fatalf("error = %v, want an API error with status %d", err, status)
	}
	return apiErr
}

// The advisory-age limit is a parental control: the household manager sets and
// clears it, within range, and the profile it restricts cannot lift it.
func TestUpdateProfile_AdvisoryAgeLimit(t *testing.T) {
	store := newProfileTestStore(t) // profile-1 is the household primary
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	update := func(active string, age *int) (ProfileView, error) {
		return handler.UpdateProfile(profileServiceContext(active), ProfileUpdateCommand{
			UserID: 1, ProfileID: "profile-2", ActiveProfileID: active,
			Request:       ProfileUpdateRequest{MaxAdvisoryAge: age},
			VerifyProfile: noProfileVerification,
		})
	}

	view, err := update("profile-1", ptr(10))
	if err != nil {
		t.Fatalf("manager set: %v", err)
	}
	if view.MaxAdvisoryAge != 10 {
		t.Fatalf("view MaxAdvisoryAge = %d, want 10", view.MaxAdvisoryAge)
	}

	// The restricted profile may not change its own limit.
	_, err = update("profile-2", ptr(0))
	requireAPIStatus(t, err, http.StatusForbidden)
	if stored, _ := store.GetProfile(context.Background(), "profile-2"); stored.MaxAdvisoryAge != 10 {
		t.Fatalf("self-service changed the limit to %d", stored.MaxAdvisoryAge)
	}

	for _, age := range []int{-1, 22} {
		_, err = update("profile-1", ptr(age))
		if apiErr := requireAPIStatus(t, err, http.StatusBadRequest); apiErr.Field != "max_advisory_age" {
			t.Fatalf("age %d: field = %q, want max_advisory_age", age, apiErr.Field)
		}
	}

	view, err = update("profile-1", ptr(0))
	if err != nil {
		t.Fatalf("manager clear: %v", err)
	}
	if view.MaxAdvisoryAge != 0 {
		t.Fatalf("view MaxAdvisoryAge after clear = %d, want 0", view.MaxAdvisoryAge)
	}
}

// A non-admin creating the household's first profile becomes its primary, so
// the profile cannot arrive with access restrictions already applied.
func TestCreateProfile_AdvisoryAgeLimitNeedsAManager(t *testing.T) {
	store := newEmptyProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{user: &models.User{ID: 1, MaxProfiles: 5}}

	_, err := handler.CreateProfile(profileServiceContext(""), ProfileCreateCommand{
		UserID:        1,
		Request:       ProfileCreateRequest{Name: "Main", MaxAdvisoryAge: 10},
		VerifyProfile: noProfileVerification,
	})
	requireAPIStatus(t, err, http.StatusForbidden)

	if _, err := handler.CreateProfile(profileServiceContext(""), ProfileCreateCommand{
		UserID: 1, Request: ProfileCreateRequest{Name: "Main"}, VerifyProfile: noProfileVerification,
	}); err != nil {
		t.Fatalf("bootstrap create: %v", err)
	}
	view, err := handler.CreateProfile(profileServiceContext(""), ProfileCreateCommand{
		UserID: 1, ActiveProfileID: firstProfileID(t, store),
		Request:       ProfileCreateRequest{Name: "Kids", MaxAdvisoryAge: 9},
		VerifyProfile: noProfileVerification,
	})
	if err != nil {
		t.Fatalf("manager create: %v", err)
	}
	if view.MaxAdvisoryAge != 9 {
		t.Fatalf("view MaxAdvisoryAge = %d, want 9", view.MaxAdvisoryAge)
	}
}

func firstProfileID(t *testing.T, store userstore.UserStore) string {
	t.Helper()
	profiles, err := store.ListProfiles(context.Background())
	if err != nil || len(profiles) == 0 {
		t.Fatalf("list profiles: %v (%d)", err, len(profiles))
	}
	return profiles[0].ID
}

// /api/v1 is frozen: a v1 body can neither set the limit nor read it back.
func TestAdvisoryAgeLimitStaysOffTheV1Wire(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids", MaxAdvisoryAge: 10}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(http.MethodPut, "/profiles/profile-2", `{"max_advisory_age":3,"name":"Kids"}`, "user", "profile-1")
	rr := httptest.NewRecorder()
	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("advisory")) || bytes.Contains(rr.Body.Bytes(), []byte("AdvisoryAge")) {
		t.Fatalf("v1 response leaked the advisory-age limit: %s", rr.Body.String())
	}
	if stored, _ := store.GetProfile(context.Background(), "profile-2"); stored.MaxAdvisoryAge != 10 {
		t.Fatalf("a v1 body changed the limit to %d", stored.MaxAdvisoryAge)
	}
}

// Requiring an advisory age tightens a parental control, so it follows the
// limit's rules: the household manager sets and clears it, and the profile it
// restricts cannot lift it.
func TestUpdateProfile_RequireAdvisoryAge(t *testing.T) {
	store := newProfileTestStore(t) // profile-1 is the household primary
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids", MaxAdvisoryAge: 10}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	update := func(active string, require bool) (ProfileView, error) {
		return handler.UpdateProfile(profileServiceContext(active), ProfileUpdateCommand{
			UserID: 1, ProfileID: "profile-2", ActiveProfileID: active,
			Request:       ProfileUpdateRequest{RequireAdvisoryAge: &require},
			VerifyProfile: noProfileVerification,
		})
	}

	view, err := update("profile-1", true)
	if err != nil {
		t.Fatalf("manager set: %v", err)
	}
	if !view.RequireAdvisoryAge {
		t.Fatal("view RequireAdvisoryAge = false after the manager set it")
	}

	// The restricted profile may not loosen its own limit.
	_, err = update("profile-2", false)
	requireAPIStatus(t, err, http.StatusForbidden)
	if stored, _ := store.GetProfile(context.Background(), "profile-2"); !stored.RequireAdvisoryAge {
		t.Fatal("self-service cleared RequireAdvisoryAge")
	}

	view, err = update("profile-1", false)
	if err != nil {
		t.Fatalf("manager clear: %v", err)
	}
	if view.RequireAdvisoryAge {
		t.Fatal("view RequireAdvisoryAge = true after the manager cleared it")
	}
}

// Like the limit, a non-admin's bootstrap profile cannot arrive restricted.
func TestCreateProfile_RequireAdvisoryAgeNeedsAManager(t *testing.T) {
	store := newEmptyProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{user: &models.User{ID: 1, MaxProfiles: 5}}

	_, err := handler.CreateProfile(profileServiceContext(""), ProfileCreateCommand{
		UserID:        1,
		Request:       ProfileCreateRequest{Name: "Main", RequireAdvisoryAge: true},
		VerifyProfile: noProfileVerification,
	})
	requireAPIStatus(t, err, http.StatusForbidden)
}

// /api/v1 is frozen: a v1 body cannot set the strict flag either.
func TestRequireAdvisoryAgeStaysOffTheV1Wire(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids", MaxAdvisoryAge: 10}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(http.MethodPut, "/profiles/profile-2", `{"require_advisory_age":true,"name":"Kids"}`, "user", "profile-1")
	rr := httptest.NewRecorder()
	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("require_advisory")) {
		t.Fatalf("v1 response leaked require_advisory_age: %s", rr.Body.String())
	}
	if stored, _ := store.GetProfile(context.Background(), "profile-2"); stored.RequireAdvisoryAge {
		t.Fatal("a v1 body set RequireAdvisoryAge")
	}
}
