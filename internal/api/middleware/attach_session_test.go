package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// TestAttachSessionNeverRefuses: a valid login session's claims reach the
// handler; every other credential, and a session check that cannot run,
// leaves the request anonymous instead of refusing it.
func TestAttachSessionNeverRefuses(t *testing.T) {
	session := auth.Claims{UserID: 42, Role: "user", SessionID: "sess", TokenType: auth.TokenTypeAccess}
	restricted := session
	restricted.PasswordChangeRequired = true
	refresh := session
	refresh.TokenType = auth.TokenTypeRefresh
	staleRole := session
	staleRole.Role = "admin"

	cases := []struct {
		name     string
		claims   auth.Claims
		sessions *fakeSessionValidator
		header   string
		wantUser int
	}{
		{"login session", session, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 42},
		{"no credential", session, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "", 0},
		{"api key", session, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer sa_key", 0},
		{"refresh token", refresh, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 0},
		{"revoked session", session, &fakeSessionValidator{roles: map[string]string{}}, "Bearer token", 0},
		{"role changed", staleRole, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 0},
		{"temporary password", restricted, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 0},
		{"session store down", session, &fakeSessionValidator{err: errors.New("down")}, "Bearer token", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			am := NewAuthMiddleware(claimsValidator{tc.claims}, tc.sessions, nil, nil)
			gotUser := -1
			h := am.AttachSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUser = GetUserID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/v2/auth/device?code=482177", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent || gotUser != tc.wantUser {
				t.Fatalf("status %d, user %d; want 204 and user %d", rec.Code, gotUser, tc.wantUser)
			}
		})
	}
}
