package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

func runActingAdminMiddleware(t *testing.T, role, profileID string, check PrimaryProfileChecker) int {
	t.Helper()
	next := RequireActingAdmin(check)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/sessions", nil)
	if profileID != "" {
		req.Header.Set("X-Profile-Id", profileID)
	}
	ctx := SetClaims(req.Context(), &auth.Claims{UserID: 7, Role: role, TokenType: auth.TokenTypeAccess})
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req.WithContext(ctx))
	return rec.Code
}

func primaryChecker(isPrimary, found bool, err error) PrimaryProfileChecker {
	return func(_ context.Context, _ int, _ string) (bool, bool, error) {
		return isPrimary, found, err
	}
}

func TestRequireActingAdmin_RejectsNonAdmin(t *testing.T) {
	if code := runActingAdminMiddleware(t, "user", "", nil); code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireActingAdmin_AllowsAdminWithoutProfile(t *testing.T) {
	check := primaryChecker(false, true, nil) // must not be consulted with no declared profile
	if code := runActingAdminMiddleware(t, "admin", "", check); code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireActingAdmin_AllowsPrimaryProfile(t *testing.T) {
	check := primaryChecker(true, true, nil)
	if code := runActingAdminMiddleware(t, "admin", "prof-1", check); code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}

func TestRequireActingAdmin_RejectsNonPrimaryProfile(t *testing.T) {
	check := primaryChecker(false, true, nil)
	if code := runActingAdminMiddleware(t, "admin", "prof-2", check); code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireActingAdmin_RejectsUnknownProfile(t *testing.T) {
	// A declared profile that doesn't resolve to one of the caller's profiles
	// fails closed; otherwise a bogus X-Profile-Id would restore admin powers.
	check := primaryChecker(false, false, nil)
	if code := runActingAdminMiddleware(t, "admin", "prof-x", check); code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireActingAdmin_CheckerErrorIsInternalError(t *testing.T) {
	check := primaryChecker(false, false, errors.New("db down"))
	if code := runActingAdminMiddleware(t, "admin", "prof-1", check); code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", code, http.StatusInternalServerError)
	}
}

func TestRequireActingAdmin_NilCheckerBehavesLikeRequireAdmin(t *testing.T) {
	if code := runActingAdminMiddleware(t, "admin", "prof-2", nil); code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", code, http.StatusNoContent)
	}
}
