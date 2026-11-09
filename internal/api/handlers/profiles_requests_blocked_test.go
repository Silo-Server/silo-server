package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/onboarding"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Blocking a profile from requests is a parental control: the household
// manager sets and clears it, and the blocked profile cannot lift it.
func TestUpdateProfile_RequestsBlocked(t *testing.T) {
	store := newProfileTestStore(t) // profile-1 is the household primary
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	update := func(active string, blocked bool) (ProfileView, error) {
		return handler.UpdateProfile(profileServiceContext(active), ProfileUpdateCommand{
			UserID: 1, ProfileID: "profile-2", ActiveProfileID: active,
			Request:       ProfileUpdateRequest{RequestsBlocked: &blocked},
			VerifyProfile: noProfileVerification,
		})
	}

	view, err := update("profile-1", true)
	if err != nil {
		t.Fatalf("manager block: %v", err)
	}
	if !view.RequestsBlocked {
		t.Fatal("view RequestsBlocked = false after the manager blocked the profile")
	}

	_, err = update("profile-2", false)
	requireAPIStatus(t, err, http.StatusForbidden)
	if stored, _ := store.GetProfile(context.Background(), "profile-2"); !stored.RequestsBlocked {
		t.Fatal("self-service lifted the requests block")
	}

	view, err = update("profile-1", false)
	if err != nil {
		t.Fatalf("manager unblock: %v", err)
	}
	if view.RequestsBlocked {
		t.Fatal("view RequestsBlocked = true after the manager unblocked the profile")
	}
}

// Like the other access fields, a non-admin's bootstrap profile cannot arrive
// blocked.
func TestCreateProfile_RequestsBlockedNeedsAManager(t *testing.T) {
	store := newEmptyProfileTestStore(t)
	handler := NewProfileHandler(testUserStoreProvider{store: store})
	handler.UserRepo = testProfileUserRepo{user: &models.User{ID: 1, MaxProfiles: new(5)}}

	_, err := handler.CreateProfile(profileServiceContext(""), ProfileCreateCommand{
		UserID:        1,
		Request:       ProfileCreateRequest{Name: "Main", RequestsBlocked: true},
		VerifyProfile: noProfileVerification,
	})
	requireAPIStatus(t, err, http.StatusForbidden)
}

// /api/v1 is frozen: a v1 body cannot set the block, and v1 never shows it.
func TestRequestsBlockedStaysOffTheV1Wire(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	handler := NewProfileHandler(testUserStoreProvider{store: store})

	req := newAuthorizedProfileRequestWithRole(http.MethodPut, "/profiles/profile-2", `{"requests_blocked":true,"requests_allowed":false,"name":"Kids"}`, "user", "profile-1")
	rr := httptest.NewRecorder()
	handler.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("requests_")) {
		t.Fatalf("v1 response leaked the requests block: %s", rr.Body.String())
	}
	if stored, _ := store.GetProfile(context.Background(), "profile-2"); stored.RequestsBlocked {
		t.Fatal("a v1 body blocked the profile")
	}
}

// The tour's requests step opens a page a blocked profile is refused, so the
// flow leaves it out, as it does for a child profile.
func TestOnboardingFlow_BlockedProfileSkipsRequests(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Teen", RequestsBlocked: true}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	on := func(context.Context) bool { return true }
	handler := NewOnboardingHandler(testUserStoreProvider{store: store}, onboarding.Gates{Requests: on})

	for profileID, want := range map[string]bool{"profile-1": true, "profile-2": false} {
		flow, err := handler.ReadOnboardingFlow(context.Background(), 1, profileID, onboarding.SurfaceWeb)
		if err != nil {
			t.Fatalf("%s: %v", profileID, err)
		}
		got := false
		for _, step := range flow.Steps {
			got = got || step.ID == onboarding.StepIDRequests
		}
		if got != want {
			t.Fatalf("%s: requests step present = %v, want %v", profileID, got, want)
		}
	}
}
