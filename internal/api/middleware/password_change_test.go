package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// claimsValidator accepts any bearer and returns fixed claims.
type claimsValidator struct{ claims auth.Claims }

func (v claimsValidator) ValidateToken(string) (*auth.Claims, error) {
	c := v.claims
	return &c, nil
}

func TestRequireAuthConfinesTemporaryPasswordSessions(t *testing.T) {
	restricted := auth.Claims{UserID: 42, Role: "user", SessionID: "sess", TokenType: auth.TokenTypeAccess, PasswordChangeRequired: true}
	serve := func(claims auth.Claims, method, path string) (*httptest.ResponseRecorder, *activitylog.LogContext) {
		am := NewAuthMiddleware(claimsValidator{claims}, &fakeSessionValidator{valid: map[string]bool{"sess": true}}, nil, nil)
		h := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		lc := &activitylog.LogContext{}
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(activitylog.SetLogContext(req.Context(), lc)))
		return rec, lc
	}

	for route := range passwordChangeRoutes {
		method, path, _ := strings.Cut(route, " ")
		if rec, _ := serve(restricted, method, path); rec.Code != http.StatusNoContent {
			t.Errorf("%s: status %d, want the route to stay reachable", route, rec.Code)
		}
	}

	for _, route := range [][2]string{
		{http.MethodGet, "/api/v2/profiles"},
		{http.MethodGet, "/api/v1/auth/sessions"},
		{http.MethodPost, "/api/v2/account/password/capability"},
		{http.MethodGet, "/api/v2/account/me/"},
	} {
		rec, lc := serve(restricted, route[0], route[1])
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status %d, want 403", route[0], route[1], rec.Code)
		}
		var body denialBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error != CodePasswordChangeRequired {
			t.Fatalf("%s %s: body %s", route[0], route[1], rec.Body.String())
		}
		// The refusal is still attributed to the account in the audit log.
		if lc.UserID == nil || *lc.UserID != 42 || lc.SessionID != "sess" {
			t.Fatalf("refusal not attributed: %+v", lc)
		}
	}

	settled := restricted
	settled.PasswordChangeRequired = false
	if rec, _ := serve(settled, http.MethodGet, "/api/v2/profiles"); rec.Code != http.StatusNoContent {
		t.Fatalf("settled session refused: %d", rec.Code)
	}
}
