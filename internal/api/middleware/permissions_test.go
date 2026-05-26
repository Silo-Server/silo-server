package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

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
	)
	next := mw.RequireMetadataCurationForItem(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, requestWithItemID(role))
	return rec.Code
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
