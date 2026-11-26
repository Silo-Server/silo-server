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
// leaves the request anonymous instead of refusing it. Only a session that
// passes every check and must change its temporary password is marked so. It
// catches the mark set for a dead or invalid token, which would tell a signed
// out client to change its password.
func TestAttachSessionNeverRefuses(t *testing.T) {
	session := auth.Claims{UserID: 42, Role: "user", SessionID: "sess", TokenType: auth.TokenTypeAccess}
	restricted := session
	restricted.PasswordChangeRequired = true
	refresh := session
	refresh.TokenType = auth.TokenTypeRefresh
	staleRole := session
	staleRole.Role = "admin"

	restrictedStaleRole := restricted
	restrictedStaleRole.Role = "admin"
	restrictedRefresh := restricted
	restrictedRefresh.TokenType = auth.TokenTypeRefresh

	cases := []struct {
		name     string
		claims   auth.Claims
		sessions *fakeSessionValidator
		header   string
		wantUser int
		// wantMark: the request is marked as set aside only for a temporary
		// password (SessionMustChangePassword).
		wantMark bool
	}{
		{"login session", session, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 42, false},
		{"no credential", session, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "", 0, false},
		{"api key", session, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer sa_key", 0, false},
		{"refresh token", refresh, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 0, false},
		{"revoked session", session, &fakeSessionValidator{roles: map[string]string{}}, "Bearer token", 0, false},
		{"role changed", staleRole, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 0, false},
		{"temporary password", restricted, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 0, true},
		// The mark is for an otherwise valid session only: a client told to
		// change its password must not be holding a dead one.
		{"temporary password, revoked session", restricted, &fakeSessionValidator{roles: map[string]string{}}, "Bearer token", 0, false},
		{"temporary password, role changed", restrictedStaleRole, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 0, false},
		{"temporary password, refresh token", restrictedRefresh, &fakeSessionValidator{roles: map[string]string{"sess": "user"}}, "Bearer token", 0, false},
		{"temporary password, session store down", restricted, &fakeSessionValidator{err: errors.New("down")}, "Bearer token", 0, false},
		{"session store down", session, &fakeSessionValidator{err: errors.New("down")}, "Bearer token", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			am := NewAuthMiddleware(claimsValidator{tc.claims}, tc.sessions, nil, nil)
			gotUser, gotMark := -1, false
			h := am.AttachSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUser, gotMark = GetUserID(r.Context()), SessionMustChangePassword(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/v2/auth/device?code=482177", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent || gotUser != tc.wantUser || gotMark != tc.wantMark {
				t.Fatalf("status %d, user %d, mark %v; want 204, user %d, mark %v", rec.Code, gotUser, gotMark, tc.wantUser, tc.wantMark)
			}
		})
	}
}
