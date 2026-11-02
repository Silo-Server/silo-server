package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakePermissionUserLoader struct {
	user *models.User
	err  error
}

func (f fakePermissionUserLoader) GetByID(context.Context, int) (*models.User, error) {
	return f.user, f.err
}

type fakeTargetLibraryResolver struct {
	ids []int
	err error
}

func (f fakeTargetLibraryResolver) ResolveMetadataTargetLibraryIDs(context.Context, string) ([]int, error) {
	return f.ids, f.err
}

func requestWithItemID(role string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/admin/items/item-1/refresh-metadata", nil)
	ctx := SetClaims(req.Context(), &auth.Claims{UserID: 7, Role: role, TokenType: auth.TokenTypeAccess})
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "item-1")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	return req.WithContext(ctx)
}

func runMetadataCurationMiddlewareWithProfile(
	user *models.User,
	libraryIDs []int,
	role, profileID string,
	check PrimaryProfileChecker,
) int {
	mw := NewPermissionMiddleware(
		fakePermissionUserLoader{user: user},
		fakeTargetLibraryResolver{ids: libraryIDs},
		check,
		nil,
	)
	next := mw.RequireMetadataCurationForItem(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := requestWithItemID(role)
	if profileID != "" {
		req.Header.Set("X-Profile-Id", profileID)
	}
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	return rec.Code
}

// TestRequireMetadataCurationForItem pins the legacy gate's absolute answers;
// the policy-backed parity test compares against these.
func TestRequireMetadataCurationForItem(t *testing.T) {
	admin := func(perms []string) *models.User {
		return &models.User{ID: 7, Role: "admin", Enabled: true, Permissions: perms}
	}
	curator := func(libraries []int) *models.User {
		return &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: libraries, Permissions: []string{"metadata_curation"}}
	}
	for _, tc := range []struct {
		name      string
		user      *models.User
		targetIDs []int
		role      string
		profileID string
		check     PrimaryProfileChecker
		want      int
	}{
		{"admin on primary profile bypasses", nil, nil, "admin", "prof-1", primaryChecker(true, true, nil), http.StatusNoContent},
		{"admin on non-primary profile without permission", admin(nil), []int{1}, "admin", "prof-2", primaryChecker(false, true, nil), http.StatusForbidden},
		{"admin on non-primary profile with permission", admin([]string{"metadata_curation"}), []int{1}, "admin", "prof-2", primaryChecker(false, true, nil), http.StatusNoContent},
		{"admin without profile", nil, nil, "admin", "", nil, http.StatusNoContent},
		{"user without permission", &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: []int{1}}, []int{1}, "user", "", nil, http.StatusForbidden},
		{"unrestricted curator", curator(nil), []int{1, 2}, "user", "", nil, http.StatusNoContent},
		{"all target libraries allowed", curator([]int{1, 2, 3}), []int{1, 3}, "user", "", nil, http.StatusNoContent},
		{"a target library outside access", curator([]int{1}), []int{1, 2}, "user", "", nil, http.StatusForbidden},
		{"target has no libraries", curator(nil), nil, "user", "", nil, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := runMetadataCurationMiddlewareWithProfile(tc.user, tc.targetIDs, tc.role, tc.profileID, tc.check); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
		})
	}
}

// runMetadataCurationMiddlewareWithGroup exercises the legacy gate with an
// access-group provider wired, which is how an inherited (NULL) library list
// resolves to the group's list.
func runMetadataCurationMiddlewareWithGroup(user *models.User, targetIDs []int, groups access.GroupPolicyProvider) int {
	mw := NewPermissionMiddleware(
		fakePermissionUserLoader{user: user},
		fakeTargetLibraryResolver{ids: targetIDs},
		nil,
		nil,
		groups,
	)
	next := mw.RequireMetadataCurationForItem(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, requestWithItemID("user"))
	return rec.Code
}

func TestRequireMetadataCurationForItem_InheritedGroupLibrariesApply(t *testing.T) {
	groupID := int64(3)
	user := &models.User{
		ID:            7,
		Role:          "user",
		Enabled:       true,
		AccessGroupID: &groupID,
		Permissions:   []string{string(auth.PermissionMetadataCuration)},
	}
	groups := middlewareGroupProvider{group: &access.GroupPolicy{LibraryIDs: []int{1}}}

	if code := runMetadataCurationMiddlewareWithGroup(user, []int{9}, groups); code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d for a target outside the inherited group libraries", code, http.StatusForbidden)
	}
	if code := runMetadataCurationMiddlewareWithGroup(user, []int{1}, groups); code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d for a target inside the inherited group libraries", code, http.StatusNoContent)
	}
}

func TestRequireMetadataCurationForItem_GroupLookupFailureIsForbidden(t *testing.T) {
	groupID := int64(3)
	user := &models.User{
		ID:            7,
		Role:          "user",
		Enabled:       true,
		AccessGroupID: &groupID,
		Permissions:   []string{string(auth.PermissionMetadataCuration)},
	}
	groups := middlewareGroupProvider{err: errors.New("group store down")}

	if code := runMetadataCurationMiddlewareWithGroup(user, []int{1}, groups); code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d when the group policy cannot be resolved", code, http.StatusForbidden)
	}
}
