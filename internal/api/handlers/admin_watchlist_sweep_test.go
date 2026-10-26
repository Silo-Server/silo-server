package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// watchlistSweepRecorder counts orphan-title sweeps and checks each runs on a
// live context.
type watchlistSweepRecorder struct {
	calls  int
	ctxErr error
}

func (r *watchlistSweepRecorder) SweepOrphanTitles(ctx context.Context) error {
	r.calls++
	r.ctxErr = ctx.Err()
	return nil
}

// Deleting an account drops its watchlist entries through the users foreign
// key; both delete paths then sweep the titles left without entries.
func TestAdminAccountDeleteSweepsWatchlistTitles(t *testing.T) {
	t.Run("v2 delete", func(t *testing.T) {
		sweeper := &watchlistSweepRecorder{}
		h := &AdminHandler{userRepo: &mutatingUserRepo{current: userAccount()}, WatchlistTitlesSweeper: sweeper}
		if err := h.DeleteAdminAccount(claimsCtx(7), testAdminID, -1, 0); err != nil {
			t.Fatal(err)
		}
		if sweeper.calls != 1 || sweeper.ctxErr != nil {
			t.Fatalf("sweeps = %d ctxErr = %v, want one on a live context", sweeper.calls, sweeper.ctxErr)
		}
	})
	t.Run("v2 refused delete", func(t *testing.T) {
		sweeper := &watchlistSweepRecorder{}
		h := &AdminHandler{userRepo: &mutatingUserRepo{current: adminAccount()}, WatchlistTitlesSweeper: sweeper}
		if err := h.DeleteAdminAccount(claimsCtx(7), testAdminID, -1, 0); err == nil {
			t.Fatal("admin deleted another admin")
		}
		if sweeper.calls != 0 {
			t.Fatalf("sweeps = %d after a refused delete", sweeper.calls)
		}
	})
	t.Run("v1 delete", func(t *testing.T) {
		sweeper := &watchlistSweepRecorder{}
		h := &AdminHandler{userRepo: &scopedKeyUserRepo{user: new(userAccount())}, WatchlistTitlesSweeper: sweeper}
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/9", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "9")
		req = req.WithContext(apimw.SetClaims(context.WithValue(req.Context(), chi.RouteCtxKey, rctx),
			&auth.Claims{UserID: 7, Role: "admin", TokenType: auth.TokenTypeAccess, SessionID: "s1"}))
		rec := httptest.NewRecorder()
		h.HandleDeleteUser(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if sweeper.calls != 1 {
			t.Fatalf("sweeps = %d, want 1", sweeper.calls)
		}
	})
}
