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

func runMetadataCurationMiddleware(user *models.User, libraryIDs []int, role string) int {
	mw := NewPermissionMiddleware(
		fakePermissionUserLoader{user: user},
		fakeTargetLibraryResolver{ids: libraryIDs},
		nil,
	)
	next := mw.RequireMetadataCurationForItem(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, requestWithItemID(role))
	return rec.Code
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

func TestRequireMetadataCurationForItem_AdminOnPrimaryProfileBypasses(t *testing.T) {
	code := runMetadataCurationMiddlewareWithProfile(nil, nil, "admin", "prof-1", primaryChecker(true, true, nil))
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireMetadataCurationForItem_AdminOnNonPrimaryProfileWithoutAssignedPermission(t *testing.T) {
	admin := &models.User{ID: 7, Role: "admin", Enabled: true, LibraryIDs: nil, Permissions: nil}
	code := runMetadataCurationMiddlewareWithProfile(admin, []int{1}, "admin", "prof-2", primaryChecker(false, true, nil))
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireMetadataCurationForItem_AdminOnNonPrimaryProfileWithAssignedPermission(t *testing.T) {
	admin := &models.User{ID: 7, Role: "admin", Enabled: true, LibraryIDs: nil, Permissions: []string{"metadata_curation"}}
	code := runMetadataCurationMiddlewareWithProfile(admin, []int{1}, "admin", "prof-2", primaryChecker(false, true, nil))
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireMetadataCurationForItem_AllowsAdmin(t *testing.T) {
	code := runMetadataCurationMiddleware(nil, nil, "admin")
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireMetadataCurationForItem_RejectsUserWithoutPermission(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: []int{1}, Permissions: nil}
	code := runMetadataCurationMiddleware(user, []int{1}, "user")
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireMetadataCurationForItem_AllowsUnrestrictedCurator(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: nil, Permissions: []string{"metadata_curation"}}
	code := runMetadataCurationMiddleware(user, []int{1, 2}, "user")
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireMetadataCurationForItem_AllowsWhenAllTargetLibrariesAreAllowed(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: []int{1, 2, 3}, Permissions: []string{"metadata_curation"}}
	code := runMetadataCurationMiddleware(user, []int{1, 3}, "user")
	if code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireMetadataCurationForItem_RejectsWhenAnyTargetLibraryIsOutsideAccess(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: []int{1}, Permissions: []string{"metadata_curation"}}
	code := runMetadataCurationMiddleware(user, []int{1, 2}, "user")
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireMetadataCurationForItem_NotFoundWhenTargetHasNoLibraries(t *testing.T) {
	user := &models.User{ID: 7, Role: "user", Enabled: true, LibraryIDs: nil, Permissions: []string{"metadata_curation"}}
	code := runMetadataCurationMiddleware(user, nil, "user")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", code, http.StatusNotFound)
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
