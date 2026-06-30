package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

type fakeAuthCapabilitiesAuthorizer struct {
	allowed        map[auth.ACLAction]bool
	requirePrimary bool
	requests       *[]auth.AccessRequest
}

func (f fakeAuthCapabilitiesAuthorizer) Authorize(_ context.Context, request auth.AccessRequest) (auth.AccessDecision, error) {
	if f.requests != nil {
		*f.requests = append(*f.requests, request)
	}
	if f.requirePrimary && !request.PrimaryProfile {
		return auth.AccessDecision{Allowed: false}, nil
	}
	return auth.AccessDecision{Allowed: f.allowed[request.Action]}, nil
}

func (f fakeAuthCapabilitiesAuthorizer) Explain(_ context.Context, request auth.AccessRequest) (auth.AccessExplanation, error) {
	decision, err := f.Authorize(context.Background(), request)
	return auth.AccessExplanation{Request: request, Decision: decision}, err
}

func TestHandleAdminCapabilitiesListsExplicitDelegatedGrants(t *testing.T) {
	handler := &AuthHandler{
		AdminAuthorizer: fakeAuthCapabilitiesAuthorizer{
			allowed: map[auth.ACLAction]bool{
				auth.ActionUsersManage:     true,
				auth.ActionRequestsApprove: true,
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/admin-capabilities", nil)
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    42,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
	}))
	rec := httptest.NewRecorder()

	handler.HandleAdminCapabilities(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminCapabilitiesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := []string{"users.manage", "requests.approve"}
	if len(resp.Actions) != len(want) {
		t.Fatalf("actions = %#v, want %#v", resp.Actions, want)
	}
	for i := range want {
		if resp.Actions[i] != want[i] {
			t.Fatalf("actions = %#v, want %#v", resp.Actions, want)
		}
	}
}

func TestHandleAdminCapabilitiesHonorsPrimaryProfileForLegacyAdmin(t *testing.T) {
	handler := &AuthHandler{
		AdminAuthorizer: fakeAuthCapabilitiesAuthorizer{
			allowed: map[auth.ACLAction]bool{
				auth.ActionUsersManage: true,
			},
			requirePrimary: true,
		},
		PrimaryProfileChecker: func(context.Context, int, string) (bool, bool, error) {
			return false, true, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/admin-capabilities", nil)
	req.Header.Set("X-Profile-Id", "non-primary-profile")
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    42,
		Role:      "admin",
		TokenType: auth.TokenTypeAccess,
	}))
	rec := httptest.NewRecorder()

	handler.HandleAdminCapabilities(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminCapabilitiesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Actions) != 0 {
		t.Fatalf("actions = %#v, want empty", resp.Actions)
	}
}

func TestHandleCapabilitiesListsUserFacingGrantsForActiveProfile(t *testing.T) {
	var requests []auth.AccessRequest
	handler := &AuthHandler{
		AdminAuthorizer: fakeAuthCapabilitiesAuthorizer{
			allowed: map[auth.ACLAction]bool{
				auth.ActionPlaybackPlay:        true,
				auth.ActionPersonalListsManage: true,
				auth.ActionRequestsCreate:      true,
			},
			requests: &requests,
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/capabilities", nil)
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    42,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
	}), "profile-1"))
	rec := httptest.NewRecorder()

	handler.HandleCapabilities(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminCapabilitiesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := []string{"playback.play", "personal_lists.manage", "requests.create"}
	if len(resp.Actions) != len(want) {
		t.Fatalf("actions = %#v, want %#v", resp.Actions, want)
	}
	for i := range want {
		if resp.Actions[i] != want[i] {
			t.Fatalf("actions = %#v, want %#v", resp.Actions, want)
		}
	}
	if len(requests) != len(auth.UserFacingCapabilityActions()) {
		t.Fatalf("authorizer calls = %d, want %d", len(requests), len(auth.UserFacingCapabilityActions()))
	}
	for _, request := range requests {
		if request.ProfileID != "profile-1" {
			t.Fatalf("ProfileID = %q, want profile-1", request.ProfileID)
		}
		if request.ResourceID != "*" {
			t.Fatalf("ResourceID = %q, want *", request.ResourceID)
		}
	}
}
