package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

type pluginLaunchViewerResolver struct {
	calls int
}

func (r *pluginLaunchViewerResolver) Resolve(context.Context, access.ResolveInput) (access.Scope, error) {
	r.calls++
	return access.Scope{}, errors.New("viewer lookup unavailable")
}

func TestOptionalProfileViewerAccessPreservesProfilelessLaunch(t *testing.T) {
	resolver := &pluginLaunchViewerResolver{}
	viewer := apimw.NewViewerAccessMiddleware(resolver)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := optionalProfileViewerAccess(viewer)(next)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/plugin-launch", nil)
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || resolver.calls != 0 {
		t.Fatalf("profile-less launch status=%d viewer_calls=%d", rec.Code, resolver.calls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/plugin-launch", nil)
	req.Header.Set("X-Profile-Id", "profile-1")
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || resolver.calls != 1 {
		t.Fatalf("profile launch status=%d viewer_calls=%d", rec.Code, resolver.calls)
	}
}
