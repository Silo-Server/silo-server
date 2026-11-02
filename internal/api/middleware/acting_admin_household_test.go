package middleware

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func householdRequirement(required bool, err error) HouseholdProfileRequirement {
	return func(context.Context, int) (bool, error) { return required, err }
}

// TestActingAdminHouseholdRule: an admin request that declares no profile
// keeps admin powers only while the household has no PIN-protected or
// access-limited profile; a device signed into the account cannot regain them
// by dropping X-Profile-Id. API keys keep their profile-less access, a lookup
// error fails closed, and the declared-profile rule is unchanged. The legacy
// and the policy-backed gate answer every case identically.
func TestActingAdminHouseholdRule(t *testing.T) {
	pdp := newMiddlewarePolicyPDP(t)
	session := &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess}
	apiKey := &auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAPIKey}
	restricted := householdRequirement(true, nil)
	unrestricted := householdRequirement(false, nil)

	for _, tc := range []struct {
		name      string
		claims    *auth.Claims
		profileID string
		check     PrimaryProfileChecker
		household HouseholdProfileRequirement
		want      int
	}{
		{"no profile, restricted household", session, "", primaryChecker(true, true, nil), restricted, http.StatusForbidden},
		{"no profile, unrestricted household", session, "", primaryChecker(true, true, nil), unrestricted, http.StatusNoContent},
		{"no profile, lookup error", session, "", primaryChecker(true, true, nil), householdRequirement(false, errors.New("store down")), http.StatusInternalServerError},
		{"non-primary profile", session, "kid", primaryChecker(false, true, nil), unrestricted, http.StatusForbidden},
		{"primary profile, restricted household", session, "prof-1", primaryChecker(true, true, nil), restricted, http.StatusNoContent},
		{"API key without profile, restricted household", apiKey, "", primaryChecker(true, true, nil), restricted, http.StatusNoContent},
		{"API key with non-primary profile", apiKey, "kid", primaryChecker(false, true, nil), restricted, http.StatusForbidden},
		{"no requirement wired", session, "", primaryChecker(true, true, nil), nil, http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := captureActingAdminResponse(RequireActingAdmin(tc.check, tc.household), tc.claims, tc.profileID)
			if legacy.code != tc.want {
				t.Fatalf("legacy status = %d, want %d: %s", legacy.code, tc.want, legacy.body)
			}
			policyBacked := captureActingAdminResponse(NewPolicyActingAdminMiddleware(pdp, tc.check, tc.household), tc.claims, tc.profileID)
			assertMiddlewareResponsesEqual(t, policyBacked, legacy)
		})
	}
}

// TestMetadataCurationHouseholdRule: the curation gate's admin bypass follows
// the same profile-less rule; a refused admin falls back to the explicitly
// assigned permission like any other caller.
func TestMetadataCurationHouseholdRule(t *testing.T) {
	pdp := newMiddlewarePolicyPDP(t)
	admin := &models.User{ID: 7, Role: "admin", Enabled: true, Permissions: []string{}}
	for _, tc := range []struct {
		name      string
		household HouseholdProfileRequirement
		want      int
	}{
		{"restricted household", householdRequirement(true, nil), http.StatusForbidden},
		{"unrestricted household", householdRequirement(false, nil), http.StatusNoContent},
		{"lookup error", householdRequirement(false, errors.New("store down")), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := captureMetadataCurationResponse(
				NewPermissionMiddleware(fakePermissionUserLoader{user: admin}, fakeTargetLibraryResolver{ids: []int{1}}, primaryChecker(true, true, nil), tc.household),
				adminClaims(), "", "item-1")
			if legacy.code != tc.want {
				t.Fatalf("legacy status = %d, want %d: %s", legacy.code, tc.want, legacy.body)
			}
			policyBacked := captureMetadataCurationResponse(
				NewPolicyPermissionMiddleware(fakePermissionUserLoader{user: admin}, fakeTargetLibraryResolver{ids: []int{1}}, primaryChecker(true, true, nil), tc.household, pdp),
				adminClaims(), "", "item-1")
			assertMiddlewareResponsesEqual(t, policyBacked, legacy)
		})
	}
}

// TestMarkerEditHouseholdRule: the marker gate's admin grant is an
// acting-admin decision (#1911), so a profile-less admin session on a
// restricted household needs an assigned marker_edit like any other caller.
// The legacy and the policy-backed gate answer every case identically.
func TestMarkerEditHouseholdRule(t *testing.T) {
	pdp := newMiddlewarePolicyPDP(t)
	admin := &models.User{ID: 7, Role: "admin", Enabled: true, Permissions: []string{}}
	granted := &models.User{ID: 7, Role: "admin", Enabled: true, Permissions: []string{string(auth.PermissionMarkerEdit)}}
	for _, tc := range []struct {
		name      string
		user      *models.User
		household HouseholdProfileRequirement
		want      int
	}{
		{"restricted household", admin, householdRequirement(true, nil), http.StatusForbidden},
		{"restricted household, assigned grant", granted, householdRequirement(true, nil), http.StatusNoContent},
		{"unrestricted household", admin, householdRequirement(false, nil), http.StatusNoContent},
		{"lookup error", admin, householdRequirement(false, errors.New("store down")), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := captureMarkerEditResponse(
				NewPermissionMiddleware(fakePermissionUserLoader{user: tc.user}, nil, primaryChecker(true, true, nil), tc.household),
				adminClaims())
			if legacy.code != tc.want {
				t.Fatalf("legacy status = %d, want %d: %s", legacy.code, tc.want, legacy.body)
			}
			policyBacked := captureMarkerEditResponse(
				NewPolicyPermissionMiddleware(fakePermissionUserLoader{user: tc.user}, nil, primaryChecker(true, true, nil), tc.household, pdp),
				adminClaims())
			assertMiddlewareResponsesEqual(t, policyBacked, legacy)
		})
	}
}
